package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// contextBuckets are the input-size histogram bins, log-ish steps with edges
// aligned to the long-context pricing thresholds (200k for Anthropic/Gemini,
// 272k for OpenAI) so the premium-billed share is directly readable.
// Max is the inclusive upper edge in tokens; 0 marks the unbounded last bin.
var contextBuckets = []struct {
	Label string
	Max   int64
}{
	{"≤ 1k", 1000},
	{"1k–4k", 4000},
	{"4k–16k", 16000},
	{"16k–64k", 64000},
	{"64k–128k", 128000},
	{"128k–200k", 200000},
	{"200k–272k", 272000},
	{"> 272k", 0},
}

// ContextBucketRow is one input-size bin: how many LLM calls landed in it and
// what they cost (summed from the per-span materialized cost column).
type ContextBucketRow struct {
	Bucket   string  `json:"bucket"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

// QueryContextHistogram bins LLM calls (spans carrying input tokens) by their
// inclusive prompt size. Every bin is returned, zero-filled and in ascending
// order, so the chart axis is stable regardless of the data.
func (s *Store) QueryContextHistogram(ctx context.Context, from, to time.Time, f Filter) ([]ContextBucketRow, error) {
	clause, fargs := f.spansClause()
	var cases strings.Builder
	for i, b := range contextBuckets {
		if b.Max > 0 {
			fmt.Fprintf(&cases, " WHEN input_tokens <= %d THEN %d", b.Max, i)
		} else {
			fmt.Fprintf(&cases, " ELSE %d", i)
		}
	}
	query := `SELECT CASE` + cases.String() + ` END AS bin,
			COUNT(*), COALESCE(SUM(cost), 0)
		FROM genai_spans
		WHERE input_tokens > 0 AND time >= ? AND time <= ?` + clause + `
		GROUP BY bin`
	args := append([]any{from, to}, fargs...)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]ContextBucketRow, len(contextBuckets))
	for i, b := range contextBuckets {
		result[i].Bucket = b.Label
	}
	for rows.Next() {
		var bin int
		var n int64
		var cost float64
		if err := rows.Scan(&bin, &n, &cost); err != nil {
			return nil, err
		}
		if bin >= 0 && bin < len(result) {
			result[bin].Requests = n
			result[bin].Cost = cost
		}
	}
	return result, rows.Err()
}
