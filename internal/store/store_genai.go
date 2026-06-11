package store

import (
	"context"
	"encoding/json"
	"time"
)

type GenAIMetricRow struct {
	ReceivedAt    time.Time
	Time          time.Time
	ServiceName   string
	MetricName    string
	OperationName string
	ProviderName  string
	RequestModel  string
	ResponseModel string
	TokenType     string
	ServerAddress string
	ErrorType     string
	EndUserID     string
	EndUserEmail  string
	Count         int64
	Sum           float64
	MinVal        float64
	MaxVal        float64
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

type UserTokenSummaryRow struct {
	EndUserID     string  `json:"enduser_id"`
	EndUserEmail  string  `json:"enduser_email"`
	RequestModel  string  `json:"request_model"`
	TokenType     string  `json:"token_type"`
	TotalTokens   float64 `json:"total_tokens"`
	TotalRequests int64   `json:"total_requests"`
}

type ActiveUsersRow struct {
	DAU int64 `json:"dau"`
	WAU int64 `json:"wau"`
	MAU int64 `json:"mau"`
}

type ModelDistributionRow struct {
	ProviderName string `json:"provider_name"`
	RequestModel string `json:"request_model"`
	TotalRequests int64  `json:"total_requests"`
}

type GenAIErrorRow struct {
	ErrorType    string `json:"error_type"`
	RequestModel string `json:"request_model"`
	Count        int64  `json:"count"`
}

func (s *Store) InsertGenAIMetrics(ctx context.Context, rows []GenAIMetricRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO genai_metrics
		(received_at, time, service_name, metric_name, operation_name, provider_name,
		 request_model, response_model, token_type, server_address, error_type,
		 enduser_id, enduser_email, count, sum, min_val, max_val, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		attrs, _ := json.Marshal(r.Attributes)
		_, err := stmt.ExecContext(ctx,
			r.ReceivedAt, r.Time, r.ServiceName, r.MetricName, r.OperationName,
			r.ProviderName, r.RequestModel, r.ResponseModel, r.TokenType,
			r.ServerAddress, r.ErrorType, r.EndUserID, r.EndUserEmail,
			r.Count, r.Sum, r.MinVal, r.MaxVal, string(attrs),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
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
	query := `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(token_type, '') as label,
			COALESCE(SUM(sum), 0) as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND time >= ? AND time <= ?` + clause + `
		GROUP BY bucket, token_type
		ORDER BY bucket
	`
	args := append([]any{interval, from, to}, fargs...)

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

func (s *Store) QueryUserTokenSummary(ctx context.Context, from, to time.Time, f Filter) ([]UserTokenSummaryRow, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(enduser_id, '') as enduser_id,
			COALESCE(enduser_email, '') as enduser_email,
			COALESCE(request_model, '') as request_model,
			COALESCE(token_type, '') as token_type,
			COALESCE(SUM(sum), 0) as total_tokens,
			COALESCE(SUM(count), 0) as total_requests
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND time >= ? AND time <= ?
		  AND enduser_id IS NOT NULL AND enduser_id != ''`+clause+`
		GROUP BY enduser_id, enduser_email, request_model, token_type
		ORDER BY total_tokens DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []UserTokenSummaryRow
	for rows.Next() {
		var r UserTokenSummaryRow
		if err := rows.Scan(&r.EndUserID, &r.EndUserEmail, &r.RequestModel, &r.TokenType, &r.TotalTokens, &r.TotalRequests); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) QueryActiveUsers(ctx context.Context, at time.Time, f Filter) (*ActiveUsersRow, error) {
	clause, fargs := f.genaiClause()
	sub := `(SELECT COUNT(DISTINCT enduser_id) FROM genai_metrics
			 WHERE enduser_id IS NOT NULL AND enduser_id != ''
			   AND time >= ? AND time <= ?` + clause + `)`
	var args []any
	for _, window := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour} {
		args = append(args, at.Add(-window), at)
		args = append(args, fargs...)
	}
	row := s.db.QueryRowContext(ctx,
		"SELECT "+sub+" as dau, "+sub+" as wau, "+sub+" as mau", args...)

	r := &ActiveUsersRow{}
	if err := row.Scan(&r.DAU, &r.WAU, &r.MAU); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) QueryActiveUsersTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			'' as label,
			COUNT(DISTINCT enduser_id) as value,
			COUNT(DISTINCT enduser_id) as count
		FROM genai_metrics
		WHERE enduser_id IS NOT NULL AND enduser_id != ''
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
		var r TimeseriesPoint
		if err := rows.Scan(&r.Bucket, &r.Label, &r.Value, &r.Count); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) QueryOperationDurationTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
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

func (s *Store) QueryCacheEfficiency(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			'' as label,
			CASE WHEN SUM(sum) > 0
				THEN 100.0 * SUM(CASE WHEN token_type = 'cache_read' THEN sum ELSE 0 END) / SUM(sum)
				ELSE 0 END as value,
			COALESCE(SUM(count), 0) as count
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
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
		var r TimeseriesPoint
		if err := rows.Scan(&r.Bucket, &r.Label, &r.Value, &r.Count); err != nil {
			return nil, err
		}
		result = append(result, r)
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
