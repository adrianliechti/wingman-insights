package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"insights/internal/pricing"

	"github.com/duckdb/duckdb-go/v2"
)

type SpanRow struct {
	ReceivedAt    time.Time `json:"-"`
	Time          time.Time `json:"time"`
	Duration      float64   `json:"duration"`
	TraceID       string    `json:"trace_id"`
	SpanID        string    `json:"span_id"`
	ParentSpanID  string    `json:"parent_span_id,omitempty"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind,omitempty"`
	Status        string    `json:"status,omitempty"`
	ServiceName   string    `json:"service_name,omitempty"`
	OperationName string    `json:"operation_name,omitempty"`
	ProviderName  string    `json:"provider_name,omitempty"`
	RequestModel  string    `json:"request_model,omitempty"`
	ResponseModel string    `json:"response_model,omitempty"`
	AgentName     string    `json:"agent_name,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	UserEmail     string    `json:"user_email,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	ErrorType     string    `json:"error_type,omitempty"`
	FinishReasons string    `json:"finish_reasons,omitempty"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	CacheRead     int64     `json:"cache_read_tokens"`
	CacheCreation int64     `json:"cache_creation_tokens"`
	Reasoning     int64     `json:"reasoning_tokens"`

	// Cost is derived from token counts and models.dev pricing at query time.
	Cost float64 `json:"cost"`
	// Attrs carries the raw span attributes; populated by QueryTrace only.
	Attrs map[string]string `json:"attributes,omitempty"`

	Attributes map[string]string `json:"-"`
}

// price fills Cost from the span's token counts. InputTokens is the inclusive
// prompt total (includes cache), so pricing.Cost bills only the non-cached
// remainder at the input rate; reasoning is billed within output.
func (r *SpanRow) price() {
	p, ok := pricing.Lookup(r.ProviderName, r.RequestModel)
	if !ok {
		return
	}
	r.Cost = p.Cost(float64(r.InputTokens), float64(r.OutputTokens),
		float64(r.CacheRead), float64(r.CacheCreation))
}

func (s *Store) InsertSpans(ctx context.Context, rows []SpanRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO genai_spans
		(received_at, time, duration, trace_id, span_id, parent_span_id, name, kind,
		 status, service_name, operation_name, provider_name, request_model,
		 response_model, agent_name, tool_name, user_id, user_email, session_id,
		 error_type, finish_reasons, input_tokens, output_tokens, cache_read_tokens,
		 cache_creation_tokens, reasoning_tokens, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		attrs, _ := json.Marshal(r.Attributes)
		_, err := stmt.ExecContext(ctx,
			r.ReceivedAt, r.Time, r.Duration, r.TraceID, r.SpanID, r.ParentSpanID,
			r.Name, r.Kind, r.Status, r.ServiceName, r.OperationName, r.ProviderName,
			r.RequestModel, r.ResponseModel, r.AgentName, r.ToolName, r.UserID,
			r.UserEmail, r.SessionID, r.ErrorType, r.FinishReasons, r.InputTokens,
			r.OutputTokens, r.CacheRead, r.CacheCreation, r.Reasoning, string(attrs),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sortTraceSummaries(rows []TraceSummary) {
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Time.After(rows[j].Time)
	})
}

const spanColumns = `time, duration, trace_id, span_id, COALESCE(parent_span_id, ''),
	COALESCE(name, ''), COALESCE(kind, ''), COALESCE(status, ''),
	COALESCE(service_name, ''), COALESCE(operation_name, ''), COALESCE(provider_name, ''),
	COALESCE(request_model, ''), COALESCE(response_model, ''), COALESCE(agent_name, ''),
	COALESCE(tool_name, ''), COALESCE(user_id, ''), COALESCE(user_email, ''),
	COALESCE(session_id, ''), COALESCE(error_type, ''), COALESCE(finish_reasons, ''),
	COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cache_read_tokens, 0),
	COALESCE(cache_creation_tokens, 0), COALESCE(reasoning_tokens, 0)`

// TraceSummary is one trace condensed to a list entry.
type TraceSummary struct {
	TraceID      string    `json:"trace_id"`
	Name         string    `json:"name"`
	Time         time.Time `json:"time"`
	Duration     float64   `json:"duration"`
	ServiceName  string    `json:"service_name,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	UserEmail    string    `json:"user_email,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	SpanCount    int64     `json:"span_count"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	Cost         float64   `json:"cost"`
	HasError     bool      `json:"has_error"`
}

// QueryTraceList returns recent traces, newest first. Rows are aggregated per
// (trace, provider, model) in SQL so cost can be priced per model in Go, then
// merged per trace.
func (s *Store) QueryTraceList(ctx context.Context, from, to time.Time, f Filter, onlyErrors bool, limit int) ([]TraceSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	clause, fargs := f.spansClause()
	// Pick the newest trace ids first so aggregation and pricing only touch
	// `limit` traces instead of every trace in the window.
	recent := `SELECT trace_id FROM genai_spans
		WHERE time >= ? AND time <= ?` + clause
	if onlyErrors {
		recent += ` AND (status = 'error' OR (error_type IS NOT NULL AND error_type != ''))`
	}
	recent += ` GROUP BY trace_id ORDER BY MAX(time) DESC LIMIT ?`

	query := `
		SELECT
			trace_id,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,
			COALESCE(MAX(CASE WHEN parent_span_id = '' OR parent_span_id IS NULL THEN name END), arg_min(name, time)) as name,
			MAX(CASE WHEN parent_span_id = '' OR parent_span_id IS NULL THEN 1 ELSE 0 END) as has_root,
			MIN(time) as start_time,
			MIN(epoch(time)) as start_epoch,
			MAX(epoch(time) + duration) as end_epoch,
			COALESCE(NULLIF(MAX(service_name), ''), '') as service_name,
			COALESCE(NULLIF(MAX(user_id), ''), '') as user_id,
			COALESCE(NULLIF(MAX(user_email), ''), '') as user_email,
			COALESCE(NULLIF(MAX(session_id), ''), '') as session_id,
			COUNT(*) as span_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read,
			COALESCE(SUM(cache_creation_tokens), 0) as cache_creation,
			BOOL_OR(status = 'error' OR (error_type IS NOT NULL AND error_type != '')) as has_error
		FROM genai_spans
		WHERE time >= ? AND time <= ?` + clause + `
		  AND trace_id IN (` + recent + `)
		GROUP BY trace_id, provider_name, request_model`
	args := append([]any{from, to}, fargs...)
	args = append(args, from, to)
	args = append(args, fargs...)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		summary    TraceSummary
		startEpoch float64
		endEpoch   float64
		rootName   string
		hasRoot    bool
	}
	traces := make(map[string]*acc)
	for rows.Next() {
		var t TraceSummary
		var provider, model, name string
		var hasRoot int
		var startEpoch, endEpoch float64
		var cacheRead, cacheCreation int64
		if err := rows.Scan(&t.TraceID, &provider, &model, &name, &hasRoot, &t.Time, &startEpoch, &endEpoch,
			&t.ServiceName, &t.UserID, &t.UserEmail, &t.SessionID, &t.SpanCount,
			&t.InputTokens, &t.OutputTokens, &cacheRead, &cacheCreation, &t.HasError); err != nil {
			return nil, err
		}
		var cost float64
		if price, ok := pricing.Lookup(provider, model); ok {
			cost = price.Cost(float64(t.InputTokens), float64(t.OutputTokens),
				float64(cacheRead), float64(cacheCreation))
		}

		a, ok := traces[t.TraceID]
		if !ok {
			traces[t.TraceID] = &acc{summary: t, startEpoch: startEpoch, endEpoch: endEpoch, rootName: name, hasRoot: hasRoot == 1}
			traces[t.TraceID].summary.Cost = cost
			continue
		}
		s := &a.summary
		if t.Time.Before(s.Time) {
			s.Time = t.Time
		}
		// The trace title is the parentless span's name; fall back to the
		// earliest span when the root wasn't ingested.
		if hasRoot == 1 && !a.hasRoot {
			a.rootName = name
			a.hasRoot = true
		} else if !a.hasRoot && startEpoch < a.startEpoch {
			a.rootName = name
		}
		if startEpoch < a.startEpoch {
			a.startEpoch = startEpoch
		}
		if endEpoch > a.endEpoch {
			a.endEpoch = endEpoch
		}
		s.SpanCount += t.SpanCount
		s.InputTokens += t.InputTokens
		s.OutputTokens += t.OutputTokens
		s.Cost += cost
		s.HasError = s.HasError || t.HasError
		if s.UserID == "" {
			s.UserID, s.UserEmail = t.UserID, t.UserEmail
		}
		if s.SessionID == "" {
			s.SessionID = t.SessionID
		}
		if s.ServiceName == "" {
			s.ServiceName = t.ServiceName
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]TraceSummary, 0, len(traces))
	for _, a := range traces {
		a.summary.Name = a.rootName
		a.summary.Duration = a.endEpoch - a.startEpoch
		result = append(result, a.summary)
	}
	sortTraceSummaries(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// QueryTrace returns all stored spans of one trace, oldest first, including
// raw attributes for the detail view.
func (s *Store) QueryTrace(ctx context.Context, traceID string) ([]SpanRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+spanColumns+`, COALESCE(attributes, '{}')
		FROM genai_spans WHERE trace_id = ? ORDER BY time, duration DESC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SpanRow
	for rows.Next() {
		var r SpanRow
		// JSON columns scan via duckdb.Composite, per the duckdb-go JSON
		// example: github.com/duckdb/duckdb-go/blob/main/examples/json
		var attrs duckdb.Composite[map[string]any]
		err := rows.Scan(&r.Time, &r.Duration, &r.TraceID, &r.SpanID, &r.ParentSpanID,
			&r.Name, &r.Kind, &r.Status, &r.ServiceName, &r.OperationName, &r.ProviderName,
			&r.RequestModel, &r.ResponseModel, &r.AgentName, &r.ToolName, &r.UserID,
			&r.UserEmail, &r.SessionID, &r.ErrorType, &r.FinishReasons,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.Reasoning, &attrs)
		if err != nil {
			return nil, err
		}
		if m := attrs.Get(); len(m) > 0 {
			r.Attrs = make(map[string]string, len(m))
			for key, val := range m {
				r.Attrs[key] = fmt.Sprintf("%v", val)
			}
		}
		r.price()
		result = append(result, r)
	}
	return result, rows.Err()
}
