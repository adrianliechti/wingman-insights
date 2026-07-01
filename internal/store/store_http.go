package store

import (
	"context"
	"encoding/json"
	"time"
)

type HTTPMetricRow struct {
	ReceivedAt  time.Time
	Time        time.Time
	ServiceName string
	MetricName  string
	Direction   string
	Method      string
	Route       string
	StatusCode  int
	ErrorType   string
	AppID       string
	UserID      string
	UserEmail   string
	Count       int64
	Sum         float64
	Attributes  map[string]string
}

type HTTPSummaryRow struct {
	Direction     string  `json:"direction"`
	Method        string  `json:"method"`
	Route         string  `json:"route"`
	TotalRequests int64   `json:"total_requests"`
	AvgDuration   float64 `json:"avg_duration"`
	ErrorCount    int64   `json:"error_count"`
}

type HTTPErrorsByCodeRow struct {
	StatusCode int   `json:"status_code"`
	Count      int64 `json:"count"`
}

func (s *Store) InsertHTTPMetrics(ctx context.Context, rows []HTTPMetricRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO http_metrics
		(received_at, time, service_name, metric_name, direction, method, route,
		 status_code, error_type,
		 app_id, user_id, user_email,
		 count, sum, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		attrs, _ := json.Marshal(r.Attributes)
		_, err := stmt.ExecContext(ctx,
			r.ReceivedAt, r.Time, r.ServiceName, r.MetricName, r.Direction,
			r.Method, r.Route, r.StatusCode, r.ErrorType, r.AppID, r.UserID, r.UserEmail,
			r.Count, r.Sum,
			string(attrs),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) QueryHTTPSummary(ctx context.Context, from, to time.Time, f Filter) ([]HTTPSummaryRow, error) {
	clause, fargs := f.httpClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(direction, '') as direction,
			COALESCE(method, '') as method,
			COALESCE(route, '') as route,
			COALESCE(SUM(count), 0) as total_requests,
			CASE WHEN SUM(count) > 0 THEN SUM(sum) / SUM(count) ELSE 0 END as avg_duration,
			COALESCE(SUM(CASE WHEN error_type != '' AND error_type IS NOT NULL THEN count ELSE 0 END), 0) as error_count
		FROM http_metrics
		WHERE metric_name LIKE 'http.%.request.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY direction, method, route
		ORDER BY total_requests DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []HTTPSummaryRow
	for rows.Next() {
		var r HTTPSummaryRow
		if err := rows.Scan(&r.Direction, &r.Method, &r.Route, &r.TotalRequests, &r.AvgDuration, &r.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) QueryHTTPTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.httpClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(direction, '') as label,
			CASE WHEN SUM(count) > 0 THEN SUM(sum) / SUM(count) ELSE 0 END as value,
			COALESCE(SUM(count), 0) as count
		FROM http_metrics
		WHERE metric_name LIKE 'http.%.request.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, direction
		ORDER BY bucket
	`, args...)
}

func (s *Store) QueryHTTPRequestsTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.httpClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(direction, '') as label,
			COALESCE(SUM(count), 0) as value,
			COALESCE(SUM(count), 0) as count
		FROM http_metrics
		WHERE metric_name LIKE 'http.%.request.duration'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, direction
		ORDER BY bucket
	`, args...)
}

func (s *Store) QueryHTTPErrorsByCode(ctx context.Context, from, to time.Time, f Filter) ([]HTTPErrorsByCodeRow, error) {
	clause, fargs := f.httpClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			status_code,
			COALESCE(SUM(count), 0) as count
		FROM http_metrics
		WHERE metric_name LIKE 'http.%.request.duration'
		  AND status_code >= 400
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY status_code
		ORDER BY count DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []HTTPErrorsByCodeRow
	for rows.Next() {
		var r HTTPErrorsByCodeRow
		if err := rows.Scan(&r.StatusCode, &r.Count); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
