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
	AppID         string    `json:"app_id,omitempty"`
	OperationName string    `json:"operation_name,omitempty"`
	ProviderName  string    `json:"provider_name,omitempty"`
	RequestModel  string    `json:"request_model,omitempty"`
	ResponseModel string    `json:"response_model,omitempty"`
	AgentName     string    `json:"agent_name,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	UserEmail     string    `json:"user_email,omitempty"`
	UserName      string    `json:"user_name,omitempty"` // resolved display name; empty if unresolved
	UserKind      string    `json:"user_kind,omitempty"` // user | application; empty if unresolved
	SessionID     string    `json:"session_id,omitempty"`
	ErrorType     string    `json:"error_type,omitempty"`
	FinishReasons string    `json:"finish_reasons,omitempty"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	CacheRead     int64     `json:"cache_read_tokens"`
	CacheCreation int64     `json:"cache_creation_tokens"`
	Reasoning     int64     `json:"reasoning_tokens"`

	// Cost / cache_savings are materialized at insert time (see costBreakdown)
	// and read back as plain stored columns — never recomputed from token counts
	// at query time, so a span's cost stays what it was at the time it was
	// incurred even after the pricing catalog changes.
	Cost float64 `json:"cost"`
	// Attrs carries the raw span attributes; populated by QueryTrace only.
	Attrs map[string]string `json:"attributes,omitempty"`

	Attributes map[string]string `json:"-"`
}

// costBreakdown prices the span for the materialized cost columns: the four
// per-category costs, their sum (cost), cache savings (what the cache-read
// tokens would have cost at the full input rate minus what they actually
// cost), and whether a price was found at all. InputTokens is the inclusive
// prompt total (includes cache), so only the non-cached remainder bills at the
// input rate; reasoning is billed within output. Everything is 0/false for an
// unpriced model. Storing these at insert freezes them at the price then in
// effect — the right behavior for FinOps (cost as incurred) — and is what lets
// every cost-reading query aggregate spend with a plain SUM/BOOL_AND instead of
// re-pricing on read.
func (r SpanRow) costBreakdown() (inputCost, outputCost, cacheReadCost, cacheCreationCost, cost, savings float64, priced bool) {
	p, ok := pricing.Lookup(r.ProviderName, r.RequestModel)
	if !ok {
		return 0, 0, 0, 0, 0, 0, false
	}
	regular := float64(r.InputTokens) - float64(r.CacheRead) - float64(r.CacheCreation)
	if regular < 0 {
		regular = 0
	}
	inputCost = p.TokenCost("input", regular)
	outputCost = p.TokenCost("output", float64(r.OutputTokens))
	cacheReadCost = p.TokenCost("cache_read", float64(r.CacheRead))
	cacheCreationCost = p.TokenCost("cache_creation", float64(r.CacheCreation))
	cost = inputCost + outputCost + cacheReadCost + cacheCreationCost
	savings = p.TokenCost("input", float64(r.CacheRead)) - cacheReadCost
	return inputCost, outputCost, cacheReadCost, cacheCreationCost, cost, savings, true
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
		 status, app_id, operation_name, provider_name, request_model,
		 response_model, agent_name, tool_name, user_id, user_email, session_id,
		 error_type, finish_reasons, input_tokens, output_tokens, cache_read_tokens,
		 cache_creation_tokens, reasoning_tokens, attributes,
		 input_cost, output_cost, cache_read_cost, cache_creation_cost, cost, cache_savings, priced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		attrs, _ := json.Marshal(r.Attributes)
		inputCost, outputCost, cacheReadCost, cacheCreationCost, cost, savings, priced := r.costBreakdown()
		_, err := stmt.ExecContext(ctx,
			r.ReceivedAt, r.Time, r.Duration, r.TraceID, r.SpanID, r.ParentSpanID,
			r.Name, r.Kind, r.Status, r.AppID, r.OperationName, r.ProviderName,
			r.RequestModel, r.ResponseModel, r.AgentName, r.ToolName, r.UserID,
			r.UserEmail, r.SessionID, r.ErrorType, r.FinishReasons, r.InputTokens,
			r.OutputTokens, r.CacheRead, r.CacheCreation, r.Reasoning, string(attrs),
			inputCost, outputCost, cacheReadCost, cacheCreationCost, cost, savings, priced,
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
	COALESCE(app_id, ''), COALESCE(operation_name, ''), COALESCE(provider_name, ''),
	COALESCE(request_model, ''), COALESCE(response_model, ''), COALESCE(agent_name, ''),
	COALESCE(tool_name, ''), COALESCE(user_id, ''), COALESCE(user_email, ''),
	COALESCE(session_id, ''), COALESCE(error_type, ''), COALESCE(finish_reasons, ''),
	COALESCE(cost, 0),
	COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cache_read_tokens, 0),
	COALESCE(cache_creation_tokens, 0), COALESCE(reasoning_tokens, 0)`

// TraceSummary is one trace condensed to a list entry.
type TraceSummary struct {
	TraceID      string    `json:"trace_id"`
	Name         string    `json:"name"`
	Time         time.Time `json:"time"`
	Duration     float64   `json:"duration"`
	AppID        string    `json:"app_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	UserEmail    string    `json:"user_email,omitempty"`
	UserName     string    `json:"user_name,omitempty"` // resolved display name; empty if unresolved
	UserKind     string    `json:"user_kind,omitempty"` // user | application; empty if unresolved
	SessionID    string    `json:"session_id,omitempty"`
	SpanCount    int64     `json:"span_count"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	Cost         float64   `json:"cost"`
	HasError     bool      `json:"has_error"`
}

// QueryTraceList returns recent traces, newest first. Cost is a plain SUM of
// the per-span materialized cost column (priced once, at insert time), so this
// needs no per-model live pricing and no per-trace merge in Go — one row per
// trace comes straight out of SQL.
func (s *Store) QueryTraceList(ctx context.Context, from, to time.Time, f Filter, onlyErrors bool, limit int) ([]TraceSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	clause, fargs := f.spansClause()
	// Pick the newest trace ids first so aggregation only touches `limit`
	// traces instead of every trace in the window.
	recent := `SELECT trace_id FROM genai_spans
		WHERE time >= ? AND time <= ?` + clause
	if onlyErrors {
		recent += ` AND (status = 'error' OR (error_type IS NOT NULL AND error_type != ''))`
	}
	recent += ` GROUP BY trace_id ORDER BY MAX(time) DESC LIMIT ?`

	query := `
		SELECT
			trace_id,
			COALESCE(MAX(CASE WHEN parent_span_id = '' OR parent_span_id IS NULL THEN name END), arg_min(name, time)) as name,
			MIN(time) as start_time,
			MIN(epoch(time)) as start_epoch,
			MAX(epoch(time) + duration) as end_epoch,
			COALESCE(NULLIF(MAX(app_id), ''), '') as app_id,
			COALESCE(NULLIF(MAX(user_id), ''), '') as user_id,
			COALESCE(NULLIF(MAX(user_email), ''), '') as user_email,
			COALESCE(NULLIF(MAX(session_id), ''), '') as session_id,
			COUNT(*) as span_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cost), 0) as cost,
			BOOL_OR(status = 'error' OR (error_type IS NOT NULL AND error_type != '')) as has_error
		FROM genai_spans
		WHERE time >= ? AND time <= ?` + clause + `
		  AND trace_id IN (` + recent + `)
		GROUP BY trace_id`
	args := append([]any{from, to}, fargs...)
	args = append(args, from, to)
	args = append(args, fargs...)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TraceSummary
	for rows.Next() {
		var t TraceSummary
		var startEpoch, endEpoch float64
		if err := rows.Scan(&t.TraceID, &t.Name, &t.Time, &startEpoch, &endEpoch,
			&t.AppID, &t.UserID, &t.UserEmail, &t.SessionID, &t.SpanCount,
			&t.InputTokens, &t.OutputTokens, &t.Cost, &t.HasError); err != nil {
			return nil, err
		}
		t.Duration = endEpoch - startEpoch
		t.UserName, t.UserKind = s.resolveName(t.UserID, t.UserEmail)
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortTraceSummaries(result)
	return result, nil
}

// maxTraceSpans caps QueryTrace's result: a normal trace has at most dozens of
// spans, but a runaway agent tool-call loop could in principle emit far more,
// and this is the one query in the store that returns raw (unaggregated) rows
// with no natural bound.
const maxTraceSpans = 5000

// QueryTrace returns all stored spans of one trace, oldest first, including
// raw attributes for the detail view.
func (s *Store) QueryTrace(ctx context.Context, traceID string) ([]SpanRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+spanColumns+`, COALESCE(attributes, '{}')
		FROM genai_spans WHERE trace_id = ? ORDER BY time, duration DESC LIMIT ?`, traceID, maxTraceSpans)
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
			&r.Name, &r.Kind, &r.Status, &r.AppID, &r.OperationName, &r.ProviderName,
			&r.RequestModel, &r.ResponseModel, &r.AgentName, &r.ToolName, &r.UserID,
			&r.UserEmail, &r.SessionID, &r.ErrorType, &r.FinishReasons, &r.Cost,
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
		r.UserName, r.UserKind = s.resolveName(r.UserID, r.UserEmail)
		result = append(result, r)
	}
	return result, rows.Err()
}
