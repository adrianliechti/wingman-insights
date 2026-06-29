package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// AnomalyPoint is one time bucket of token consumption scored against a
// rolling baseline of the preceding buckets (per group and token type).
type AnomalyPoint struct {
	Bucket    time.Time `json:"bucket"`
	GroupKey  string    `json:"group_key"`
	Name      string    `json:"name,omitempty"` // resolved when group_by=user; else empty
	Kind      string    `json:"kind,omitempty"`
	TokenType string    `json:"token_type"`
	Tokens    float64   `json:"tokens"`
	Expected  float64   `json:"expected"`
	Score     float64   `json:"score"`
}

// baselineBuckets is how many preceding buckets form the rolling baseline,
// and minBaseline how many of them must exist before a bucket gets scored.
const (
	baselineBuckets = 24
	minBaseline     = 6
)

// anomalyGroupColumns are the genai_metrics group dimensions. Metrics carry no
// application identity (that lives on spans as app_id), so there is no app
// dimension here — see spanAnomalyGroupColumns.
var anomalyGroupColumns = map[string]string{
	"none":  "''",
	"user":  "COALESCE(enduser_id, '')",
	"model": "COALESCE(request_model, '')",
}

// QueryTokenAnomalies buckets input/output token consumption and scores each
// bucket against the rolling mean/stddev of the preceding buckets in the same
// (group, token type) series. The scan starts baselineBuckets before `from` so
// early buckets have a baseline. minScore > 0 returns only anomalous buckets
// (sorted by score); minScore == 0 returns the full scored series for charts.
func (s *Store) QueryTokenAnomalies(ctx context.Context, from, to time.Time, interval, groupBy string, minScore float64, f Filter) ([]AnomalyPoint, error) {
	groupCol, ok := anomalyGroupColumns[groupBy]
	if !ok {
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	clause, fargs := f.genaiClause()

	query := fmt.Sprintf(`
		WITH buckets AS (
			SELECT
				time_bucket(CAST(? AS INTERVAL), time) as bucket,
				%s as group_key,
				COALESCE(token_type, '') as token_type,
				SUM(sum) as tokens
			FROM genai_metrics
			WHERE metric_name = 'gen_ai.client.token.usage'
			  AND token_type IN ('input', 'output')
			  AND time >= CAST(? AS TIMESTAMP) - CAST(? AS INTERVAL) * %d
			  AND time <= ?%s
			GROUP BY bucket, group_key, token_type
		),
		scored AS (
			SELECT bucket, group_key, token_type, tokens,
				AVG(tokens) OVER w as expected,
				STDDEV_SAMP(tokens) OVER w as spread,
				COUNT(*) OVER w as baseline_n
			FROM buckets
			WINDOW w AS (
				PARTITION BY group_key, token_type
				ORDER BY bucket
				ROWS BETWEEN %d PRECEDING AND 1 PRECEDING
			)
		)
		SELECT * FROM (
			SELECT bucket, group_key, token_type, tokens,
				COALESCE(expected, tokens) as expected,
				CASE WHEN baseline_n >= %d
					THEN (tokens - expected) / GREATEST(COALESCE(spread, 0), 0.05 * expected, 1.0)
					ELSE 0 END as score
			FROM scored
			WHERE bucket >= ? AND bucket <= ?
		)
	`, groupCol, baselineBuckets, clause, baselineBuckets, minBaseline)

	args := append([]any{interval, from, interval, to}, fargs...)
	args = append(args, from, to)

	if minScore > 0 {
		query += " WHERE score >= ? ORDER BY score DESC"
		args = append(args, minScore)
	} else {
		query += " ORDER BY bucket"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AnomalyPoint
	for rows.Next() {
		var r AnomalyPoint
		if err := rows.Scan(&r.Bucket, &r.GroupKey, &r.TokenType, &r.Tokens, &r.Expected, &r.Score); err != nil {
			return nil, err
		}
		if groupBy == "user" {
			r.Name, r.Kind = s.resolveName(r.GroupKey, "")
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ScorePoint is one bucket of a single (group) series scored against its rolling
// baseline — the generic shape behind cost anomalies and the unified feed.
type ScorePoint struct {
	Bucket   time.Time `json:"bucket"`
	GroupKey string    `json:"group_key"`
	Name     string    `json:"name,omitempty"` // resolved when group_by=user; else empty
	Kind     string    `json:"kind,omitempty"`
	Value    float64   `json:"value"`
	Expected float64   `json:"expected"`
	Score    float64   `json:"score"`
}

// spanAnomalyGroupColumns are the genai_spans group dimensions: the end user is
// user_id (genai_metrics calls it enduser_id), and app_id (service.peer.name)
// gives the application dimension that metrics lack.
var spanAnomalyGroupColumns = map[string]string{
	"none":  "''",
	"user":  "COALESCE(user_id, '')",
	"app":   "COALESCE(app_id, '')",
	"model": "COALESCE(request_model, '')",
}

// rollingScoreSQL wraps a bucketed inner SELECT (which must yield columns named
// bucket, group_key, value, ordered by bucket internally) in the same rolling
// mean/stddev z-score as QueryTokenAnomalies. The inner SELECT binds its own args
// first; the wrapper then binds the visible window (from, to). The caller appends
// the optional "WHERE score >= ? ORDER BY score DESC" tail.
func rollingScoreSQL(inner string) string {
	return fmt.Sprintf(`
		WITH buckets AS (%s),
		scored AS (
			SELECT bucket, group_key, value,
				AVG(value) OVER w as expected,
				STDDEV_SAMP(value) OVER w as spread,
				COUNT(*) OVER w as baseline_n
			FROM buckets
			WINDOW w AS (
				PARTITION BY group_key
				ORDER BY bucket
				ROWS BETWEEN %d PRECEDING AND 1 PRECEDING
			)
		)
		SELECT * FROM (
			SELECT bucket, group_key, value,
				COALESCE(expected, value) as expected,
				CASE WHEN baseline_n >= %d
					THEN (value - expected) / GREATEST(COALESCE(spread, 0), 0.05 * expected, 1.0)
					ELSE 0 END as score
			FROM scored
			WHERE bucket >= ? AND bucket <= ?
		)`, inner, baselineBuckets, minBaseline)
}

// scoredSeries buckets `valueExpr` over `table` per `groupCol` and scores each
// bucket against the preceding baselineBuckets. The scan starts baselineBuckets
// before `from` so early visible buckets already have a baseline. minScore > 0
// returns only anomalous buckets (sorted by score); minScore == 0 returns the
// full scored series for charts.
func (s *Store) scoredSeries(ctx context.Context, table, valueExpr, extraWhere, groupCol, clause string, fargs []any, from, to time.Time, interval string, minScore float64) ([]ScorePoint, error) {
	inner := fmt.Sprintf(`
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			%s as group_key,
			%s as value
		FROM %s
		WHERE %s
		  AND time >= CAST(? AS TIMESTAMP) - CAST(? AS INTERVAL) * %d
		  AND time <= ?%s
		GROUP BY bucket, group_key`, groupCol, valueExpr, table, extraWhere, baselineBuckets, clause)

	query := rollingScoreSQL(inner)
	args := append([]any{interval, from, interval, to}, fargs...)
	args = append(args, from, to)
	if minScore > 0 {
		query += " WHERE score >= ? ORDER BY score DESC"
		args = append(args, minScore)
	} else {
		query += " ORDER BY bucket"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanScorePoints(rows)
}

func scanScorePoints(rows *sql.Rows) ([]ScorePoint, error) {
	defer rows.Close()
	var result []ScorePoint
	for rows.Next() {
		var p ScorePoint
		if err := rows.Scan(&p.Bucket, &p.GroupKey, &p.Value, &p.Expected, &p.Score); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// QueryCostAnomalies buckets spend (SUM of the materialized cost column) and
// scores each bucket against the rolling baseline of the preceding buckets in the
// same group — the spend counterpart to QueryTokenAnomalies.
func (s *Store) QueryCostAnomalies(ctx context.Context, from, to time.Time, interval, groupBy string, minScore float64, f Filter) ([]ScorePoint, error) {
	groupCol, ok := spanAnomalyGroupColumns[groupBy]
	if !ok {
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	clause, fargs := f.spansClause()
	pts, err := s.scoredSeries(ctx, "genai_spans", "SUM(COALESCE(cost, 0))",
		"(input_tokens > 0 OR output_tokens > 0)", groupCol, clause, fargs, from, to, interval, minScore)
	if err != nil {
		return nil, err
	}
	if groupBy == "user" {
		for i := range pts {
			pts[i].Name, pts[i].Kind = s.resolveName(pts[i].GroupKey, "")
		}
	}
	return pts, nil
}

// AnomalyFeedRow is one flagged (dimension, entity, metric) bucket in the unified
// feed — the cross-dimension ranking that replaces manually toggling group_by.
type AnomalyFeedRow struct {
	Bucket    time.Time `json:"bucket"`
	Dimension string    `json:"dimension"` // user | app | model
	GroupKey  string    `json:"group_key"`
	Name      string    `json:"name,omitempty"` // resolved when dimension=user; else empty
	Kind      string    `json:"kind,omitempty"`
	Metric    string    `json:"metric"` // cost | tokens
	Value     float64   `json:"value"`
	Expected  float64   `json:"expected"`
	Score     float64   `json:"score"`
}

// QueryAnomalyFeed scores spend and token consumption across the user, app and
// model dimensions and returns the top `limit` flagged buckets ranked by
// z-score. Cost comes from genai_spans; tokens from genai_metrics for user/model,
// but from genai_spans for app (the app dimension exists only on spans). The
// series are scored separately and merged in Go.
func (s *Store) QueryAnomalyFeed(ctx context.Context, from, to time.Time, interval string, minScore float64, limit int, f Filter) ([]AnomalyFeedRow, error) {
	if minScore <= 0 {
		minScore = 3
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	costClause, costArgs := f.spansClause()
	tokClause, tokArgs := f.genaiClause()

	var feed []AnomalyFeedRow
	add := func(dim, metric string, pts []ScorePoint) {
		for _, p := range pts {
			if p.GroupKey == "" {
				continue // unattributed traffic isn't an actionable culprit
			}
			row := AnomalyFeedRow{
				Bucket: p.Bucket, Dimension: dim, GroupKey: p.GroupKey,
				Metric: metric, Value: p.Value, Expected: p.Expected, Score: p.Score,
			}
			if dim == "user" {
				row.Name, row.Kind = s.resolveName(p.GroupKey, "")
			}
			feed = append(feed, row)
		}
	}

	for _, dim := range []string{"user", "app", "model"} {
		cost, err := s.scoredSeries(ctx, "genai_spans", "SUM(COALESCE(cost, 0))",
			"(input_tokens > 0 OR output_tokens > 0)", spanAnomalyGroupColumns[dim], costClause, costArgs, from, to, interval, minScore)
		if err != nil {
			return nil, err
		}
		add(dim, "cost", cost)

		var tok []ScorePoint
		if dim == "app" {
			// Metrics carry no app_id, so app token anomalies are span-sourced
			// (matching app cost above).
			tok, err = s.scoredSeries(ctx, "genai_spans", "SUM(input_tokens + output_tokens)",
				"(input_tokens > 0 OR output_tokens > 0)", spanAnomalyGroupColumns["app"], costClause, costArgs, from, to, interval, minScore)
		} else {
			tok, err = s.scoredSeries(ctx, "genai_metrics", "SUM(sum)",
				"metric_name = 'gen_ai.client.token.usage' AND token_type IN ('input', 'output')", anomalyGroupColumns[dim], tokClause, tokArgs, from, to, interval, minScore)
		}
		if err != nil {
			return nil, err
		}
		add(dim, "tokens", tok)
	}

	sort.Slice(feed, func(i, j int) bool { return feed[i].Score > feed[j].Score })
	if len(feed) > limit {
		feed = feed[:limit]
	}
	return feed, nil
}
