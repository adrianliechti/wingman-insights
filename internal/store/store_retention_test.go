package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestPruneBefore verifies retention deletes only rows older than the cutoff,
// across all three telemetry tables.
func TestPruneBefore(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	if err := s.InsertSpans(ctx, []SpanRow{
		{ReceivedAt: now, Time: old, TraceID: "t-old", SpanID: "s-old"},
		{ReceivedAt: now, Time: now, TraceID: "t-new", SpanID: "s-new"},
	}); err != nil {
		t.Fatalf("seed spans: %v", err)
	}
	if err := s.InsertMetrics(ctx,
		[]GenAIMetricRow{
			{ReceivedAt: now, Time: old, MetricName: "gen_ai.client.token.usage"},
			{ReceivedAt: now, Time: now, MetricName: "gen_ai.client.token.usage"},
		},
		[]HTTPMetricRow{
			{ReceivedAt: now, Time: old, MetricName: "http.server.request.duration"},
			{ReceivedAt: now, Time: now, MetricName: "http.server.request.duration"},
		}); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}

	n, err := s.PruneBefore(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 {
		t.Errorf("pruned %d rows, want 3", n)
	}

	for _, table := range []string{"genai_spans", "genai_metrics", "http_metrics"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("%s: count: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s: %d rows remain, want 1", table, count)
		}
	}
}
