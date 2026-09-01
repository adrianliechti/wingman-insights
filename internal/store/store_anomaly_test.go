package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestAnomalyAppDimensionSQL is a smoke test that the app-dimension anomaly
// queries reference genai_spans.app_id (not the removed service_name) and
// execute. A wrong column name is a runtime-only SQL error the build can't catch.
func TestAnomalyAppDimensionSQL(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC().Truncate(time.Hour)
	var spans []SpanRow
	for i := 0; i < 30; i++ {
		spans = append(spans, SpanRow{
			ReceivedAt: base, Time: base.Add(time.Duration(i) * time.Hour),
			TraceID: "t", SpanID: "s", AppID: "checkout", ProviderName: "anthropic",
			RequestModel: "claude", InputTokens: 100, OutputTokens: 50,
		})
	}
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatalf("insert: %v", err)
	}

	from, to := base, base.Add(30*time.Hour)
	if _, err := s.QueryAnomalyFeed(ctx, from, to, "1 hour", 0, 50, Filter{}); err != nil {
		t.Fatalf("QueryAnomalyFeed: %v", err)
	}
	if _, err := s.QueryCostAnomalies(ctx, from, to, "1 hour", "app", 0, Filter{}); err != nil {
		t.Fatalf("QueryCostAnomalies group_by=app: %v", err)
	}
	// The app filter must also bind against app_id on the spans path.
	if _, err := s.QueryCostAnomalies(ctx, from, to, "1 hour", "app", 0, Filter{App: []string{"checkout"}}); err != nil {
		t.Fatalf("QueryCostAnomalies with App filter: %v", err)
	}
}
