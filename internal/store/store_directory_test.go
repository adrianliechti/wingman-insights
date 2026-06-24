package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"insights/pkg/directory"
)

// mappingStub is a fake directory exercising every resolution path: a user
// reachable by raw id OR by email (fallback), an app reachable by its service
// principal id OR client id, and everything else unknown.
type mappingStub struct{}

func (mappingStub) Lookup(id string) (directory.Identity, bool) {
	switch strings.ToLower(id) {
	case "u-guid-1", "alice@corp.com", "alice-obj":
		return directory.Identity{ID: "alice-obj", Name: "Alice", Kind: directory.KindUser}, true
	case "sp-guid", "app-client":
		return directory.Identity{ID: "app-client", Name: "Worker", Kind: directory.KindApplication}, true
	}
	return directory.Identity{}, false
}

func (mappingStub) Aliases(id string) []string {
	switch strings.ToLower(id) {
	case "alice-obj":
		return []string{"u-guid-1", "alice@corp.com", "alice-obj"}
	case "app-client":
		return []string{"sp-guid", "app-client"}
	}
	return nil
}

func TestDirectoryMapping(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Token-usage metrics with heterogeneous raw user.id / user.email:
	//   u-guid-1            -> Alice   (by id)
	//   other-guid + email  -> Alice   (by EMAIL fallback; id is unknown)
	//   sp-guid             -> Worker  (app, by service-principal id)
	//   ghost               -> unresolved (passes through as the raw id)
	ts := time.Now().UTC() // bind time as Go UTC, exactly like the ingest path
	insert := func(id, email, tt string, count int, sum float64) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO genai_metrics
			(received_at, time, metric_name, token_type, enduser_id, enduser_email, count, sum)
			VALUES (?, ?, 'gen_ai.client.token.usage', ?, ?, ?, ?, ?)`,
			ts, ts, tt, id, email, count, sum); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	insert("u-guid-1", "", "input", 3, 100)
	insert("u-guid-1", "", "output", 0, 50)
	insert("other-guid", "alice@corp.com", "input", 2, 200)
	insert("other-guid", "alice@corp.com", "output", 0, 20)
	insert("sp-guid", "", "input", 5, 500)
	insert("ghost", "", "input", 1, 10)

	s.SetDirectory(mappingStub{})

	ctx := context.Background()
	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	// 1) Unification: the two Alice ids (one resolved by email) fold into one row.
	top, err := s.QueryTopConsumers(ctx, from, to, 10, Filter{})
	if err != nil {
		t.Fatalf("top consumers: %v", err)
	}
	byID := map[string]TopConsumerRow{}
	for _, r := range top {
		byID[r.ID] = r
	}
	alice, ok := byID["alice-obj"]
	if !ok {
		t.Fatalf("Alice not resolved/folded; got %+v", top)
	}
	if alice.Name != "Alice" || alice.Kind != "user" {
		t.Errorf("Alice identity = %+v, want name=Alice kind=user", alice)
	}
	if alice.TotalTokens != 370 { // 100+50 + 200+20
		t.Errorf("Alice tokens = %v, want 370 (both ids merged)", alice.TotalTokens)
	}
	if alice.TotalRequests != 5 { // 3 + 2 input rows
		t.Errorf("Alice requests = %v, want 5", alice.TotalRequests)
	}
	if w := byID["app-client"]; w.Name != "Worker" || w.Kind != "application" {
		t.Errorf("Worker (app via service principal) = %+v", w)
	}
	if _, ok := byID["ghost"]; !ok {
		t.Errorf("unresolved 'ghost' should pass through as its raw id")
	}

	// 2) Email-fallback FILTER: selecting Alice must also catch the rows that
	//    only match on the email column (the bug this guards against).
	f := s.ExpandUserFilter(Filter{User: "alice-obj"})
	top, err = s.QueryTopConsumers(ctx, from, to, 10, f)
	if err != nil {
		t.Fatalf("filtered top consumers: %v", err)
	}
	if len(top) != 1 || top[0].ID != "alice-obj" {
		t.Fatalf("filtered = %+v, want only Alice", top)
	}
	if top[0].TotalTokens != 370 {
		t.Errorf("filtered Alice tokens = %v, want 370 (incl. email-resolved rows)", top[0].TotalTokens)
	}

	// 3) Filter dropdown dedupes raw ids into one entry per canonical identity.
	opts, err := s.QueryFilterOptions(ctx, from, to)
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	users := map[string]FilterUser{}
	for _, u := range opts.Users {
		users[u.ID] = u
	}
	if u, ok := users["alice-obj"]; !ok || u.Name != "Alice" {
		t.Errorf("dropdown Alice = %+v ok=%v", u, ok)
	}
	for _, raw := range []string{"u-guid-1", "other-guid"} {
		if _, dup := users[raw]; dup {
			t.Errorf("raw id %q should be folded into alice-obj, not listed separately", raw)
		}
	}

	// 4) Traces resolve the span's user for display (here via email fallback).
	if _, err := s.db.Exec(`INSERT INTO genai_spans
		(received_at, time, duration, trace_id, span_id, name, status, user_id, user_email, input_tokens, output_tokens)
		VALUES (?, ?, 1.0, 'trace-1', 'span-1', 'root', 'ok', 'other-guid', 'alice@corp.com', 10, 5)`,
		ts, ts); err != nil {
		t.Fatalf("insert span: %v", err)
	}
	traces, err := s.QueryTraceList(ctx, from, to, Filter{}, false, 50)
	if err != nil {
		t.Fatalf("trace list: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("traces = %d, want 1", len(traces))
	}
	if traces[0].UserName != "Alice" || traces[0].UserKind != "user" {
		t.Errorf("trace user = name:%q kind:%q, want Alice/user", traces[0].UserName, traces[0].UserKind)
	}

	// 5) User stats fold the two Alice ids (the span above via email + this one
	//    by id) into a single identity row.
	if _, err := s.db.Exec(`INSERT INTO genai_spans
		(received_at, time, duration, trace_id, span_id, name, status, user_id, user_email, input_tokens, output_tokens, cost)
		VALUES (?, ?, 1.0, 'trace-2', 'span-2', 'root', 'ok', 'u-guid-1', '', 30, 15, 0.5)`,
		ts, ts); err != nil {
		t.Fatalf("insert span 2: %v", err)
	}
	stats, err := s.QueryUserStats(ctx, from, to, 0, Filter{})
	if err != nil {
		t.Fatalf("user stats: %v", err)
	}
	if len(stats) != 1 || stats[0].ID != "alice-obj" {
		t.Fatalf("user stats = %+v, want one folded Alice row", stats)
	}
	if stats[0].Name != "Alice" || stats[0].Requests != 2 || stats[0].Tokens != 60 {
		t.Errorf("Alice stats = %+v, want name=Alice requests=2 tokens=60", stats[0])
	}
}
