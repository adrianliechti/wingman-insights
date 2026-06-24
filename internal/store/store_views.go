package store

import (
	"context"
	"sort"
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
	r := dirResolve("genai_metrics", "enduser_id", "enduser_email")
	// avg_per_user divides by distinct resolved identities, so a user under
	// several OTel ids isn't counted as several users.
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT session_id) as sessions,
			CASE WHEN COUNT(DISTINCT `+r.ID+`) > 0
				THEN CAST(COUNT(DISTINCT session_id) AS DOUBLE) / COUNT(DISTINCT `+r.ID+`)
				ELSE 0 END as avg_per_user,
			CASE WHEN COUNT(DISTINCT session_id) > 0
				THEN SUM(CASE WHEN metric_name = 'gen_ai.client.token.usage' THEN sum ELSE 0 END) / COUNT(DISTINCT session_id)
				ELSE 0 END as avg_tokens
		FROM genai_metrics`+r.Join+`
		WHERE session_id IS NOT NULL AND session_id != ''
		  AND genai_metrics.time >= ? AND genai_metrics.time <= ?`+clause, args...)
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

// QueryInteractions counts LLM inference operations (spans carrying token usage)
// in the range — the "interactions" product KPI.
func (s *Store) QueryInteractions(ctx context.Context, from, to time.Time, f Filter) (int64, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	var n int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0)
		  AND time >= ? AND time <= ?`+clause, args...).Scan(&n)
	return n, err
}

// QueryThroughputTimeseries is generation throughput — output tokens per second
// of model time — per bucket, from span durations.
func (s *Store) QueryThroughputTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			'' as label,
			CASE WHEN SUM(duration) > 0 THEN SUM(output_tokens) / SUM(duration) ELSE 0 END as value,
			COUNT(*) as count
		FROM genai_spans
		WHERE output_tokens > 0 AND duration > 0 AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket
		ORDER BY bucket
	`, args...)
}

// User engagement segments, classified by request rate (requests per day over
// the range). Range-normalized so the same thresholds work for 24h and 30d.
const (
	segPower    = "power"
	segFrequent = "frequent"
	segRegular  = "regular"
	segCasual   = "casual"
)

// segmentOrder lists segments most- to least-engaged for stable display.
var segmentOrder = []string{segPower, segFrequent, segRegular, segCasual}

func classifySegment(requests int64, rangeDays float64) string {
	if rangeDays < 1 {
		rangeDays = 1
	}
	switch perDay := float64(requests) / rangeDays; {
	case perDay >= 15:
		return segPower
	case perDay >= 5:
		return segFrequent
	case perDay >= 1:
		return segRegular
	default:
		return segCasual
	}
}

func rangeDays(from, to time.Time) float64 {
	if d := to.Sub(from).Hours() / 24; d >= 1 {
		return d
	}
	return 1
}

// UserStatRow is one end user's activity in the range: the row behind the
// unified per-user table and the input to segmentation.
type UserStatRow struct {
	ID         string  `json:"id"`             // Entra object id when resolved, else raw OTel id
	Name       string  `json:"name,omitempty"` // resolved display name; empty if unresolved
	Kind       string  `json:"kind,omitempty"` // user | application; empty if unresolved
	Requests   int64   `json:"requests"`
	Tokens     float64 `json:"tokens"`
	Cost       float64 `json:"cost"`
	ActiveDays int64   `json:"active_days"`
	TopModel   string  `json:"top_model"`
	Segment    string  `json:"segment"`
}

// QueryUserStats aggregates per-user requests, tokens, cost, active days and
// most-used model from spans (the only source carrying materialized cost), and
// tags each user with an engagement segment. limit <= 0 returns every user.
func (s *Store) QueryUserStats(ctx context.Context, from, to time.Time, limit int, f Filter) ([]UserStatRow, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	r := dirResolve("genai_spans", "user_id", "user_email")
	// Resolve then group by the canonical identity in SQL. Folding the aliases
	// before aggregation makes active_days (distinct days) and top_model (mode)
	// exact for the merged user — the Go fold could only approximate them.
	q := `
		WITH resolved AS (
			SELECT ` + r.ID + ` as principal, ` + r.Name + ` as name, ` + r.Kind + ` as kind,
				genai_spans.time as ts, request_model, input_tokens, output_tokens, cost
			FROM genai_spans` + r.Join + `
			WHERE (input_tokens > 0 OR output_tokens > 0)
			  AND user_id IS NOT NULL AND user_id != ''
			  AND genai_spans.time >= ? AND genai_spans.time <= ?` + clause + `
		)
		SELECT principal, name, kind,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens), 0) as tokens,
			COALESCE(SUM(cost), 0) as cost,
			COUNT(DISTINCT CAST(ts AS DATE)) as active_days,
			COALESCE(mode(request_model), '') as top_model
		FROM resolved
		GROUP BY principal, name, kind
		ORDER BY cost DESC, tokens DESC`
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	days := rangeDays(from, to)
	var result []UserStatRow
	for rows.Next() {
		var row UserStatRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind, &row.Requests, &row.Tokens,
			&row.Cost, &row.ActiveDays, &row.TopModel); err != nil {
			return nil, err
		}
		row.Segment = classifySegment(row.Requests, days)
		result = append(result, row)
	}
	return result, rows.Err()
}

// UserSegmentRow is one engagement segment with its population and share of
// spend / consumption.
type UserSegmentRow struct {
	Segment  string  `json:"segment"`
	Users    int64   `json:"users"`
	Requests int64   `json:"requests"`
	Tokens   float64 `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// QueryUserSegments rolls the per-user stats up into the four engagement
// segments, in most- to least-engaged order.
func (s *Store) QueryUserSegments(ctx context.Context, from, to time.Time, f Filter) ([]UserSegmentRow, error) {
	stats, err := s.QueryUserStats(ctx, from, to, 0, f)
	if err != nil {
		return nil, err
	}
	bySeg := map[string]*UserSegmentRow{}
	for _, seg := range segmentOrder {
		bySeg[seg] = &UserSegmentRow{Segment: seg}
	}
	for _, u := range stats {
		r := bySeg[u.Segment]
		r.Users++
		r.Requests += u.Requests
		r.Tokens += u.Tokens
		r.Cost += u.Cost
	}
	result := make([]UserSegmentRow, 0, len(segmentOrder))
	for _, seg := range segmentOrder {
		result = append(result, *bySeg[seg])
	}
	return result, nil
}

// CohortCell is one (signup-week cohort, weeks-since-signup) bucket of the
// retention matrix. Active at week_offset 0 is the cohort size.
type CohortCell struct {
	Cohort     time.Time `json:"cohort"`
	WeekOffset int64     `json:"week_offset"`
	Active     int64     `json:"active"`
}

// QueryCohortRetention builds a weekly signup-cohort retention matrix. Cohorts
// are keyed by each user's first-ever activity week (full history), so it uses a
// fixed `weeks` lookback ending at `to` rather than the dashboard range, which is
// usually too short for cohorts.
func (s *Store) QueryCohortRetention(ctx context.Context, to time.Time, weeks int, f Filter) ([]CohortCell, error) {
	if weeks <= 0 || weeks > 53 {
		weeks = 12
	}
	bound := to.AddDate(0, 0, -7*weeks)
	clause, fargs := f.spansClause()
	args := append([]any{to}, fargs...)
	args = append(args, to)
	args = append(args, fargs...)
	args = append(args, bound)
	r := dirResolve("genai_spans", "user_id", "user_email")
	// Cohorts key on the resolved identity, so a user under several OTel ids has
	// one first-seen week and is counted once per cohort/offset cell.
	rows, err := s.db.QueryContext(ctx, `
		WITH first_seen AS (
			SELECT `+r.ID+` as principal, MIN(genai_spans.time) AS first_ts
			FROM genai_spans`+r.Join+`
			WHERE user_id IS NOT NULL AND user_id != '' AND genai_spans.time <= ?`+clause+`
			GROUP BY principal
		),
		activity AS (
			SELECT DISTINCT `+r.ID+` as principal, date_trunc('week', genai_spans.time) AS wk
			FROM genai_spans`+r.Join+`
			WHERE user_id IS NOT NULL AND user_id != '' AND genai_spans.time <= ?`+clause+`
		)
		SELECT
			date_trunc('week', fs.first_ts) AS cohort,
			CAST(date_diff('week', date_trunc('week', fs.first_ts), a.wk) AS BIGINT) AS week_offset,
			COUNT(DISTINCT a.principal) AS active
		FROM first_seen fs JOIN activity a ON fs.principal = a.principal
		WHERE date_trunc('week', fs.first_ts) >= date_trunc('week', CAST(? AS TIMESTAMP))
		GROUP BY cohort, week_offset
		ORDER BY cohort, week_offset
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CohortCell
	for rows.Next() {
		var c CohortCell
		if err := rows.Scan(&c.Cohort, &c.WeekOffset, &c.Active); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// AppAdoptionRow is one application's (service_name's) footprint in the range:
// distinct users, requests, tokens and spend.
type AppAdoptionRow struct {
	ServiceName string  `json:"service_name"`
	Users       int64   `json:"users"`
	Requests    int64   `json:"requests"`
	Tokens      float64 `json:"tokens"`
	Cost        float64 `json:"cost"`
}

// QueryAppAdoption ranks applications by spend, with their distinct-user reach —
// the per-application lens (service_name is the app dimension).
func (s *Store) QueryAppAdoption(ctx context.Context, from, to time.Time, f Filter) ([]AppAdoptionRow, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	r := dirResolve("genai_spans", "user_id", "user_email")
	// Distinct-user reach counts resolved identities, so a user under several
	// OTel ids isn't over-counted per app.
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(service_name, '') as service_name,
			COUNT(DISTINCT `+r.ID+`) as users,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens), 0) as tokens,
			COALESCE(SUM(cost), 0) as cost
		FROM genai_spans`+r.Join+`
		WHERE (input_tokens > 0 OR output_tokens > 0)
		  AND genai_spans.time >= ? AND genai_spans.time <= ?`+clause+`
		GROUP BY service_name
		ORDER BY cost DESC, tokens DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AppAdoptionRow
	for rows.Next() {
		var r AppAdoptionRow
		if err := rows.Scan(&r.ServiceName, &r.Users, &r.Requests, &r.Tokens, &r.Cost); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ModelPreferenceRow is the token volume one engagement segment sends to one
// model — for the "do power users prefer premium models?" chart.
type ModelPreferenceRow struct {
	Segment string  `json:"segment"`
	Model   string  `json:"model"`
	Tokens  float64 `json:"tokens"`
}

// QueryModelPreferenceBySegment cross-tabs token volume by engagement segment and
// model. Segments come from QueryUserStats; per-(user, model) volume is joined to
// them in Go.
func (s *Store) QueryModelPreferenceBySegment(ctx context.Context, from, to time.Time, f Filter) ([]ModelPreferenceRow, error) {
	stats, err := s.QueryUserStats(ctx, from, to, 0, f)
	if err != nil {
		return nil, err
	}
	segOf := make(map[string]string, len(stats))
	for _, u := range stats {
		segOf[u.ID] = u.Segment
	}

	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	r := dirResolve("genai_spans", "user_id", "user_email")
	// Group by the canonical identity in SQL so it lines up with QueryUserStats's
	// resolved ids (segOf is keyed on those).
	rows, err := s.db.QueryContext(ctx, `
		WITH resolved AS (
			SELECT `+r.ID+` as principal, COALESCE(request_model, '') as request_model,
				input_tokens, output_tokens
			FROM genai_spans`+r.Join+`
			WHERE (input_tokens > 0 OR output_tokens > 0)
			  AND request_model IS NOT NULL AND request_model != ''
			  AND user_id IS NOT NULL AND user_id != ''
			  AND genai_spans.time >= ? AND genai_spans.time <= ?`+clause+`
		)
		SELECT principal, request_model, COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM resolved
		GROUP BY principal, request_model
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct{ seg, model string }
	agg := map[key]float64{}
	for rows.Next() {
		var id, model string
		var tokens float64
		if err := rows.Scan(&id, &model, &tokens); err != nil {
			return nil, err
		}
		seg, ok := segOf[id]
		if !ok {
			continue
		}
		agg[key{seg, model}] += tokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]ModelPreferenceRow, 0, len(agg))
	for k, v := range agg {
		result = append(result, ModelPreferenceRow{Segment: k.seg, Model: k.model, Tokens: v})
	}
	// Stable order: segment (most-engaged first), then volume desc.
	rank := map[string]int{segPower: 0, segFrequent: 1, segRegular: 2, segCasual: 3}
	sort.Slice(result, func(i, j int) bool {
		if rank[result[i].Segment] != rank[result[j].Segment] {
			return rank[result[i].Segment] < rank[result[j].Segment]
		}
		return result[i].Tokens > result[j].Tokens
	})
	return result, nil
}

// BurstRow is one user's request-burst profile: the busiest one-minute window,
// total requests and how many distinct active minutes — the in-dashboard
// runaway/peak signal (an agent stuck hammering the gateway shows a high peak).
type BurstRow struct {
	ID            string `json:"id"`             // Entra object id when resolved, else raw OTel id
	Name          string `json:"name,omitempty"` // resolved display name; empty if unresolved
	Kind          string `json:"kind,omitempty"` // user | application; empty if unresolved
	PeakRPM       int64  `json:"peak_rpm"`
	TotalRequests int64  `json:"total_requests"`
	ActiveMinutes int64  `json:"active_minutes"`
}

// QueryUserBurst ranks users by their peak requests-per-minute in the range.
func (s *Store) QueryUserBurst(ctx context.Context, from, to time.Time, limit int, f Filter) ([]BurstRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 15
	}
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	args = append(args, limit)
	r := dirResolve("genai_spans", "user_id", "user_email")
	// Resolve to the canonical identity per minute first, so peak_rpm and active
	// minutes are exact for a user spread across several OTel ids (the Go fold
	// could only take the worst single-alias peak).
	rows, err := s.db.QueryContext(ctx, `
		WITH per_min AS (
			SELECT `+r.ID+` as principal, `+r.Name+` as name, `+r.Kind+` as kind,
				time_bucket(CAST('1 minute' AS INTERVAL), genai_spans.time) as minute,
				COUNT(*) as reqs
			FROM genai_spans`+r.Join+`
			WHERE user_id IS NOT NULL AND user_id != ''
			  AND genai_spans.time >= ? AND genai_spans.time <= ?`+clause+`
			GROUP BY principal, name, kind, minute
		)
		SELECT principal, name, kind,
			MAX(reqs) as peak_rpm,
			SUM(reqs) as total_reqs,
			COUNT(*) as active_minutes
		FROM per_min
		GROUP BY principal, name, kind
		ORDER BY peak_rpm DESC, total_reqs DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BurstRow
	for rows.Next() {
		var row BurstRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind, &row.PeakRPM, &row.TotalRequests, &row.ActiveMinutes); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// QueryNewVsReturningTimeseries splits active users per bucket into those seen
// for the first time ever in that bucket (new) and those active before (returning).
func (s *Store) QueryNewVsReturningTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.spansClause()
	var args []any
	args = append(args, fargs...)           // first_seen clause
	args = append(args, interval, from, to) // activity bucket + range
	args = append(args, fargs...)           // activity clause
	args = append(args, interval)           // new/returning split
	r := dirResolve("genai_spans", "user_id", "user_email")
	// New vs returning keys on the resolved identity: first-seen and per-bucket
	// activity fold every OTel id of a user into one principal.
	return s.queryTimeseries(ctx, `
		WITH first_seen AS (
			SELECT `+r.ID+` as principal, MIN(genai_spans.time) AS first_ts
			FROM genai_spans`+r.Join+`
			WHERE user_id IS NOT NULL AND user_id != ''`+clause+`
			GROUP BY principal
		),
		activity AS (
			SELECT DISTINCT `+r.ID+` as principal, time_bucket(CAST(? AS INTERVAL), genai_spans.time) as bucket
			FROM genai_spans`+r.Join+`
			WHERE user_id IS NOT NULL AND user_id != ''
			  AND genai_spans.time >= ? AND genai_spans.time <= ?`+clause+`
		)
		SELECT
			a.bucket,
			CASE WHEN time_bucket(CAST(? AS INTERVAL), fs.first_ts) >= a.bucket THEN 'new' ELSE 'returning' END as label,
			COUNT(DISTINCT a.principal) as value,
			COUNT(DISTINCT a.principal) as count
		FROM activity a JOIN first_seen fs ON a.principal = fs.principal
		GROUP BY a.bucket, label
		ORDER BY a.bucket
	`, args...)
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
