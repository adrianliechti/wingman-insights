package store

import (
	"context"
	"time"
)

// QueryModelMixTimeseries returns token volume per bucket and model
// (input + output tokens), for stacked model-share charts.
func (s *Store) QueryModelMixTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(request_model, '') as label,
			COALESCE(SUM(sum), 0) as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND token_type IN ('input', 'output')
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, request_model
		ORDER BY bucket
	`, args...)
}

// QueryOperationMixTimeseries returns request counts per bucket and
// operation, for feature-adoption charts.
func (s *Store) QueryOperationMixTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(operation_name, '') as label,
			COALESCE(SUM(count), 0) as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND token_type = 'input'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, operation_name
		ORDER BY bucket
	`, args...)
}

// QueryTokensPerRequest returns the average input and output tokens per
// request over time — the "context growth" trend.
func (s *Store) QueryTokensPerRequest(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(token_type, '') as label,
			CASE WHEN SUM(count) > 0 THEN SUM(sum) / SUM(count) ELSE 0 END as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND token_type IN ('input', 'output')
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, token_type
		ORDER BY bucket
	`, args...)
}

// QuerySessionsTimeseries counts distinct sessions per bucket.
func (s *Store) QuerySessionsTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			'' as label,
			COUNT(DISTINCT session_id) as value,
			COUNT(DISTINCT session_id) as count
		FROM genai_metrics
		WHERE session_id IS NOT NULL AND session_id != ''
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket
		ORDER BY bucket
	`, args...)
}

// SessionStats summarizes session activity in a range.
type SessionStats struct {
	Sessions         int64   `json:"sessions"`
	AvgPerUser       float64 `json:"avg_per_user"`
	AvgTokensPerSess float64 `json:"avg_tokens_per_session"`
}

func (s *Store) QuerySessionStats(ctx context.Context, from, to time.Time, f Filter) (*SessionStats, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT session_id) as sessions,
			CASE WHEN COUNT(DISTINCT enduser_id) > 0
				THEN CAST(COUNT(DISTINCT session_id) AS DOUBLE) / COUNT(DISTINCT enduser_id)
				ELSE 0 END as avg_per_user,
			CASE WHEN COUNT(DISTINCT session_id) > 0
				THEN SUM(CASE WHEN metric_name = 'gen_ai.client.token.usage' THEN sum ELSE 0 END) / COUNT(DISTINCT session_id)
				ELSE 0 END as avg_tokens
		FROM genai_metrics
		WHERE session_id IS NOT NULL AND session_id != ''
		  AND time >= ? AND time <= ?`+clause, args...)
	st := &SessionStats{}
	if err := row.Scan(&st.Sessions, &st.AvgPerUser, &st.AvgTokensPerSess); err != nil {
		return nil, err
	}
	return st, nil
}

// QueryLatencyPercentiles computes p50/p95/p99 of GenAI span durations per
// bucket. Spans give exact per-request durations, unlike the pre-aggregated
// metric histograms.
func (s *Store) QueryLatencyPercentiles(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			quantile_cont(duration, 0.50) as p50,
			quantile_cont(duration, 0.95) as p95,
			quantile_cont(duration, 0.99) as p99
		FROM genai_spans
		WHERE request_model IS NOT NULL AND request_model != ''
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket
		ORDER BY bucket
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimeseriesPoint
	for rows.Next() {
		var bucket time.Time
		var p50, p95, p99 float64
		if err := rows.Scan(&bucket, &p50, &p95, &p99); err != nil {
			return nil, err
		}
		result = append(result,
			TimeseriesPoint{Bucket: bucket, Label: "p50", Value: p50},
			TimeseriesPoint{Bucket: bucket, Label: "p95", Value: p95},
			TimeseriesPoint{Bucket: bucket, Label: "p99", Value: p99},
		)
	}
	return result, rows.Err()
}

// QueryTTFCTimeseries returns the average time-to-first-chunk for streaming
// completions.
func (s *Store) QueryTTFCTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(request_model, '') as label,
			CASE WHEN SUM(count) > 0 THEN SUM(sum) / SUM(count) ELSE 0 END as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.operation.time_to_first_chunk'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, request_model
		ORDER BY bucket
	`, args...)
}

// QueryGenAIErrorRate returns the share of failed GenAI operations per bucket.
func (s *Store) QueryGenAIErrorRate(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			'' as label,
			CASE WHEN SUM(count) > 0
				THEN 100.0 * SUM(CASE WHEN error_type IS NOT NULL AND error_type != '' THEN count ELSE 0 END) / SUM(count)
				ELSE 0 END as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.operation.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket
		ORDER BY bucket
	`, args...)
}

// ToolStatRow summarizes execute_tool spans per tool.
type ToolStatRow struct {
	ToolName    string  `json:"tool_name"`
	Count       int64   `json:"count"`
	AvgDuration float64 `json:"avg_duration"`
	ErrorCount  int64   `json:"error_count"`
}

func (s *Store) QueryToolStats(ctx context.Context, from, to time.Time, f Filter) ([]ToolStatRow, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			tool_name,
			COUNT(*) as count,
			AVG(duration) as avg_duration,
			SUM(CASE WHEN status = 'error' OR (error_type IS NOT NULL AND error_type != '') THEN 1 ELSE 0 END) as error_count
		FROM genai_spans
		WHERE tool_name IS NOT NULL AND tool_name != ''
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY tool_name
		ORDER BY count DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ToolStatRow
	for rows.Next() {
		var r ToolStatRow
		if err := rows.Scan(&r.ToolName, &r.Count, &r.AvgDuration, &r.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// queryTimeseries runs a query whose SELECT matches TimeseriesPoint columns.
func (s *Store) queryTimeseries(ctx context.Context, query string, args ...any) ([]TimeseriesPoint, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimeseriesPoint
	for rows.Next() {
		var r TimeseriesPoint
		if err := rows.Scan(&r.Bucket, &r.Label, &r.Value, &r.Count); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
