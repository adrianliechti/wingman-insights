package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestTokenChartAfterBackfill reproduces the reported prod symptom end-to-end:
// a header-auth app's metric rows were ingested with no service.peer.name, so
// app_id was NULL and the metric-sourced token chart collapsed to one entry when
// the app (selectable via its service.name) was filtered. After the migration
// fills app_id from service.name, the chart query must return every bucket.
func TestTokenChartAfterBackfill(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)

	// Pre-fix rows: app_id NULL (no peer), only the resource service.name "acme".
	// Three hourly buckets, input+output each — the shape that drew one entry.
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		for _, tt := range []string{"input", "output"} {
			if _, err := s.db.Exec(`INSERT INTO genai_metrics
				(received_at, time, service_name, app_id, metric_name, token_type,
				 request_model, count, sum, attributes)
				VALUES (?, ?, 'acme', NULL, 'gen_ai.client.token.usage', ?, 'claude', 1, 100, '{}')`,
				ts, ts, tt); err != nil {
				t.Fatalf("seed bucket %d %s: %v", i, tt, err)
			}
		}
	}

	from, to := base.Add(-time.Hour), base.Add(4*time.Hour)
	acme := Filter{App: []string{"acme"}}

	// Before migration: NULL app_id can't be matched by the app filter → nothing.
	pre, err := s.QueryTokenTimeseries(ctx, from, to, "1 hour", acme)
	if err != nil {
		t.Fatalf("pre query: %v", err)
	}
	if len(pre) != 0 {
		t.Fatalf("pre-migration: got %d points for unattributed rows, want 0", len(pre))
	}

	// Run the migration as startup would.
	if err := s.backfillAppID(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// After migration: app_id = "acme", so the chart returns all three buckets
	// (× input/output) — not one stray entry.
	post, err := s.QueryTokenTimeseries(ctx, from, to, "1 hour", acme)
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	buckets := map[time.Time]bool{}
	for _, p := range post {
		buckets[p.Bucket] = true
	}
	if len(buckets) != 3 {
		t.Errorf("post-migration: %d distinct buckets, want 3 (chart no longer collapsed)", len(buckets))
	}
	if len(post) != 6 {
		t.Errorf("post-migration: %d points, want 6 (3 buckets × input/output)", len(post))
	}

	// And the app is now offered in the dropdown (union covers metric-only apps).
	opts, err := s.QueryFilterOptions(ctx, from, to)
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	found := false
	for _, a := range opts.Apps {
		if a.ID == "acme" {
			found = true
		}
	}
	if !found {
		t.Error("acme missing from app dropdown after backfill")
	}
}
