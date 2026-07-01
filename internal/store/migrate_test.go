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
	// Make the DB look pre-migration. Drop the trace_id index first: a real
	// pre-rename DB predates it, and DuckDB refuses to rename a column while an
	// index depends on the table.
	if _, err := s.db.Exec("DROP INDEX IF EXISTS idx_genai_spans_trace_id"); err != nil {
		t.Fatalf("drop index: %v", err)
	}
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

// TestDropDeadMetricColumns verifies that genai_metrics/http_metrics tables
// created before response_model/server_address/url_scheme/server_port/min_val/
// max_val were dropped (retired as write-only columns no query ever read) lose
// those columns in place on the next open, keeping every other column's data.
func TestDropDeadMetricColumns(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertGenAIMetrics(ctx, []GenAIMetricRow{{
		ReceivedAt: now, Time: now, AppID: "myapp", MetricName: "gen_ai.client.token.usage",
		RequestModel: "gpt-4", Count: 3, Sum: 9,
	}}); err != nil {
		t.Fatalf("insert genai_metrics: %v", err)
	}
	if err := s.InsertHTTPMetrics(ctx, []HTTPMetricRow{{
		ReceivedAt: now, Time: now, MetricName: "http.server.request.duration",
		Method: "GET", Route: "/v1/chat", Count: 2, Sum: 4,
	}}); err != nil {
		t.Fatalf("insert http_metrics: %v", err)
	}
	// Make the DBs look pre-migration: re-add the retired columns with data in
	// them, as a DB predating this change would already have.
	for _, stmt := range []string{
		"ALTER TABLE genai_metrics ADD COLUMN response_model VARCHAR",
		"ALTER TABLE genai_metrics ADD COLUMN server_address VARCHAR",
		"ALTER TABLE genai_metrics ADD COLUMN min_val DOUBLE",
		"ALTER TABLE genai_metrics ADD COLUMN max_val DOUBLE",
		"UPDATE genai_metrics SET response_model = 'gpt-4-turbo', server_address = 'api.openai.com', min_val = 1, max_val = 5",
		"ALTER TABLE http_metrics ADD COLUMN url_scheme VARCHAR",
		"ALTER TABLE http_metrics ADD COLUMN server_address VARCHAR",
		"ALTER TABLE http_metrics ADD COLUMN server_port INTEGER",
		"ALTER TABLE http_metrics ADD COLUMN min_val DOUBLE",
		"ALTER TABLE http_metrics ADD COLUMN max_val DOUBLE",
		"UPDATE http_metrics SET url_scheme = 'https', server_address = 'localhost', server_port = 443, min_val = 1, max_val = 2",
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("simulate old schema (%s): %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = New() // runs migrate() against the old-schema file
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()

	for _, col := range []string{"response_model", "server_address", "min_val", "max_val"} {
		if s.columnExists("genai_metrics", col) {
			t.Errorf("genai_metrics.%s still present after migration", col)
		}
	}
	for _, col := range []string{"url_scheme", "server_address", "server_port", "min_val", "max_val"} {
		if s.columnExists("http_metrics", col) {
			t.Errorf("http_metrics.%s still present after migration", col)
		}
	}

	var requestModel string
	var count int64
	if err := s.db.QueryRow("SELECT request_model, count FROM genai_metrics WHERE app_id = 'myapp'").
		Scan(&requestModel, &count); err != nil {
		t.Fatalf("query genai_metrics: %v", err)
	}
	if requestModel != "gpt-4" || count != 3 {
		t.Errorf("genai_metrics kept columns = (%q, %d), want (%q, %d)", requestModel, count, "gpt-4", 3)
	}

	var route string
	if err := s.db.QueryRow("SELECT route FROM http_metrics WHERE method = 'GET'").Scan(&route); err != nil {
		t.Fatalf("query http_metrics: %v", err)
	}
	if route != "/v1/chat" {
		t.Errorf("http_metrics.route = %q, want %q (data lost in column drop)", route, "/v1/chat")
	}
}
