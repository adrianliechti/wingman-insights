package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestBackfillAppID guards the migrations (backfillSpansAppID + backfillAppID)
// that make app_id obey service.peer.name ?? service.name on pre-rule rows,
// reading the peer from the stored attributes JSON so an app is keyed
// identically across both tables.
func TestBackfillAppID(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ts := time.Now().UTC()

	// Old metric rows: app_id NULL (column added later). One carries a peer in
	// attributes (→ peer), one doesn't (→ resource service_name).
	if _, err := s.db.Exec(`INSERT INTO genai_metrics
		(received_at, time, service_name, app_id, metric_name, attributes)
		VALUES
		(?, ?, 'wingman', NULL, 'gen_ai.client.token.usage', '{"service.peer.name":"checkout"}'),
		(?, ?, 'wingman', NULL, 'gen_ai.client.token.usage', '{}')`,
		ts, ts, ts, ts); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}

	// Old span rows: app_id holds the resource service.name (pre-rename). One has
	// a recorded peer (→ lifted to peer), one doesn't (→ left as-is).
	if _, err := s.db.Exec(`INSERT INTO genai_spans
		(received_at, time, trace_id, span_id, app_id, attributes)
		VALUES
		(?, ?, 't1', 's1', 'wingman', '{"service.peer.name":"billing"}'),
		(?, ?, 't2', 's2', 'selfapp', '{}')`,
		ts, ts, ts, ts); err != nil {
		t.Fatalf("seed spans: %v", err)
	}

	// backfillSpansAppID covers genai_spans (only run by migrate() in the same
	// startup as the service_name -> app_id rename); backfillAppID covers the
	// null-guarded genai_metrics/http_metrics columns on every startup.
	if err := s.backfillSpansAppID(ctx); err != nil {
		t.Fatalf("backfillSpansAppID: %v", err)
	}
	if err := s.backfillAppID(ctx); err != nil {
		t.Fatalf("backfillAppID: %v", err)
	}

	want := func(query, label, expect string) {
		t.Helper()
		var got string
		if err := s.db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got != expect {
			t.Errorf("%s = %q, want %q", label, got, expect)
		}
	}
	want(`SELECT app_id FROM genai_metrics WHERE attributes ->> '$."service.peer.name"' = 'checkout'`,
		"metric with peer", "checkout")
	want(`SELECT app_id FROM genai_metrics WHERE attributes = '{}'`,
		"metric without peer (fallback to service_name)", "wingman")
	want(`SELECT app_id FROM genai_spans WHERE span_id = 's1'`,
		"span lifted to peer", "billing")
	want(`SELECT app_id FROM genai_spans WHERE span_id = 's2'`,
		"span without peer (unchanged)", "selfapp")
}

// TestFilterOptionsUnionsBothTables guards that the app dropdown offers apps
// seen in either telemetry source: a metric-only app must be selectable so its
// metric-sourced charts can be filtered, even with no spans.
func TestFilterOptionsUnionsBothTables(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ts := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO genai_metrics
		(received_at, time, app_id, metric_name) VALUES
		(?, ?, 'metric-only', 'gen_ai.client.token.usage')`, ts, ts); err != nil {
		t.Fatalf("seed metric: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO genai_spans
		(received_at, time, trace_id, span_id, app_id) VALUES
		(?, ?, 't1', 's1', 'span-only')`, ts, ts); err != nil {
		t.Fatalf("seed span: %v", err)
	}

	opts, err := s.QueryFilterOptions(ctx, ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	seen := map[string]bool{}
	for _, a := range opts.Apps {
		seen[a.ID] = true
	}
	if !seen["metric-only"] {
		t.Error("metric-only app missing from dropdown")
	}
	if !seen["span-only"] {
		t.Error("span-only app missing from dropdown")
	}
}
