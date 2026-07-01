package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestCostIsFrozenNotRecomputed guards the core FinOps invariant: every
// cost-reading query (breakdown, timeseries, trace list, trace detail,
// user stats) must report the span's *stored* cost, never re-derive it from
// token counts via the current pricing catalog. Before this was fixed,
// QueryCostBreakdown/QueryCostTimeseries/QueryTraceList/QueryTrace recomputed
// live while QueryUserStats/QueryAppAdoption/QueryCostAnomalies read the
// materialized column — the same span would report two different costs
// depending which page you looked at, and the discrepancy would appear (or
// change) every time the pricing catalog was refreshed.
//
// This test inserts a span via raw SQL with a cost deliberately different from
// what today's pricing catalog would compute for its token counts (simulating
// a row priced under an older catalog), then asserts every consumer reports
// the stored value, not a live recomputation.
func TestCostIsFrozenNotRecomputed(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ts := time.Now().UTC()
	// openai/gpt-5.1 is a real catalog entry (input $1.25/M). 1,000,000 input
	// tokens with no cache/output would live-recompute to $1.25 — but this row
	// carries a stored cost/breakdown of $99.99, standing in for "priced under
	// a catalog that has since changed". If any query recomputes live, it will
	// report 1.25 instead of 99.99 and the test fails.
	const storedCost = 99.99
	if _, err := s.db.Exec(`INSERT INTO genai_spans
		(received_at, time, duration, trace_id, span_id, name, status, app_id,
		 provider_name, request_model, input_tokens, output_tokens,
		 cache_read_tokens, cache_creation_tokens, reasoning_tokens,
		 input_cost, output_cost, cache_read_cost, cache_creation_cost, cost, cache_savings, priced)
		VALUES (?, ?, 1.0, 't1', 's1', 'root', 'ok', 'myapp',
		        'openai', 'gpt-5.1', 1000000, 0, 0, 0, 0,
		        ?, 0, 0, 0, ?, 0, true)`,
		ts, ts, storedCost, storedCost); err != nil {
		t.Fatalf("seed span: %v", err)
	}

	ctx := context.Background()
	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	breakdown, err := s.QueryCostBreakdown(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("QueryCostBreakdown: %v", err)
	}
	if len(breakdown) != 1 {
		t.Fatalf("QueryCostBreakdown rows = %d, want 1", len(breakdown))
	}
	if breakdown[0].TotalCost != storedCost {
		t.Errorf("QueryCostBreakdown.TotalCost = %v, want stored %v (not live-recomputed)", breakdown[0].TotalCost, storedCost)
	}
	if !breakdown[0].Priced {
		t.Errorf("QueryCostBreakdown.Priced = false, want true")
	}
	// Categories must sum to the total exactly (input_cost carries the whole
	// stored value here; the rest are 0).
	sum := breakdown[0].InputCost + breakdown[0].OutputCost + breakdown[0].CacheReadCost + breakdown[0].CacheCreationCost
	if sum != breakdown[0].TotalCost {
		t.Errorf("category costs sum to %v, want TotalCost %v", sum, breakdown[0].TotalCost)
	}

	tl, err := s.QueryTraceList(ctx, from, to, Filter{}, false, 10)
	if err != nil {
		t.Fatalf("QueryTraceList: %v", err)
	}
	if len(tl) != 1 || tl[0].Cost != storedCost {
		t.Errorf("QueryTraceList cost = %+v, want %v", tl, storedCost)
	}

	trace, err := s.QueryTrace(ctx, "t1")
	if err != nil {
		t.Fatalf("QueryTrace: %v", err)
	}
	if len(trace) != 1 || trace[0].Cost != storedCost {
		t.Errorf("QueryTrace cost = %+v, want %v", trace, storedCost)
	}

	users, err := s.QueryUserStats(ctx, from, to, 200, Filter{})
	if err != nil {
		t.Fatalf("QueryUserStats: %v", err)
	}
	// This span has no user, so it should not appear in per-user stats; the
	// point of including this call is to confirm it doesn't error, and that a
	// costed-but-userless row isn't silently attributed to a phantom user.
	for _, u := range users {
		if u.Cost == storedCost {
			t.Errorf("unexpected user row picked up the userless span's cost: %+v", u)
		}
	}

	apps, err := s.QueryAppAdoption(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("QueryAppAdoption: %v", err)
	}
	var appCost float64
	for _, a := range apps {
		if a.AppID == "myapp" {
			appCost = a.Cost
		}
	}
	if appCost != storedCost {
		t.Errorf("QueryAppAdoption cost for myapp = %v, want %v", appCost, storedCost)
	}

	ts2, err := s.QueryCostTimeseries(ctx, from, to, "1 hour", "app", Filter{})
	if err != nil {
		t.Fatalf("QueryCostTimeseries: %v", err)
	}
	var tsCost float64
	for _, p := range ts2 {
		tsCost += p.Value
	}
	if tsCost != storedCost {
		t.Errorf("QueryCostTimeseries total = %v, want %v", tsCost, storedCost)
	}
}

// TestBackfillSpanCostCategories verifies backfillSpanCost fills the
// per-category cost columns (input/output/cache_read/cache_creation) for rows
// inserted before those columns existed — simulated here by inserting a span
// with only the original cost/cache_savings columns set (input_cost and
// friends stay NULL, as they would on a pre-migration row) and letting New()
// drive migrate() -> backfillSpanCost on reopen.
func TestBackfillSpanCostCategories(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ts := time.Now().UTC()
	// openai/gpt-5.1: input $1.25/M, output $10/M, cache_read $0.125/M, cache
	// write $0/M. regular = 1,000,000 - 200,000 - 100,000 = 700,000.
	if _, err := s.db.Exec(`INSERT INTO genai_spans
		(received_at, time, duration, trace_id, span_id, name, status,
		 provider_name, request_model, input_tokens, output_tokens,
		 cache_read_tokens, cache_creation_tokens, reasoning_tokens)
		VALUES (?, ?, 1.0, 't1', 's1', 'root', 'ok',
		        'openai', 'gpt-5.1', 1000000, 500000, 200000, 100000, 0)`,
		ts, ts); err != nil {
		t.Fatalf("seed span: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = New() // runs migrate() -> backfillSpanCost against the NULL columns
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()

	var inputCost, outputCost, cacheReadCost, cacheCreationCost, cost, savings float64
	var priced bool
	if err := s.db.QueryRow(`SELECT input_cost, output_cost, cache_read_cost, cache_creation_cost, cost, cache_savings, priced
		FROM genai_spans WHERE span_id = 's1'`).
		Scan(&inputCost, &outputCost, &cacheReadCost, &cacheCreationCost, &cost, &savings, &priced); err != nil {
		t.Fatalf("query backfilled row: %v", err)
	}
	want := struct{ inputCost, outputCost, cacheReadCost, cacheCreationCost, cost, savings float64 }{
		inputCost: 0.875, outputCost: 5.0, cacheReadCost: 0.025, cacheCreationCost: 0, cost: 5.9, savings: 0.225,
	}
	if !priced {
		t.Error("priced = false, want true")
	}
	if inputCost != want.inputCost || outputCost != want.outputCost ||
		cacheReadCost != want.cacheReadCost || cacheCreationCost != want.cacheCreationCost {
		t.Errorf("categories = (in:%v out:%v cr:%v cc:%v), want (in:%v out:%v cr:%v cc:%v)",
			inputCost, outputCost, cacheReadCost, cacheCreationCost,
			want.inputCost, want.outputCost, want.cacheReadCost, want.cacheCreationCost)
	}
	if cost != want.cost {
		t.Errorf("cost = %v, want %v", cost, want.cost)
	}
	if inputCost+outputCost+cacheReadCost+cacheCreationCost != cost {
		t.Errorf("categories sum to %v, want cost %v", inputCost+outputCost+cacheReadCost+cacheCreationCost, cost)
	}
	if savings != want.savings {
		t.Errorf("cache_savings = %v, want %v", savings, want.savings)
	}
}
