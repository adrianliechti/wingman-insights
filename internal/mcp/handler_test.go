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
	"slices"
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
	app.Handle("/mcp", NewHandler(s, unverified("admins")))
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

type resp[T any] struct {
	Query    QueryInfo `json:"query"`
	Warnings []string  `json:"warnings"`
	Data     T         `json:"data"`
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

func TestAdminGateAndDiscovery(t *testing.T) {
	s := fixture(t)
	post := func(adminGroup, token string) int {
		r := httptest.NewRequest("POST", "/mcp", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		NewHandler(s, unverified(adminGroup)).ServeHTTP(w, r)
		return w.Code
	}
	if got := post("admins", testToken("alice")); got != 403 {
		t.Fatalf("token without the admin group: status=%d", got)
	}
	if got := post("admins", testToken("alice", "other")); got != 403 {
		t.Fatalf("token with another group: status=%d", got)
	}
	if got := post("", testToken("alice", "admins")); got != 403 {
		t.Fatalf("unset INSIGHTS_ADMIN_GROUP admitted a caller: status=%d", got)
	}
	if got := post("admins", ""); got != 401 {
		t.Fatalf("missing token: status=%d", got)
	}
	if got := post("admins", testToken("alice", "admins")); got == 401 || got == 403 {
		t.Fatalf("admin refused: status=%d", got)
	}

	client, transport := connect(t, s, "admin", "admins")
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 3 {
		t.Fatalf("tools = %d", len(listed.Tools))
	}
	if info := client.InitializeResult().ServerInfo; len(info.Icons) != 2 {
		t.Fatalf("server icons: %+v", info.Icons)
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("not read only: %s", tool.Name)
		}
		for _, icon := range tool.Icons {
			svg, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(icon.Source, "data:image/svg+xml;base64,"))
			if err != nil || !strings.HasPrefix(string(svg), "<svg") || strings.Contains(string(svg), "currentColor") {
				t.Fatalf("%s icon: %v %s", tool.Name, err, svg)
			}
		}
		if len(tool.Icons) != 2 {
			t.Fatalf("%s icons: %+v", tool.Name, tool.Icons)
		}
		schema := tool.InputSchema.(map[string]any)
		props := schema["properties"].(map[string]any)
		keys := []string{"applications", "users", "departments", "locations", "from", "to"}
		if tool.Name == "get_usage" {
			keys = append(keys, "providers", "models")
		}
		for _, key := range keys {
			p, ok := props[key].(map[string]any)
			if !ok || p["description"] == "" {
				t.Fatalf("missing documented %s in %s: %+v", key, tool.Name, schema)
			}
		}
	}

	all := call[Response[UsageData]](t, client, "get_usage", map[string]any{"interval": "day"})
	alice := call[Response[UsageData]](t, client, "get_usage", map[string]any{"users": []string{"alice"}, "interval": "day"})
	if !reflect.DeepEqual(alice.Query.Filters.Users, []string{"alice"}) || all.Data.Totals.CostUSD <= alice.Data.Totals.CostUSD {
		t.Fatalf("users filter: all=%+v alice=%+v", all.Query.Filters, alice.Query.Filters)
	}
	from, _ := time.Parse(time.RFC3339Nano, alice.Query.From)
	to, _ := time.Parse(time.RFC3339Nano, alice.Query.To)
	want, err := s.QueryUsageTotals(context.Background(), from, to, store.Filter{User: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*alice.Data.Totals, newUsage(want)) {
		t.Fatalf("usage=%+v, store=%+v", alice.Data.Totals, want)
	}
	got := *alice.Data.Totals
	if got.InputTokens != 1000500 || got.CacheReadTokens != 200000 || got.OutputTokens != 100000 || got.ReasoningTokens != 10000 || got.TotalTokens != 1100500 || got.UnpricedTokens != 500 {
		t.Fatalf("accounting: %+v", got)
	}
	if len(alice.Data.Points) != 2 || !slices.Contains(alice.Warnings, unpricedWarning) {
		t.Fatalf("missing trends/coverage: %+v", alice)
	}
	var sum float64
	for _, p := range alice.Data.Points {
		sum += p.CostUSD
	}
	if math.Abs(sum-alice.Data.Totals.CostUSD) > 1e-6 {
		t.Fatal("trend/totals disagree")
	}
	for _, group := range []string{"model", "application", "department", "location", "user"} {
		g := call[Response[UsageData]](t, client, "get_usage", map[string]any{"group_by": group, "users": []string{"alice"}})
		var tokens int64
		for _, r := range g.Data.Groups {
			tokens += r.TotalTokens
		}
		if !reflect.DeepEqual(*g.Data.Totals, newUsage(want)) || tokens != 1100500 || g.Query.GroupBy != group {
			t.Fatalf("%s ignored the user filter: %+v", group, g)
		}
	}

	// Same logical client, next HTTP request: the role must be re-read.
	transport.token.Store(testToken("admin"))
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_usage"}); err == nil {
		t.Fatal("downgraded caller retained access")
	}
}

// Some MCP clients refuse boolean subschemas and "type" arrays, so the
// inferred input and output schemas must use plain typed objects throughout.
func TestOutputSchemasArePortable(t *testing.T) {
	client, _ := connect(t, fixture(t), "admin", "admins")
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var check func(tool, path string, v any)
	check = func(tool, path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s: non-object schema at %s: %v", tool, path, v)
		}
		if _, ok := m["type"].(string); !ok {
			t.Fatalf("%s: missing or non-string type at %s: %v", tool, path, m["type"])
		}
		if props, ok := m["properties"].(map[string]any); ok {
			for k, p := range props {
				check(tool, path+"."+k, p)
			}
		}
		if items, ok := m["items"]; ok {
			check(tool, path+"[]", items)
		}
	}
	for _, tool := range listed.Tools {
		for _, s := range []any{tool.InputSchema, tool.OutputSchema} {
			b, _ := json.Marshal(s)
			var schema any
			json.Unmarshal(b, &schema)
			check(tool.Name, "$", schema)
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
	if len(listed.Tools) != 3 {
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
	if c.Data.TotalPrompts != 9 || c.Data.TotalCategories != 2 || !c.Data.Truncated || len(c.Data.Categories) != 1 || c.Data.Categories[0].Category != "legal" || *c.Data.Categories[0].SharePercent != 55.56 || c.Data.Categories[0].Department != nil {
		t.Fatalf("category counts/shares: %+v", c)
	}
	d := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"group_by": "department"})
	shares := map[string]float64{}
	for _, r := range d.Data.Categories {
		shares[*r.Department] += *r.SharePercent
		if r.Category == "software_development" && (*r.SharePercent != 66.67 || r.Prompts != 4) {
			t.Fatalf("department share=%+v", r)
		}
	}
	if d.Data.TotalPrompts != 9 || math.Abs(shares["Engineering"]-100) > 0.02 || shares["Sales"] != 100 {
		t.Fatalf("department shares: %+v", d)
	}
	for _, iv := range []string{"hour", "day"} {
		trend := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"interval": iv})
		var prompts int64
		var share float64
		for _, p := range trend.Data.Points {
			prompts += p.Prompts
			share += *p.SharePercent
		}
		if prompts != 9 || trend.Data.TotalPrompts != 9 || len(trend.Data.Points) != 2 || trend.Query.Interval != iv || math.Abs(share-100) > 0.02 {
			t.Fatalf("trend=%+v", trend)
		}
	}
	filtered := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"departments": []string{"engineering"}, "applications": []string{"chat"}, "locations": []string{"zurich"}})
	if filtered.Data.TotalPrompts != 6 {
		t.Fatalf("category filters: %+v", filtered)
	}
	usage := call[Response[UsageData]](t, client, "get_usage", map[string]any{"models": []string{"gpt-5.1"}, "providers": []string{"openai"}, "users": []string{"alice"}})
	if usage.Data.Totals.InputTokens != 1000000 || usage.Data.Totals.UnpricedTokens != 0 || len(usage.Warnings) != 0 {
		t.Fatalf("usage filters: %+v", usage)
	}
	cost := call[Response[UsageData]](t, client, "get_usage", map[string]any{"group_by": "model", "limit": 1})
	if !cost.Data.Truncated || cost.Data.TotalGroups != 2 || cost.Data.Totals.UnpricedTokens != 500 || len(cost.Data.Groups) != 1 {
		t.Fatalf("cost limit/coverage: %+v", cost)
	}
	if cost.Data.Totals.CostUSD != cost.Data.Groups[0].CostUSD || cost.Data.Groups[0].ID != "openai/gpt-5.1" {
		t.Fatal("unexpected priced totals")
	}
	users := call[Response[UsageData]](t, client, "get_usage", map[string]any{"group_by": "user"})
	for _, g := range users.Data.Groups {
		if g.ID == "alice" && (g.Name != "Alice" || g.Department != "Engineering" || g.Location != "Zurich") {
			t.Fatalf("user identity: %+v", g)
		}
	}
}

func TestEmptyDataAndInvalidInputs(t *testing.T) {
	client, _ := connect(t, fixture(t), "admin", "admins")
	for _, args := range []map[string]any{{}, {"interval": "day"}, {"group_by": "user"}, {"group_by": "prompt_size"}, {"view": "adoption"}} {
		for _, name := range []string{"get_usage", "get_categories"} {
			if name == "get_categories" && (args["group_by"] != nil || args["view"] != nil) {
				continue
			}
			args["applications"] = []string{"does-not-exist"}
			r := call[Response[json.RawMessage]](t, client, name, args)
			// The open-ended last prompt-size bin is the only legitimate null.
			if len(r.Warnings) == 0 || strings.Contains(strings.ReplaceAll(string(r.Data), `"max_input_tokens":null`, ""), "null") {
				t.Fatalf("%s %v empty result: %+v %s", name, args, r, r.Data)
			}
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
		{"get_usage", map[string]any{"group_by": "sql"}},
		{"get_usage", map[string]any{"group_by": "model", "limit": -1}},
		{"get_usage", map[string]any{"group_by": "model", "interval": "day"}},
		{"get_usage", map[string]any{"view": "sql"}},
		{"get_usage", map[string]any{"view": "adoption", "group_by": "user"}},
		{"get_usage", map[string]any{"view": "adoption", "interval": "minute"}},
		{"get_categories", map[string]any{"limit": 101}},
		{"get_categories", map[string]any{"group_by": "model"}},
		{"get_categories", map[string]any{"models": []string{"gpt-5.1"}}},
		{"get_categories", map[string]any{"interval": "minute"}},
		{"get_categories", map[string]any{"interval": "day", "group_by": "department"}},
		{"get_usage", map[string]any{"group_by": "prompt_size", "interval": "day"}},
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
	h := NewHandler(nil, unverified("admins"))
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
	r.Header.Set("Authorization", "Bearer "+testToken("alice", "admins"))
	r.Header.Set("Origin", "https://unrelated.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin request status=%d", w.Code)
	}
}

func TestDevFallbackOnlyWhenEnabled(t *testing.T) {
	// nil store is safe: initialization registers tools but executes no queries.
	noFallback := NewHandler(nil, unverified("admins"))
	r := httptest.NewRequest("POST", "/mcp", nil)
	w := httptest.NewRecorder()
	noFallback.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("missing token accepted without the dev option: status=%d", w.Code)
	}

	// The option replaces the 401 with a fixed identity, so an anonymous caller
	// reaches the transport instead of the auth wall.
	h := NewHandler(nil, unverified("admins"), WithDevFallback("dev@example.com", true))
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
	closed := NewHandler(nil, unverified("admins"))
	r := httptest.NewRequest("POST", "http://insights.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+testToken("alice", "admins"))
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	closed.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("default allowed cross-origin: status=%d acao=%q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}

	// An explicit allowlist answers preflights without credentials and echoes
	// the origin, but still refuses origins it was not given.
	listed := NewHandler(nil, unverified("admins"), WithAllowedOrigins(origin))
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
	r.Header.Set("Authorization", "Bearer "+testToken("alice", "admins"))
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	listed.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin admitted: status=%d", w.Code)
	}

	// Wildcard admits any origin and drops the cross-origin protection.
	open := NewHandler(nil, unverified("admins"), WithAllowedOrigins("*"))
	r = httptest.NewRequest("POST", "http://insights.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+testToken("alice", "admins"))
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
	for _, name := range []string{"get_usage", "get_categories"} {
		r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !r.IsError || !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "2000") {
			t.Fatalf("%s point limit: %+v %v", name, r, err)
		}
	}
	args["interval"] = "day"
	call[Response[UsageData]](t, client, "get_usage", args)
	call[Response[CategoryData]](t, client, "get_categories", args)

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

// unverified decodes test JWTs without checking a signature so handler tests
// can mint identities; production wires api.Handler.VerifyBearer instead.
func unverified(adminGroup string) Verifier {
	return func(r *http.Request) (string, bool, error) {
		p, ok := tokenPrincipal(r.Header.Get("Authorization"), adminGroup)
		if !ok {
			return "", false, errors.New("invalid test token")
		}
		return p.user, p.admin, nil
	}
}

func tokenPrincipal(header, adminGroup string) (principal, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 32*1024 {
		return principal{}, false
	}
	jwt := strings.Split(parts[1], ".")
	if len(jwt) != 3 || jwt[0] == "" {
		return principal{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(jwt[1])
	if err != nil {
		return principal{}, false
	}
	var claims struct {
		OID    string   `json:"oid"`
		Sub    string   `json:"sub"`
		Groups []string `json:"groups"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return principal{}, false
	}
	user := strings.TrimSpace(claims.OID)
	if user == "" {
		user = strings.TrimSpace(claims.Sub)
	}
	if user == "" {
		return principal{}, false
	}
	p := principal{user: user}
	for _, group := range claims.Groups {
		if adminGroup != "" && group == adminGroup {
			p.admin = true
		}
	}
	return p, true
}
