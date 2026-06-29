package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestAppFilterNarrowsMetrics is the regression guard for the App filter being
// silently ignored on metric-sourced widgets: app_id (service.peer.name) now
// lives on genai_metrics, so filtering by app must narrow token/active-user
// queries, not leave them unchanged.
func TestAppFilterNarrowsMetrics(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Add(-time.Hour)
	// Two apps, distinct users and token volumes, one usage row each.
	metric := func(app, user string, tokens float64) GenAIMetricRow {
		return GenAIMetricRow{
			ReceivedAt: now, Time: now, ServiceName: "wingman", AppID: app,
			MetricName: "gen_ai.client.token.usage", TokenType: "input",
			ProviderName: "anthropic", RequestModel: "claude",
			EndUserID: user, EndUserEmail: user + "@corp.com", Count: 1, Sum: tokens,
		}
	}
	if err := s.InsertGenAIMetrics(ctx, []GenAIMetricRow{
		metric("checkout", "alice", 100),
		metric("checkout", "bob", 200),
		metric("billing", "carol", 999),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	to := now.Add(time.Hour)
	from := now.Add(-time.Hour)
	checkout := Filter{App: "checkout"}

	// Token summary: only checkout's 300 tokens, never billing's 999.
	sum, err := s.QueryTokenSummary(ctx, from, to, checkout)
	if err != nil {
		t.Fatalf("token summary: %v", err)
	}
	var total float64
	for _, r := range sum {
		total += r.TotalTokens
	}
	if total != 300 {
		t.Errorf("token summary tokens = %v, want 300 (checkout only)", total)
	}

	// Active users: alice + bob, not carol.
	au, err := s.QueryActiveUsers(ctx, to, checkout)
	if err != nil {
		t.Fatalf("active users: %v", err)
	}
	if au.DAU != 2 {
		t.Errorf("active users DAU = %d, want 2 (checkout only)", au.DAU)
	}

	// Top consumers: only checkout's users.
	top, err := s.QueryTopConsumers(ctx, from, to, 10, checkout)
	if err != nil {
		t.Fatalf("top consumers: %v", err)
	}
	if len(top) != 2 {
		t.Errorf("top consumers = %d rows, want 2 (checkout only)", len(top))
	}
	for _, c := range top {
		if c.ID == "carol" {
			t.Errorf("top consumers leaked billing user carol")
		}
	}
}
