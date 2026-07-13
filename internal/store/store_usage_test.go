package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryCostTotal(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	// Empty database → should return 0, not an error.
	total, err := s.QueryCostTotal(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("empty DB: %v", err)
	}
	if total != 0 {
		t.Errorf("empty DB: got %v, want 0", total)
	}

	const (
		appA  = "app-a"
		appB  = "app-b"
		userX = "user-x"
		userY = "user-y"
	)

	seed := func(traceID, spanID, appID, userID string, cost float64) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO genai_spans
			(received_at, time, duration, trace_id, span_id, name, status, app_id,
			 provider_name, request_model, input_tokens, output_tokens,
			 cache_read_tokens, cache_creation_tokens, reasoning_tokens,
			 input_cost, output_cost, cache_read_cost, cache_creation_cost, cost, cache_savings, priced,
			 user_id)
			VALUES (?, ?, 1.0, ?, ?, 'root', 'ok', ?,
			        'openai', 'gpt-4o', 100, 50, 0, 0, 0,
			        ?, 0, 0, 0, ?, 0, true, ?)`,
			now, now, traceID, spanID, appID,
			cost, cost, userID); err != nil {
			t.Fatalf("seed span: %v", err)
		}
	}

	seed("t1", "s1", appA, userX, 1.00) // user X, app A
	seed("t2", "s2", appA, userY, 0.50) // user Y, app A
	seed("t3", "s3", appB, userX, 0.25) // user X, app B

	// No filter → sum of all three spans.
	total, err = s.QueryCostTotal(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if want := 1.75; total != want {
		t.Errorf("all: got %v, want %v", total, want)
	}

	// Filter by user X → spans in app-a and app-b for that user.
	total, err = s.QueryCostTotal(ctx, from, to, Filter{User: userX})
	if err != nil {
		t.Fatalf("user X: %v", err)
	}
	if want := 1.25; total != want {
		t.Errorf("user X: got %v, want %v", total, want)
	}

	// Filter by app A → both users in app-a.
	total, err = s.QueryCostTotal(ctx, from, to, Filter{App: appA})
	if err != nil {
		t.Fatalf("app A: %v", err)
	}
	if want := 1.50; total != want {
		t.Errorf("app A: got %v, want %v", total, want)
	}

	// Filter by user X + app A → single span.
	total, err = s.QueryCostTotal(ctx, from, to, Filter{User: userX, App: appA})
	if err != nil {
		t.Fatalf("user X + app A: %v", err)
	}
	if want := 1.00; total != want {
		t.Errorf("user X + app A: got %v, want %v", total, want)
	}

	// Window that excludes all spans → 0.
	total, err = s.QueryCostTotal(ctx, now.Add(time.Hour), now.Add(2*time.Hour), Filter{})
	if err != nil {
		t.Fatalf("empty window: %v", err)
	}
	if total != 0 {
		t.Errorf("empty window: got %v, want 0", total)
	}
}
