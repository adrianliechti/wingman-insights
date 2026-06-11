package store

import (
	"context"
	"fmt"
	"time"
)

// AnomalyPoint is one time bucket of token consumption scored against a
// rolling baseline of the preceding buckets (per group and token type).
type AnomalyPoint struct {
	Bucket    time.Time `json:"bucket"`
	GroupKey  string    `json:"group_key"`
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

var anomalyGroupColumns = map[string]string{
	"none":    "''",
	"user":    "COALESCE(enduser_id, '')",
	"service": "COALESCE(service_name, '')",
	"model":   "COALESCE(request_model, '')",
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
		result = append(result, r)
	}
	return result, rows.Err()
}
