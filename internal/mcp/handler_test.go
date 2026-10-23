package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"insights/internal/store"
	"insights/pkg/directory"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testDirectory []directory.Record

func (d testDirectory) Lookup(key string) (directory.Identity, bool) {
	for _, r := range d {
		if r.Alias == key {
			return directory.Identity{ID: r.ID, Name: r.Name, Department: r.Department, Location: r.Location}, true
		}
	}
	return directory.Identity{}, false
}
func (d testDirectory) Records() iter.Seq[directory.Record] {
	return func(yield func(directory.Record) bool) {
		for _, r := range d {
			if !yield(r) {
				return
			}
		}
	}
}

func fixture(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "mcp.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "256MB")
	t.Setenv("INSIGHTS_DB_THREADS", "2")
	s, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetDirectory(testDirectory{
		{Alias: "alice", ID: "alice", Name: "Alice", Department: "Engineering", Location: "Zurich"},
		{Alias: "alice@example.com", ID: "alice", Name: "Alice", Department: "Engineering", Location: "Zurich"},
		{Alias: "bob", ID: "bob", Name: "Bob", Department: "Sales", Location: "London"},
	})
	ctx := context.Background()
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Add(-time.Hour)
	spans := []store.SpanRow{
		{Time: ts, ReceivedAt: ts, TraceID: "a", SpanID: "a", UserID: "alice@example.com", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 1000000, OutputTokens: 100000, CacheRead: 200000, Reasoning: 10000},
		{Time: ts, ReceivedAt: ts, TraceID: "b", SpanID: "b", UserID: "bob", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 2000000},
		{Time: ts, ReceivedAt: ts, TraceID: "c", SpanID: "c", UserID: "alice", AppID: "other", ProviderName: "unknown", RequestModel: "unpriced", InputTokens: 500},
		{Time: ts, ReceivedAt: ts, TraceID: "d", SpanID: "d", UserID: "alice", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 9999999, Attributes: map[string]string{"wingman.telemetry.source": "chat"}},
	}
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	metrics := []store.GenAIMetricRow{}
	for _, owner := range []struct{ session, user string }{{"conv-a", "alice@example.com"}, {"conv-b", "bob"}} {
		metrics = append(metrics, store.GenAIMetricRow{Time: ts, ReceivedAt: ts, SessionID: owner.session, EndUserID: owner.user, AppID: "chat", MetricName: "gen_ai.client.token.usage", TokenType: "input", Count: 1, Sum: 100})
	}
	for _, c := range []struct {
		session, kind, id, matched string
		count                      int64
	}{
		{"conv-a", "category", "software_development", "true", 4},
		{"conv-a", "category", "legal", "true", 2},
		{"conv-b", "category", "legal", "true", 3},
		{"conv-b", "category", "other", "false", 100},
		{"conv-a", "risk", "pii", "true", 99},
	} {
		metrics = append(metrics, store.GenAIMetricRow{Time: ts, ReceivedAt: ts, SessionID: c.session, AppID: "chat", MetricName: "wingman.classification.score", Count: c.count, Sum: float64(c.count) * 0.9, Attributes: map[string]string{
			"wingman.classification.kind": c.kind, "wingman.classification.id": c.id, "wingman.classification.matched": c.matched, "wingman.classification.threshold": "0.6",
		}})
	}
	if err := s.InsertGenAIMetrics(ctx, metrics); err != nil {
		t.Fatal(err)
	}
	return s
}

func testToken(user string, groups ...string) string {
	b, _ := json.Marshal(map[string]any{"oid": user, "groups": groups})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + "."
}

type tokenTransport struct{ token atomic.Value }

func (a *tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token.Load().(string))
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, s *store.Store, user string, groups ...string) (*mcp.ClientSession, *tokenTransport) {
	t.Helper()
	// Mount as main.go does, exercising INSIGHTS_BASE_PATH-style stripping.
	app := http.NewServeMux()
	app.Handle("/mcp", NewHandler(s, "admins"))
	mux := http.NewServeMux()
	mux.Handle("/insights/", http.StripPrefix("/insights", app))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	a := &tokenTransport{}
	a.token.Store(testToken(user, groups...))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/insights/mcp", HTTPClient: &http.Client{Transport: a}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, a
}

func call[T any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("%s: %+v", name, r.Content)
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHTTPDiscoveryAndPersonalScoping(t *testing.T) {
	s := fixture(t)
	client, transport := connect(t, s, "alice")
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 3 {
		t.Fatalf("personal tools = %d", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("not read only: %s", tool.Name)
		}
		schema := tool.InputSchema.(map[string]any)
		props := schema["properties"].(map[string]any)
		for _, key := range []string{"applications", "users", "departments", "locations", "providers", "models", "from", "to"} {
			p, ok := props[key].(map[string]any)
			if !ok || p["description"] == "" {
				t.Fatalf("missing documented %s in %s: %+v", key, tool.Name, schema)
			}
		}
	}
	args := map[string]any{"users": []string{"bob"}, "interval": "day"}
	u := call[Response[UsageData]](t, client, "get_usage", args)
	if u.Query.Scope != "personal" || !reflect.DeepEqual(u.Query.Filters.Users, []string{"alice"}) {
		t.Fatalf("scope: %+v", u.Query)
	}
	from, _ := time.Parse(time.RFC3339Nano, u.Query.From)
	to, _ := time.Parse(time.RFC3339Nano, u.Query.To)
	want, err := s.QueryUsageTotals(context.Background(), from, to, store.Filter{User: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if u.Data.Cost != want.Cost || u.Data.Tokens != want.Tokens {
		t.Fatalf("usage=%+v, store=%+v", u.Data, want)
	}
	if u.Data.Tokens.Input != 800500 || u.Data.Tokens.Cached != 200000 || u.Data.Tokens.Output != 100000 || u.Data.Tokens.Reasoning != 10000 || u.Data.Tokens.Priced {
		t.Fatalf("accounting: %+v", u.Data.Tokens)
	}
	if len(u.Data.Buckets) != 2 || len(u.Query.Warnings) == 0 {
		t.Fatalf("missing trends/coverage: %+v", u)
	}
	var sum float64
	for _, b := range u.Data.Buckets {
		sum += b.Cost
	}
	if math.Abs(sum-u.Data.Cost) > 1e-9 {
		t.Fatal("trend/totals disagree")
	}
	for _, group := range []string{"model", "application", "department", "location", "user"} {
		cost := call[Response[CostData]](t, client, "get_cost_breakdown", map[string]any{"group_by": group, "users": []string{"bob"}})
		if cost.Data.TotalCost != want.Cost || cost.Data.UnpricedTokens != 500 {
			t.Fatalf("%s leaked other user: %+v", group, cost)
		}
	}
	if r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_categories"}); err == nil && !r.IsError {
		t.Fatal("personal caller accessed categories")
	}
	// Same logical client, next HTTP request: identity and role must be re-read.
	transport.token.Store(testToken("bob", "admins"))
	adminUsage := call[Response[UsageData]](t, client, "get_usage", nil)
	if adminUsage.Query.Scope != "organization" || adminUsage.Data.Cost <= u.Data.Cost {
		t.Fatal("admin request did not use current identity")
	}
	transport.token.Store(testToken("bob"))
	bob := call[Response[UsageData]](t, client, "get_usage", nil)
	if !reflect.DeepEqual(bob.Query.Filters.Users, []string{"bob"}) || bob.Data.Tokens.Input != 2000000 || !bob.Data.Tokens.Priced {
		t.Fatalf("request identity leaked: %+v", bob)
	}
	for _, name := range []string{"get_categories", "get_category_trends", "get_adoption"} {
		if r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name}); err == nil && !r.IsError {
			t.Fatalf("downgraded caller retained access to %s", name)
		}
	}
}

func TestAdminCategoriesAndCosts(t *testing.T) {
	s := fixture(t)
	client, _ := connect(t, s, "admin", "admins")
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 6 {
		t.Fatalf("admin tools=%d", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, "categor") {
			props := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
			if props["models"] != nil || props["providers"] != nil {
				t.Fatal("classification exposes invalid model/provider filters")
			}
		}
	}
	c := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"limit": 1})
	if c.Data.TotalPrompts != 9 || c.TotalRows != 2 || !c.Truncated || len(c.Data.Rows) != 1 || c.Data.Rows[0].Category != "legal" || c.Data.Rows[0].Share != 5.0/9 {
		t.Fatalf("category counts/shares: %+v", c)
	}
	d := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"group_by": "department"})
	shares := map[string]float64{}
	for _, r := range d.Data.Rows {
		shares[r.Department] += r.Share
		if r.Category == "software_development" && (r.Label != "Software Development" || r.Share != 4.0/6 || r.Prompts != 4) {
			t.Fatalf("department share=%+v", r)
		}
	}
	if d.Data.TotalPrompts != 9 || shares["Engineering"] != 1 || shares["Sales"] != 1 {
		t.Fatalf("department shares: %+v", d)
	}
	for _, iv := range []string{"hour", "day"} {
		trend := call[Response[[]CategoryPoint]](t, client, "get_category_trends", map[string]any{"interval": iv})
		var prompts int64
		for _, p := range trend.Data {
			prompts += p.Prompts
		}
		if prompts != 9 || len(trend.Data) != 2 || trend.Query.Interval != iv {
			t.Fatalf("trend=%+v", trend)
		}
	}
	filtered := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"departments": []string{"engineering"}, "applications": []string{"chat"}, "locations": []string{"zurich"}})
	if filtered.Data.TotalPrompts != 6 {
		t.Fatalf("category filters: %+v", filtered)
	}
	usage := call[Response[UsageData]](t, client, "get_usage", map[string]any{"models": []string{"gpt-5.1"}, "providers": []string{"openai"}, "users": []string{"alice"}})
	if usage.Data.Tokens.Input != 800000 || !usage.Data.Tokens.Priced {
		t.Fatalf("usage filters: %+v", usage)
	}
	cost := call[Response[CostData]](t, client, "get_cost_breakdown", map[string]any{"limit": 1})
	if !cost.Truncated || cost.TotalRows != 2 || cost.Data.UnpricedTokens != 500 || len(cost.Data.Rows) != 1 {
		t.Fatalf("cost limit/coverage: %+v", cost)
	}
	if cost.Data.TotalCost != cost.Data.Rows[0].TotalCost {
		t.Fatal("unexpected priced totals")
	}
}

func TestEmptyDataAndInvalidInputs(t *testing.T) {
	client, _ := connect(t, fixture(t), "admin", "admins")
	for _, name := range []string{"get_usage", "get_cost_breakdown", "get_categories", "get_category_trends"} {
		r := call[Response[json.RawMessage]](t, client, name, map[string]any{"applications": []string{"does-not-exist"}})
		if r.TotalRows != 0 || r.Truncated || len(r.Query.Warnings) == 0 {
			t.Fatalf("empty result: %+v", r)
		}
		if strings.Contains(string(r.Data), `"buckets":null`) || strings.Contains(string(r.Data), `"rows":null`) {
			t.Fatalf("empty arrays must be []: %s", r.Data)
		}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"get_usage", map[string]any{"from": "nonsense"}},
		{"get_usage", map[string]any{"to": "nonsense"}},
		{"get_usage", map[string]any{"from": "2026-10-03T00:00:00Z", "to": "2026-10-01T00:00:00Z"}},
		{"get_usage", map[string]any{"from": "2020-01-01T00:00:00Z", "to": "2026-01-01T00:00:00Z"}},
		{"get_usage", map[string]any{"interval": "minute"}},
		{"get_usage", map[string]any{"models": []string{""}}},
		{"get_cost_breakdown", map[string]any{"group_by": "sql"}},
		{"get_cost_breakdown", map[string]any{"limit": -1}},
		{"get_categories", map[string]any{"limit": 101}},
		{"get_categories", map[string]any{"group_by": "model"}},
		{"get_categories", map[string]any{"models": []string{"gpt-5.1"}}},
		{"get_category_trends", map[string]any{"interval": "minute"}},
	} {
		t.Run(fmt.Sprint(tc.name, tc.args), func(t *testing.T) {
			r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err == nil && !r.IsError {
				t.Fatalf("accepted invalid arguments: %+v", tc)
			}
		})
	}
}

func TestTrustedTokenBoundary(t *testing.T) {
	// nil store is safe: initialization registers tools but executes no queries.
	h := NewHandler(nil, "admins")
	for _, header := range []string{"", "Bearer garbage", "Bearer a.e30.", "Bearer a.@@@.", "Basic whatever"} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", header)
		r.Header.Set("X-Forwarded-User", "alice")
		r.Header.Set("X-Forwarded-Groups", "admins")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("invalid token accepted: %q status=%d", header, w.Code)
		}
	}
	p, ok := tokenPrincipal("Bearer "+testToken("alice", "admins"), "")
	if !ok || p.admin {
		t.Fatal("unset admin group granted access")
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"subject","groups":["admins"],"exp":1}`))
	p, ok = tokenPrincipal("Bearer a."+payload+".", "admins")
	if !ok || p.user != "subject" || !p.admin {
		t.Fatal("trusted subject fallback failed")
	}
	r := httptest.NewRequest("POST", "http://insights.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+testToken("alice"))
	r.Header.Set("Origin", "https://unrelated.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin request status=%d", w.Code)
	}
}

func TestDevFallbackOnlyWhenEnabled(t *testing.T) {
	// nil store is safe: initialization registers tools but executes no queries.
	noFallback := NewHandler(nil, "admins")
	r := httptest.NewRequest("POST", "/mcp", nil)
	w := httptest.NewRecorder()
	noFallback.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("missing token accepted without the dev option: status=%d", w.Code)
	}

	// The option replaces the 401 with a fixed identity, so an anonymous caller
	// reaches the transport instead of the auth wall.
	h := NewHandler(nil, "admins", WithDevFallback("dev@example.com", true))
	r = httptest.NewRequest("POST", "/mcp", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 401 {
		t.Fatal("dev fallback did not accept an unauthenticated caller")
	}

	if p := devPrincipal(t, "  ", false); p.user != "dev" {
		t.Fatalf("blank dev user not defaulted: %q", p.user)
	}
	if p := devPrincipal(t, "alice", true); p.user != "alice" || !p.admin {
		t.Fatalf("dev identity not honored: %+v", p)
	}
}

// devPrincipal resolves the identity an Option would install.
func devPrincipal(t *testing.T, user string, admin bool) principal {
	t.Helper()
	var cfg handlerConfig
	WithDevFallback(user, admin)(&cfg)
	if cfg.dev == nil {
		t.Fatal("option installed no principal")
	}
	return *cfg.dev
}

func TestCORSAllowlist(t *testing.T) {
	// nil store is safe: initialization registers tools but executes no queries.
	const origin = "https://client.example"

	// Default: cross-origin browser POSTs stay rejected and no CORS is offered.
	closed := NewHandler(nil, "admins")
	r := httptest.NewRequest("POST", "http://insights.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+testToken("alice"))
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	closed.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("default allowed cross-origin: status=%d acao=%q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}

	// An explicit allowlist answers preflights without credentials and echoes
	// the origin, but still refuses origins it was not given.
	listed := NewHandler(nil, "admins", WithAllowedOrigins(origin))
	r = httptest.NewRequest("OPTIONS", "http://insights.example/mcp", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	w = httptest.NewRecorder()
	listed.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("preflight status=%d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("preflight origin=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "authorization, content-type" {
		t.Fatalf("preflight headers=%q", got)
	}
	if !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "Mcp-Session-Id") {
		t.Fatal("session header not exposed to browser clients")
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Fatal("per-origin response not marked Vary: Origin")
	}

	r = httptest.NewRequest("POST", "http://insights.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+testToken("alice"))
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	listed.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin admitted: status=%d", w.Code)
	}

	// Wildcard admits any origin and drops the cross-origin protection.
	open := NewHandler(nil, "admins", WithAllowedOrigins("*"))
	r = httptest.NewRequest("POST", "http://insights.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+testToken("alice"))
	r.Header.Set("Origin", "https://anything.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	open.ServeHTTP(w, r)
	if w.Code == 403 {
		t.Fatal("wildcard still rejected a cross-origin request")
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("wildcard acao=%q", got)
	}

	// Preflights must not require a token, or the browser reports a CORS error.
	r = httptest.NewRequest("OPTIONS", "http://insights.example/mcp", nil)
	r.Header.Set("Origin", "https://anything.example")
	w = httptest.NewRecorder()
	open.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("unauthenticated preflight status=%d", w.Code)
	}
}

func TestLimitsCancellationAndErrorRedaction(t *testing.T) {
	s := fixture(t)
	client, _ := connect(t, s, "admin", "admins")
	// Generate >2000 grouped points in a valid 90-day range. These rows have
	// no price, keeping the test independent of the pricing catalog.
	now := time.Now().UTC().Truncate(time.Hour)
	spans := make([]store.SpanRow, 2001)
	metrics := make([]store.GenAIMetricRow, 2001)
	for i := range spans {
		ts := now.Add(-time.Duration(i) * time.Hour)
		spans[i] = store.SpanRow{Time: ts, ReceivedAt: ts, UserID: "alice", AppID: "large", RequestModel: "unknown", InputTokens: 1}
		metrics[i] = store.GenAIMetricRow{Time: ts, ReceivedAt: ts, AppID: "large", MetricName: "wingman.classification.score", Count: 1, Sum: 0.9, Attributes: map[string]string{"wingman.classification.kind": "category", "wingman.classification.id": "legal", "wingman.classification.matched": "true"}}
	}
	if err := s.InsertSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertGenAIMetrics(context.Background(), metrics); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"from": now.Add(-2001 * time.Hour).Format(time.RFC3339), "to": now.Add(time.Minute).Format(time.RFC3339), "applications": []string{"large"}, "interval": "hour"}
	for _, name := range []string{"get_usage", "get_category_trends"} {
		r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !r.IsError || !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "2000") {
			t.Fatalf("%s point limit: %+v %v", name, r, err)
		}
	}
	args["interval"] = "day"
	call[Response[UsageData]](t, client, "get_usage", args)
	call[Response[[]CategoryPoint]](t, client, "get_category_trends", args)

	// Cancellation reaches the query layer, without a detached background query.
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), principalKey{}, principal{user: "alice"}))
	cancel()
	_, err := (tools{store: s}).usage(ctx, UsageInput{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_usage"})
	if err != nil || !r.IsError || r.Content[0].(*mcp.TextContent).Text != "internal query error" {
		t.Fatalf("database error disclosure: %+v %v", r, err)
	}
}
