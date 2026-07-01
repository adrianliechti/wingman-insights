package store

import (
	"context"
	"iter"
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
		return directory.Identity{ID: "alice-obj", Name: "Alice", Kind: directory.KindUser, Department: "Eng", Location: "Zurich"}, true
	case "sp-guid", "app-client":
		return directory.Identity{ID: "app-client", Name: "Worker", Kind: directory.KindApplication}, true
	}
	return directory.Identity{}, false
}

func (mappingStub) Records() iter.Seq[directory.Record] {
	return func(yield func(directory.Record) bool) {
		recs := []directory.Record{
			{Alias: "u-guid-1", ID: "alice-obj", Name: "Alice", Kind: directory.KindUser, Department: "Eng", Location: "Zurich"},
			{Alias: "alice@corp.com", ID: "alice-obj", Name: "Alice", Kind: directory.KindUser, Department: "Eng", Location: "Zurich"},
			{Alias: "alice-obj", ID: "alice-obj", Name: "Alice", Kind: directory.KindUser, Department: "Eng", Location: "Zurich"},
			{Alias: "sp-guid", ID: "app-client", Name: "Worker", Kind: directory.KindApplication},
			{Alias: "app-client", ID: "app-client", Name: "Worker", Kind: directory.KindApplication},
		}
		for _, r := range recs {
			if !yield(r) {
				return
			}
		}
	}
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
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
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
	top, err = s.QueryTopConsumers(ctx, from, to, 10, Filter{User: "alice-obj"})
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

// TestDepartmentFilterAndCost covers the directory-group filters and the
// department cost grouping: a department/office-location filter expands to its
// members' raw ids, and cost rows carry/aggregate the resolved department.
func TestDepartmentFilterAndCost(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ts := time.Now().UTC()
	// Metrics drive the filter dropdown (distinct users -> resolved departments).
	metric := func(id, email string) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO genai_metrics
			(received_at, time, metric_name, token_type, enduser_id, enduser_email, count, sum)
			VALUES (?, ?, 'gen_ai.client.token.usage', 'input', ?, ?, 1, 1)`,
			ts, ts, id, email); err != nil {
			t.Fatalf("insert metric: %v", err)
		}
	}
	// Spans drive the cost breakdown. These rows are inserted directly via raw
	// SQL (bypassing InsertSpans, so cost/priced are never set — NULL), so
	// assertions below use token counts rather than the materialized cost
	// columns this test doesn't populate.
	span := func(id, email string, in, out int) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO genai_spans
			(received_at, time, duration, trace_id, span_id, name, status, user_id, user_email, provider_name, request_model,
			 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost)
			VALUES (?, ?, 1.0, ?, ?, 'root', 'ok', ?, ?, 'anthropic', 'claude', ?, ?, 0, 0, 0, 0)`,
			ts, ts, id+"-t", id+"-s", id, email, in, out); err != nil {
			t.Fatalf("insert span: %v", err)
		}
	}
	// Alice resolves to dept "Eng" (one row by id, one by email fallback); the
	// app and the ghost have no department.
	for _, r := range []struct {
		id, email string
		in, out   int
	}{
		{"u-guid-1", "", 100, 50},
		{"other-guid", "alice@corp.com", 200, 20},
		{"sp-guid", "", 500, 0},
		{"ghost", "", 10, 0},
	} {
		metric(r.id, r.email)
		span(r.id, r.email, r.in, r.out)
	}

	s.SetDirectory(mappingStub{})
	ctx := context.Background()
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	// Options expose the department/location seen in range.
	opts, err := s.QueryFilterOptions(ctx, from, to)
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	if strings.Join(opts.Departments, ",") != "Eng" {
		t.Errorf("departments = %v, want [Eng]", opts.Departments)
	}
	if strings.Join(opts.Locations, ",") != "Zurich" {
		t.Errorf("locations = %v, want [Zurich]", opts.Locations)
	}

	// Department grouping: Alice's two rows fold under "Eng"; everything else
	// lands in the empty (Unknown) bucket.
	rows, err := s.QueryCostBreakdown(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("cost breakdown: %v", err)
	}
	byDept := map[string]CostRow{}
	for _, r := range AggregateCostsByDepartment(rows) {
		byDept[r.Department] = r
	}
	if eng := byDept["Eng"]; eng.InputTokens != 300 || eng.OutputTokens != 70 { // 100+200 / 50+20
		t.Errorf("Eng tokens = in:%v out:%v, want 300/70", eng.InputTokens, eng.OutputTokens)
	}
	if unknown := byDept[""]; unknown.InputTokens != 510 { // app 500 + ghost 10
		t.Errorf("Unknown input tokens = %v, want 510", unknown.InputTokens)
	}

	// Department filter matches via the directory table (incl. the email-only row).
	rows, err = s.QueryCostBreakdown(ctx, from, to, Filter{Department: "Eng"})
	if err != nil {
		t.Fatalf("filtered cost breakdown: %v", err)
	}
	var in float64
	for _, r := range rows {
		in += r.InputTokens
	}
	if in != 300 {
		t.Errorf("Eng-filtered input tokens = %v, want 300 (only Alice's rows)", in)
	}

	// An unknown department matches nothing rather than everything.
	rows, err = s.QueryCostBreakdown(ctx, from, to, Filter{Department: "Nonesuch"})
	if err != nil {
		t.Fatalf("unknown-dept breakdown: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("unknown department matched %d rows, want 0", len(rows))
	}

	// A parent code "En" matches nothing in direct mode, but in prefix mode rolls
	// up the "Eng" subtree (Alice).
	rows, _ = s.QueryCostBreakdown(ctx, from, to, Filter{Department: "En"})
	if len(rows) != 0 {
		t.Errorf("direct 'En' matched %d rows, want 0", len(rows))
	}
	rows, err = s.QueryCostBreakdown(ctx, from, to, Filter{Department: "En", DeptPrefix: true})
	if err != nil {
		t.Fatalf("prefix-dept breakdown: %v", err)
	}
	in = 0
	for _, r := range rows {
		in += r.InputTokens
	}
	if in != 300 {
		t.Errorf("prefix 'En' input tokens = %v, want 300 (Eng subtree)", in)
	}
}

// TestNoDirectory pins the pass-through behavior when no directory is configured:
// the directory table is empty, so every id resolves to itself (no name/kind),
// nothing folds, raw-id filtering still works, and group filters match nothing.
func TestNoDirectory(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	// Deliberately no SetDirectory / SyncDirectory.

	ts := time.Now().UTC()
	for _, u := range []struct {
		id  string
		in  int
		sum float64
	}{{"u1", 100, 100}, {"u2", 50, 50}} {
		if _, err := s.db.Exec(`INSERT INTO genai_metrics
			(received_at, time, metric_name, token_type, enduser_id, count, sum)
			VALUES (?, ?, 'gen_ai.client.token.usage', 'input', ?, 1, ?)`,
			ts, ts, u.id, u.sum); err != nil {
			t.Fatalf("insert metric: %v", err)
		}
		if _, err := s.db.Exec(`INSERT INTO genai_spans
			(received_at, time, duration, trace_id, span_id, name, status, user_id, provider_name, request_model,
			 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost)
			VALUES (?, ?, 1.0, ?, ?, 'root', 'ok', ?, 'anthropic', 'claude', ?, 0, 0, 0, 0, 0)`,
			ts, ts, u.id+"-t", u.id+"-s", u.id, u.in); err != nil {
			t.Fatalf("insert span: %v", err)
		}
	}

	ctx := context.Background()
	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	// Cost breakdown passes ids through with no name/kind, one row per raw id.
	rows, err := s.QueryCostBreakdown(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("cost: %v", err)
	}
	byID := map[string]CostRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if len(byID) != 2 {
		t.Fatalf("cost rows = %d, want 2 (one per raw id)", len(byID))
	}
	if r := byID["u1"]; r.Name != "" || r.Kind != "" || r.InputTokens != 100 {
		t.Errorf("u1 row = %+v, want raw passthrough (no name/kind), 100 input", r)
	}

	// Raw-id user filter works without a directory (direct equality fallback).
	rows, _ = s.QueryCostBreakdown(ctx, from, to, Filter{User: "u1"})
	if len(rows) != 1 || rows[0].ID != "u1" {
		t.Errorf("user filter = %+v, want only u1", rows)
	}

	// A department filter with no directory matches nothing (empty table).
	if rows, _ = s.QueryCostBreakdown(ctx, from, to, Filter{Department: "X"}); len(rows) != 0 {
		t.Errorf("dept filter w/o directory matched %d rows, want 0", len(rows))
	}

	// Leaderboard and distinct-user count see two raw ids, unfolded.
	top, err := s.QueryTopConsumers(ctx, from, to, 10, Filter{})
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(top) != 2 || top[0].ID != "u1" {
		t.Errorf("top consumers = %+v, want [u1, u2]", top)
	}
	au, err := s.QueryActiveUsers(ctx, to, Filter{})
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if au.DAU != 2 {
		t.Errorf("DAU = %d, want 2", au.DAU)
	}

	// Filter options list raw ids with no name, and no group dimensions.
	opts, err := s.QueryFilterOptions(ctx, from, to)
	if err != nil {
		t.Fatalf("opts: %v", err)
	}
	if len(opts.Users) != 2 || opts.Users[0].Name != "" {
		t.Errorf("users = %+v, want two un-named raw ids", opts.Users)
	}
	if len(opts.Departments) != 0 || len(opts.Locations) != 0 {
		t.Errorf("departments=%v locations=%v, want both empty", opts.Departments, opts.Locations)
	}
}

// TestDepartmentPrefixEscapesWildcards guards the LIKE escaping: a department
// code with an underscore must not let '_' act as a single-char wildcard, so a
// prefix of "a_" matches "a_b" but not "axb".
func TestDepartmentPrefixEscapesWildcards(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Populate the directory table directly (the format SyncDirectory produces).
	if _, err := s.db.Exec(`INSERT INTO directory (alias, id, name, kind, department) VALUES
		('ua', 'ua', 'A', 'user', 'a_b'),
		('ub', 'ub', 'B', 'user', 'axb')`); err != nil {
		t.Fatalf("seed directory: %v", err)
	}
	ts := time.Now().UTC()
	for _, id := range []string{"ua", "ub"} {
		if _, err := s.db.Exec(`INSERT INTO genai_spans
			(received_at, time, duration, trace_id, span_id, name, status, user_id, provider_name, request_model,
			 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost)
			VALUES (?, ?, 1.0, ?, ?, 'root', 'ok', ?, 'anthropic', 'claude', 100, 0, 0, 0, 0, 0)`,
			ts, ts, id+"-t", id+"-s", id); err != nil {
			t.Fatalf("insert span: %v", err)
		}
	}
	ctx := context.Background()
	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	rows, err := s.QueryCostBreakdown(ctx, from, to, Filter{Department: "a_", DeptPrefix: true})
	if err != nil {
		t.Fatalf("prefix breakdown: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "ua" {
		t.Errorf("prefix 'a_' matched %+v, want only ua (a_b) — '_' must be literal", rows)
	}
}
