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

// TestAppFilterResolvesDirectoryAliases guards the P0: the app dropdown sends the
// resolved canonical id, so filtering by it must expand back to the raw
// service.peer.name aliases on the rows — not match the raw column literally.
func TestAppFilterResolvesDirectoryAliases(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ts := time.Now().UTC()
	insert := func(appID string, sum float64) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO genai_metrics
			(received_at, time, metric_name, token_type, app_id, enduser_id, count, sum)
			VALUES (?, ?, 'gen_ai.client.token.usage', 'input', ?, 'u1', 1, ?)`,
			ts, ts, appID, sum); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// "sp-guid" is a directory alias of canonical app "app-client" (the Worker app
	// in mappingStub); "other-app" is unknown to the directory.
	insert("sp-guid", 100)
	insert("other-app", 999)

	s.SetDirectory(mappingStub{})
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	// Dropdown sends the canonical id "app-client"; it must match the sp-guid row.
	resolved, err := s.QueryTokenSummary(ctx, from, to, Filter{App: "app-client"})
	if err != nil {
		t.Fatalf("token summary (resolved app): %v", err)
	}
	var got float64
	for _, r := range resolved {
		got += r.TotalTokens
	}
	if got != 100 {
		t.Errorf("resolved-app filter tokens = %v, want 100 (alias expansion)", got)
	}

	// An unresolved raw app_id still matches directly.
	raw, err := s.QueryTokenSummary(ctx, from, to, Filter{App: "other-app"})
	if err != nil {
		t.Fatalf("token summary (raw app): %v", err)
	}
	got = 0
	for _, r := range raw {
		got += r.TotalTokens
	}
	if got != 999 {
		t.Errorf("raw-app filter tokens = %v, want 999", got)
	}
}
