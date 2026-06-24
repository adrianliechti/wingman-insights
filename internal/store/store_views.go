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
	// With a directory, fold raw ids into one identity before taking the top N.
	// Without one, nothing merges, so let SQL rank and limit.
	q := `
		SELECT
			COALESCE(user_id, '') as enduser_id,
			COALESCE(MAX(user_email), '') as enduser_email,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens), 0) as tokens,
			COALESCE(SUM(cost), 0) as cost,
			COUNT(DISTINCT CAST(time AS DATE)) as active_days,
			COALESCE(mode(request_model), '') as top_model
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0)
		  AND user_id IS NOT NULL AND user_id != ''
		  AND time >= ? AND time <= ?` + clause + `
		GROUP BY user_id`
	if limit > 0 && !s.resolving() {
		q += " ORDER BY cost DESC, tokens DESC LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	days := rangeDays(from, to)
	fold := newIDFolder[UserStatRow]()
	topTokens := make(map[string]float64) // tokens of the alias contributing TopModel
	for rows.Next() {
		var rawID, email, topModel string
		var requests, activeDays int64
		var tokens, cost float64
		if err := rows.Scan(&rawID, &email, &requests, &tokens, &cost, &activeDays, &topModel); err != nil {
			return nil, err
		}
		id, name, kind := s.resolveUser(rawID, email)
		agg := fold.at(id, func() *UserStatRow { return &UserStatRow{ID: id, Name: name, Kind: kind} })
		agg.Requests += requests
		agg.Tokens += tokens
		agg.Cost += cost
		// active_days and top_model can't be merged exactly post-aggregation:
		// take the max distinct-day count and the heaviest alias's top model.
		if activeDays > agg.ActiveDays {
			agg.ActiveDays = activeDays
		}
		if tokens >= topTokens[id] {
			topTokens[id] = tokens
			agg.TopModel = topModel
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := fold.rows()
	for i := range result {
		result[i].Segment = classifySegment(result[i].Requests, days)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Cost != result[j].Cost {
			return result[i].Cost > result[j].Cost
		}
		return result[i].Tokens > result[j].Tokens
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
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
	rows, err := s.db.QueryContext(ctx, `
		WITH first_seen AS (
			SELECT user_id, MIN(time) AS first_ts
			FROM genai_spans
			WHERE user_id IS NOT NULL AND user_id != '' AND time <= ?`+clause+`
			GROUP BY user_id
		),
		activity AS (
			SELECT DISTINCT user_id, date_trunc('week', time) AS wk
			FROM genai_spans
			WHERE user_id IS NOT NULL AND user_id != '' AND time <= ?`+clause+`
		)
		SELECT
			date_trunc('week', fs.first_ts) AS cohort,
			CAST(date_diff('week', date_trunc('week', fs.first_ts), a.wk) AS BIGINT) AS week_offset,
			COUNT(DISTINCT a.user_id) AS active
		FROM first_seen fs JOIN activity a ON fs.user_id = a.user_id
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(service_name, '') as service_name,
			COUNT(DISTINCT user_id) as users,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens), 0) as tokens,
			COALESCE(SUM(cost), 0) as cost
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0)
		  AND time >= ? AND time <= ?`+clause+`
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(user_id, ''), COALESCE(MAX(user_email), ''), COALESCE(request_model, ''),
			COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0)
		  AND request_model IS NOT NULL AND request_model != ''
		  AND user_id IS NOT NULL AND user_id != ''
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY user_id, request_model
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct{ seg, model string }
	agg := map[key]float64{}
	for rows.Next() {
		var user, email, model string
		var tokens float64
		if err := rows.Scan(&user, &email, &model, &tokens); err != nil {
			return nil, err
		}
		// Resolve the raw id to the same canonical id QueryUserStats keyed on
		// (id-then-email, matching resolveUser).
		id, _, _ := s.resolveUser(user, email)
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
	// With a directory, fold raw ids into one identity before taking the top N.
	// Without one, nothing merges, so let SQL rank and limit.
	q := `
		WITH per_min AS (
			SELECT user_id,
				COALESCE(MAX(user_email), '') as email,
				time_bucket(CAST('1 minute' AS INTERVAL), time) as minute,
				COUNT(*) as reqs
			FROM genai_spans
			WHERE user_id IS NOT NULL AND user_id != ''
			  AND time >= ? AND time <= ?` + clause + `
			GROUP BY user_id, minute
		)
		SELECT user_id, MAX(email) as email,
			MAX(reqs) as peak_rpm,
			SUM(reqs) as total_reqs,
			COUNT(*) as active_minutes
		FROM per_min
		GROUP BY user_id`
	if !s.resolving() {
		q += " ORDER BY peak_rpm DESC, total_reqs DESC LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fold := newIDFolder[BurstRow]()
	for rows.Next() {
		var rawID, email string
		var peak, total, minutes int64
		if err := rows.Scan(&rawID, &email, &peak, &total, &minutes); err != nil {
			return nil, err
		}
		id, name, kind := s.resolveUser(rawID, email)
		agg := fold.at(id, func() *BurstRow { return &BurstRow{ID: id, Name: name, Kind: kind} })
		// peak_rpm and active_minutes can't be merged exactly across aliases
		// post-aggregation: take the worst peak and the max active-minute count.
		if peak > agg.PeakRPM {
			agg.PeakRPM = peak
		}
		if minutes > agg.ActiveMinutes {
			agg.ActiveMinutes = minutes
		}
		agg.TotalRequests += total
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := fold.rows()
	sort.Slice(result, func(i, j int) bool {
		if result[i].PeakRPM != result[j].PeakRPM {
			return result[i].PeakRPM > result[j].PeakRPM
		}
		return result[i].TotalRequests > result[j].TotalRequests
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
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
	return s.queryTimeseries(ctx, `
		WITH first_seen AS (
			SELECT user_id, MIN(time) AS first_ts
			FROM genai_spans
			WHERE user_id IS NOT NULL AND user_id != ''`+clause+`
			GROUP BY user_id
		),
		activity AS (
			SELECT DISTINCT user_id, time_bucket(CAST(? AS INTERVAL), time) as bucket
			FROM genai_spans
			WHERE user_id IS NOT NULL AND user_id != ''
			  AND time >= ? AND time <= ?`+clause+`
		)
		SELECT
			a.bucket,
			CASE WHEN time_bucket(CAST(? AS INTERVAL), fs.first_ts) >= a.bucket THEN 'new' ELSE 'returning' END as label,
			COUNT(DISTINCT a.user_id) as value,
			COUNT(DISTINCT a.user_id) as count
		FROM activity a JOIN first_seen fs ON a.user_id = fs.user_id
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
