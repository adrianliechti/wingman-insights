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
		return directory.Identity{ID: "alice-obj", Name: "Alice", Kind: directory.KindUser, Department: "Eng", Location: "Zurich"}, true
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

func (s mappingStub) Members(attr directory.Attribute, value string) []string {
	switch attr {
	case directory.AttrDepartment:
		if strings.EqualFold(value, "Eng") {
			return s.Aliases("alice-obj")
		}
	case directory.AttrLocation:
		if strings.EqualFold(value, "Zurich") {
			return s.Aliases("alice-obj")
		}
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
	// Spans drive the cost breakdown. Costs are recomputed from pricing (the
	// stored cost column is ignored), so assertions below use token counts.
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

	// Department filter expands to Alice's ids (incl. the email-only row).
	f := s.ExpandUserFilter(Filter{Department: "Eng"})
	rows, err = s.QueryCostBreakdown(ctx, from, to, f)
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
	f = s.ExpandUserFilter(Filter{Department: "Nonesuch"})
	rows, err = s.QueryCostBreakdown(ctx, from, to, f)
	if err != nil {
		t.Fatalf("unknown-dept breakdown: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("unknown department matched %d rows, want 0", len(rows))
	}
}
