package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type GenAIMetricRow struct {
	ReceivedAt    time.Time
	Time          time.Time
	ServiceName   string
	AppID         string
	MetricName    string
	OperationName string
	ProviderName  string
	RequestModel  string
	TokenType     string
	ErrorType     string
	EndUserID     string
	EndUserEmail  string
	SessionID     string
	Count         int64
	Sum           float64
	Attributes    map[string]string
}

type TokenSummaryRow struct {
	ProviderName  string  `json:"provider_name"`
	RequestModel  string  `json:"request_model"`
	TokenType     string  `json:"token_type"`
	TotalTokens   float64 `json:"total_tokens"`
	TotalRequests int64   `json:"total_requests"`
}

type OperationRow struct {
	OperationName string  `json:"operation_name"`
	RequestModel  string  `json:"request_model"`
	ProviderName  string  `json:"provider_name"`
	TotalCount    int64   `json:"total_count"`
	AvgDuration   float64 `json:"avg_duration"`
}

type ActiveUsersRow struct {
	DAU int64 `json:"dau"`
	WAU int64 `json:"wau"`
	MAU int64 `json:"mau"`
}

type ModelDistributionRow struct {
	ProviderName  string `json:"provider_name"`
	RequestModel  string `json:"request_model"`
	TotalRequests int64  `json:"total_requests"`
}

type GenAIErrorRow struct {
	ErrorType    string `json:"error_type"`
	RequestModel string `json:"request_model"`
	Count        int64  `json:"count"`
}

// InsertMetrics stores both metric families of one OTLP export in a single
// transaction, so a mid-request failure can't persist half the payload: OTLP
// senders retry the whole request on a 5xx, and a partially-committed export
// would then be double-counted on the retry.
func (s *Store) InsertMetrics(ctx context.Context, genai []GenAIMetricRow, http []HTTPMetricRow) error {
	if len(genai) == 0 && len(http) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertGenAIMetrics(ctx, tx, genai); err != nil {
		return fmt.Errorf("genai metrics: %w", err)
	}
	if err := insertHTTPMetrics(ctx, tx, http); err != nil {
		return fmt.Errorf("http metrics: %w", err)
	}
	return tx.Commit()
}

func (s *Store) InsertGenAIMetrics(ctx context.Context, rows []GenAIMetricRow) error {
	return s.InsertMetrics(ctx, rows, nil)
}

func insertGenAIMetrics(ctx context.Context, tx *sql.Tx, rows []GenAIMetricRow) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO genai_metrics
		(received_at, time, service_name, app_id, metric_name, operation_name, provider_name,
		 request_model, token_type,
		 error_type, enduser_id, enduser_email, session_id,
		 count, sum, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		attrs, _ := json.Marshal(r.Attributes)
		_, err := stmt.ExecContext(ctx,
			r.ReceivedAt, r.Time, r.ServiceName, r.AppID, r.MetricName, r.OperationName,
			r.ProviderName, r.RequestModel, r.TokenType,
			r.ErrorType,
			r.EndUserID, r.EndUserEmail, r.SessionID,
			r.Count, r.Sum, string(attrs),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) QueryTokenSummary(ctx context.Context, from, to time.Time, f Filter) ([]TokenSummaryRow, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,
			COALESCE(token_type, '') as token_type,
			COALESCE(SUM(sum), 0) as total_tokens,
			COALESCE(SUM(count), 0) as total_requests
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY provider_name, request_model, token_type
		ORDER BY total_tokens DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TokenSummaryRow
	for rows.Next() {
		var r TokenSummaryRow
		if err := rows.Scan(&r.ProviderName, &r.RequestModel, &r.TokenType, &r.TotalTokens, &r.TotalRequests); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) QueryTokenTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(token_type, '') as label,
			COALESCE(SUM(sum), 0) as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, token_type
		ORDER BY bucket
	`, args...)
}

func (s *Store) QueryOperationSummary(ctx context.Context, from, to time.Time, f Filter) ([]OperationRow, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(operation_name, '') as operation_name,
			COALESCE(request_model, '') as request_model,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(SUM(count), 0) as total_count,
			CASE WHEN SUM(count) > 0 THEN SUM(sum) / SUM(count) ELSE 0 END as avg_duration
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.operation.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY operation_name, request_model, provider_name
		ORDER BY total_count DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []OperationRow
	for rows.Next() {
		var r OperationRow
		if err := rows.Scan(&r.OperationName, &r.RequestModel, &r.ProviderName, &r.TotalCount, &r.AvgDuration); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

type TopConsumerRow struct {
	ID            string  `json:"id"`             // Entra object id when resolved, else raw OTel id
	Name          string  `json:"name,omitempty"` // resolved display name; empty if unresolved
	Kind          string  `json:"kind,omitempty"` // user | application; empty if unresolved
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   float64 `json:"total_tokens"`
	TPM           float64 `json:"tpm"`
}

// QueryTopConsumers ranks end users by token volume in the range — the
// "who to throttle when load spikes" leaderboard. TPM is tokens per minute
// averaged over the range; requests count each operation once (input-token
// rows, since every operation emits one input and one output row).
func (s *Store) QueryTopConsumers(ctx context.Context, from, to time.Time, limit int, f Filter) ([]TopConsumerRow, error) {
	if limit <= 0 {
		limit = 10
	}
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	args = append(args, limit)
	r := dirResolve("genai_metrics", "enduser_id", "enduser_email")
	// Resolve and group by the canonical identity in SQL, so a user split across
	// several OTel ids merges into one row before the top-N cut.
	rows, err := s.db.QueryContext(ctx, `
		WITH resolved AS (
			SELECT `+r.ID+` as principal, `+r.Name+` as name, `+r.Kind+` as kind,
				CASE WHEN token_type = 'input' THEN count ELSE 0 END as reqs,
				sum as tokens
			FROM genai_metrics`+r.Join+`
			WHERE metric_name = 'gen_ai.client.token.usage'
			  AND enduser_id IS NOT NULL AND enduser_id != ''
			  AND time >= ? AND time <= ?`+clause+`
		)
		SELECT principal, name, kind,
			COALESCE(SUM(reqs), 0) as total_requests,
			COALESCE(SUM(tokens), 0) as total_tokens
		FROM resolved
		GROUP BY principal, name, kind
		ORDER BY total_tokens DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mins := to.Sub(from).Minutes()
	var result []TopConsumerRow
	for rows.Next() {
		var row TopConsumerRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind, &row.TotalRequests, &row.TotalTokens); err != nil {
			return nil, err
		}
		if mins > 0 {
			row.TPM = row.TotalTokens / mins
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) QueryActiveUsers(ctx context.Context, at time.Time, f Filter) (*ActiveUsersRow, error) {
	clause, fargs := f.genaiClause()
	dir := dirResolve("genai_metrics", "enduser_id", "enduser_email")
	// Single scan over the widest (30d/MAU) window; DAU and WAU are carved out of
	// the same scan with conditional COUNT(DISTINCT ...) rather than re-scanning
	// per window. Counting distinct resolved identities means a user appearing
	// under several OTel ids still counts once.
	q := `SELECT
			COUNT(DISTINCT CASE WHEN genai_metrics.time >= ? THEN ` + dir.ID + ` END) as dau,
			COUNT(DISTINCT CASE WHEN genai_metrics.time >= ? THEN ` + dir.ID + ` END) as wau,
			COUNT(DISTINCT ` + dir.ID + `) as mau
		  FROM genai_metrics` + dir.Join + `
		  WHERE enduser_id IS NOT NULL AND enduser_id != ''
		    AND genai_metrics.time >= ? AND genai_metrics.time <= ?` + clause
	// Placeholder order: DAU cutoff, WAU cutoff, MAU window lower bound, upper bound,
	// then the filter clause args.
	args := []any{
		at.Add(-24 * time.Hour),
		at.Add(-7 * 24 * time.Hour),
		at.Add(-30 * 24 * time.Hour),
		at,
	}
	args = append(args, fargs...)
	row := s.db.QueryRowContext(ctx, q, args...)

	r := &ActiveUsersRow{}
	if err := row.Scan(&r.DAU, &r.WAU, &r.MAU); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) QueryOperationDurationTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(request_model, '') as label,
			CASE WHEN SUM(count) > 0 THEN SUM(sum) / SUM(count) ELSE 0 END as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.operation.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, request_model
		ORDER BY bucket
	`, args...)
}

func (s *Store) QueryModelDistribution(ctx context.Context, from, to time.Time, f Filter) ([]ModelDistributionRow, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,
			COALESCE(SUM(count), 0) as total_requests
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND token_type = 'input'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY provider_name, request_model
		ORDER BY total_requests DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ModelDistributionRow
	for rows.Next() {
		var r ModelDistributionRow
		if err := rows.Scan(&r.ProviderName, &r.RequestModel, &r.TotalRequests); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// TokenPartitionsPoint is one bucket of the five disjoint token partitions
// (from spans). It is the single base series behind the token-composition,
// cache-hit-rate and reasoning-share panels — those views are pure arithmetic
// over these columns, derived client-side, so the dashboard runs this scan once
// instead of once per panel.
type TokenPartitionsPoint struct {
	Bucket     time.Time `json:"bucket"`
	Uncached   float64   `json:"uncached"`
	CacheRead  float64   `json:"cache_read"`
	CacheWrite float64   `json:"cache_write"`
	Response   float64   `json:"response"`
	Reasoning  float64   `json:"reasoning"`
}

// QueryTokenPartitions aggregates the five token partitions per bucket.
func (s *Store) QueryTokenPartitions(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TokenPartitionsPoint, error) {
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT time_bucket(CAST(? AS INTERVAL), time) as bucket,`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket
		ORDER BY bucket
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TokenPartitionsPoint
	for rows.Next() {
		var p TokenPartitionsPoint
		if err := rows.Scan(&p.Bucket, &p.Uncached, &p.CacheRead,
			&p.CacheWrite, &p.Response, &p.Reasoning); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) QueryGenAIErrors(ctx context.Context, from, to time.Time, f Filter) ([]GenAIErrorRow, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(error_type, '') as error_type,
			COALESCE(request_model, '') as request_model,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE error_type IS NOT NULL AND error_type != ''
		  AND metric_name = 'gen_ai.client.operation.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY error_type, request_model
		ORDER BY count DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []GenAIErrorRow
	for rows.Next() {
		var r GenAIErrorRow
		if err := rows.Scan(&r.ErrorType, &r.RequestModel, &r.Count); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
