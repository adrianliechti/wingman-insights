package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestAppIDColumnMigration verifies that a genai_spans table created before the
// service_name → app_id rename is migrated in place on the next open, keeping
// its data. It simulates the old schema by renaming app_id back to service_name,
// then reopens to drive migrate().
func TestAppIDColumnMigration(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertSpans(ctx, []SpanRow{{
		ReceivedAt: now, Time: now, TraceID: "t1", SpanID: "s1", AppID: "myapp",
		InputTokens: 5, OutputTokens: 5,
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Make the DB look pre-migration.
	if _, err := s.db.Exec("ALTER TABLE genai_spans RENAME COLUMN app_id TO service_name"); err != nil {
		t.Fatalf("simulate old schema: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = New() // runs migrate() against the old-schema file
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()

	if s.columnExists("genai_spans", "service_name") {
		t.Error("service_name column still present after migration")
	}
	if !s.columnExists("genai_spans", "app_id") {
		t.Fatal("app_id column missing after migration")
	}
	var got string
	if err := s.db.QueryRow("SELECT app_id FROM genai_spans WHERE span_id = 's1'").Scan(&got); err != nil {
		t.Fatalf("query app_id: %v", err)
	}
	if got != "myapp" {
		t.Errorf("app_id = %q, want %q (data lost in rename)", got, "myapp")
	}
}
