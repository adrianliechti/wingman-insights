package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"insights/internal/store"
)

func activityFixture(t *testing.T) (*store.Store, map[string]any) {
	t.Helper()
	s := fixture(t)
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	span := func(id, op, tool string, offset time.Duration, attrs map[string]string) store.SpanRow {
		return store.SpanRow{ReceivedAt: from, Time: from.Add(offset), TraceID: "PRIVATE-trace", SpanID: id,
			Name: "PRIVATE-span", AgentName: "PRIVATE-agent-name", UserID: "alice@example.com", AppID: "activity",
			ProviderName: "openai", RequestModel: "gpt-5.1", OperationName: op, ToolName: tool, Duration: 1.5, Status: "ok", Attributes: attrs}
	}
	agent := func(id, agentID, kind string, offset time.Duration) store.SpanRow {
		return span(id, "invoke_agent", "", offset, map[string]string{"gen_ai.agent.id": agentID, "wingman.agent.kind": kind})
	}
	web := map[string]string{"wingman.mcp.server.id": "web", "wingman.mcp.server.name": "Web Search", "mcp.tool.name": "web_research"}
	spans := []store.SpanRow{
		agent("old", "PRIVATE-a", "configured", -time.Hour),
		agent("a1", "PRIVATE-a", "configured", time.Minute),
		agent("a2", "PRIVATE-a", "configured", time.Hour),
		agent("b1", "PRIVATE-b", "configured", time.Hour),
		agent("b2", "PRIVATE-b", "configured", time.Hour+time.Minute),
		agent("missing", "", "configured", time.Hour),
		agent("delegate", "PRIVATE-d", "delegated", time.Hour),
		span("web1", "execute_tool", "mcp__web__research", time.Minute, web),
		span("web2", "execute_tool", "mcp__web__research", time.Hour, web),
		span("insights", "execute_tool", "mcp__insights__usage", time.Hour, map[string]string{"wingman.mcp.server.id": "insights", "wingman.mcp.server.name": "Insights", "mcp.tool.name": "get_usage", "wingman.plugin.id": "engineering"}),
		span("skill", "execute_tool", "load_skill", time.Hour, map[string]string{"gen_ai.skill.name": "engineering/report", "wingman.skill.name": "Report", "wingman.plugin.id": "engineering"}),
		span("private-skill", "execute_tool", "load_skill", time.Hour, map[string]string{"gen_ai.skill.name": "PRIVATE-skill", "wingman.skill.source": "personal"}),
		span("ask1", "execute_tool", "ask_questions", time.Minute, nil),
		span("ask2", "execute_tool", "ask_questions", time.Hour, nil),
		span("create", "execute_tool", "artifacts_create", time.Hour, map[string]string{"wingman.plugin.id": "artifacts"}),
	}
	spans[2].Status = "error"
	spans[3].Status = "" // Unreported is not assumed successful.
	dup := spans[7]
	dup.ReceivedAt = from.Add(-time.Second)
	dup.Duration = 99
	spans = append(spans, dup)
	if err := s.InsertSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	return s, map[string]any{"from": from.Format(time.RFC3339), "to": from.Add(3*time.Hour - time.Second).Format(time.RFC3339),
		"applications": []string{"activity"}, "users": []string{"alice"}, "departments": []string{"Engineering"}, "locations": []string{"Zurich"}, "providers": []string{"openai"}, "models": []string{"gpt-5.1"}}
}

func activityArgs(base map[string]any, extra map[string]any) map[string]any {
	args := map[string]any{}
	for k, v := range base {
		args[k] = v
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func assertActivityPrivacy(t *testing.T, out Response[ActivityData]) {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "null") || strings.Contains(string(b), "trace_id") {
		t.Fatalf("private metadata or null escaped projection: %s", b)
	}
}

func TestActivitySummaryAndRankings(t *testing.T) {
	s, args := activityFixture(t)
	client, _ := connect(t, s, "admin", "admins")
	out := call[Response[ActivityData]](t, client, "get_activity", args)
	assertActivityPrivacy(t, out)
	totals := out.Data.Totals
	if *totals.DistinctCustomAgents != 2 || *totals.AgentRuns != 5 || *totals.MCPToolCalls != 3 || *totals.PluginExecutions != 3 || *totals.BuiltinToolCalls != 3 {
		t.Fatalf("summary: %+v", totals)
	}
	if totals.MostUsedMCPTool.Name != "web research" || totals.MostUsedMCPTool.ServerName != "Web Search" || totals.MostUsedMCPTool.Executions != 2 || totals.MostUsedMCPTool.AvgDurationMS != 1500 {
		t.Fatalf("most used tool: %+v", totals.MostUsedMCPTool)
	}
	if out.Query.View != "summary" || len(out.Query.Filters.Models) != 1 || len(out.Warnings) != 1 {
		t.Fatalf("query/coverage: %+v", out)
	}
	for _, tc := range []struct {
		view, by, top string
		executions    int64
	}{
		{"mcp", "server", "Web Search", 2}, {"mcp", "tool", "web research", 2}, {"plugins", "plugin", "engineering", 2}, {"builtin", "tool", "ask questions", 2},
	} {
		r := call[Response[ActivityData]](t, client, "get_activity", activityArgs(args, map[string]any{"view": tc.view, "group_by": tc.by, "limit": 1}))
		assertActivityPrivacy(t, r)
		if r.Data.TotalGroups != 2 || !r.Data.Truncated || len(r.Data.Groups) != 1 || r.Data.Groups[0].Name != tc.top || r.Data.Groups[0].Executions != tc.executions {
			t.Fatalf("%s/%s ranking: %+v", tc.view, tc.by, r.Data)
		}
		if tc.view == "plugins" && (len(r.Data.Groups[0].Skills) != 1 || r.Data.Groups[0].TotalSkills != 1) {
			t.Fatalf("plugin skill details: %+v", r.Data.Groups[0])
		}
	}
}

func TestActivityTrendsMatchDashboardQueries(t *testing.T) {
	s, args := activityFixture(t)
	client, _ := connect(t, s, "admin", "admins")
	from, _ := time.Parse(time.RFC3339, args["from"].(string))
	to, _ := time.Parse(time.RFC3339, args["to"].(string))
	f := store.Filter{App: []string{"activity"}, User: []string{"alice"}, Department: []string{"Engineering"}, Location: []string{"Zurich"}, Provider: []string{"openai"}, Models: []string{"gpt-5.1"}}
	for _, view := range []string{"agents", "mcp", "plugins", "builtin"} {
		out := call[Response[ActivityData]](t, client, "get_activity", activityArgs(args, map[string]any{"view": view, "interval": "hour"}))
		assertActivityPrivacy(t, out)
		var want []store.TimeseriesPoint
		var err error
		if view == "agents" {
			want, err = s.QueryCustomAgentTimeseries(context.Background(), from, to, "1 hour", f)
		} else {
			by := view
			if by == "plugins" {
				by = "plugin"
			}
			want, err = s.QueryActivityBreakdownTimeseries(context.Background(), from, to, "1 hour", by, f)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Data.Points) != len(want) || out.Query.Interval != "hour" {
			t.Fatalf("%s points: %+v", view, out)
		}
		var sum int64
		for i, p := range out.Data.Points {
			if p.Series != want[i].Label || p.Executions != int64(want[i].Value) || p.Time != want[i].Bucket.UTC().Format(time.RFC3339) {
				t.Fatalf("dashboard parity %s: %+v vs %+v", view, p, want[i])
			}
			sum += p.Executions
		}
		var total int64
		switch view {
		case "agents":
			total = *out.Data.Totals.AgentRuns
			if *out.Data.Totals.FirstObservedRuns != 1 || *out.Data.Totals.OtherRuns != 4 {
				t.Fatalf("first observed must use historical runs and exclude delegation: %+v", out.Data.Totals)
			}
		case "mcp":
			total = *out.Data.Totals.MCPToolCalls
		case "plugins":
			total = *out.Data.Totals.PluginExecutions
		case "builtin":
			total = *out.Data.Totals.BuiltinToolCalls
		}
		if sum != total {
			t.Fatalf("trend sum %d differs from total %d", sum, total)
		}
	}
}

func TestActivityValidationEmptyResultsAndLimits(t *testing.T) {
	s, args := activityFixture(t)
	client, _ := connect(t, s, "admin", "admins")
	for _, extra := range []map[string]any{
		{}, {"view": "agents"}, {"view": "agents", "interval": "hour"}, {"view": "mcp", "interval": "day"}, {"view": "plugins", "group_by": "plugin"}, {"view": "builtin", "group_by": "tool"},
	} {
		out := call[Response[ActivityData]](t, client, "get_activity", activityArgs(activityArgs(args, extra), map[string]any{"applications": []string{"missing"}}))
		assertActivityPrivacy(t, out)
		if len(out.Warnings) == 0 {
			t.Fatal("missing empty-result warning")
		}
		for _, p := range out.Data.Points {
			if p.Executions != 0 {
				t.Fatalf("nonzero empty trend: %+v", out)
			}
		}
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "chat"
	}
	for _, extra := range []map[string]any{
		{"view": "unknown"}, {"interval": "hour"}, {"view": "agents", "group_by": "server"}, {"view": "mcp", "group_by": "plugin"}, {"view": "plugins", "group_by": "tool"}, {"view": "builtin", "group_by": "server"},
		{"view": "mcp", "group_by": "tool", "interval": "day"}, {"view": "agents", "interval": "minute"}, {"limit": 101}, {"limit": -1}, {"from": "bad"}, {"to": "bad"}, {"from": args["to"], "to": args["from"]}, {"models": []string{""}}, {"applications": tooMany},
	} {
		r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_activity", Arguments: activityArgs(args, extra)})
		if err != nil || !r.IsError {
			t.Fatalf("invalid input accepted %+v: %+v %v", extra, r, err)
		}
	}
	large := activityArgs(args, map[string]any{"view": "agents", "interval": "hour", "from": "2026-08-01T00:00:00Z", "to": "2026-10-29T00:00:00Z"})
	r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_activity", Arguments: large})
	if err != nil || !r.IsError || !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "2000") {
		t.Fatalf("trend point bound: %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (tools{store: s}).activity(ctx, ActivityInput{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_activity", Arguments: args})
	if err != nil || !r.IsError || r.Content[0].(*mcp.TextContent).Text != "internal query error" {
		t.Fatalf("query error not redacted: %+v %v", r, err)
	}
}

func TestActivityInputSchemaMatchesExistingProperties(t *testing.T) {
	client, _ := connect(t, fixture(t), "admin", "admins")
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "get_activity" {
			b, _ := json.Marshal(tool.InputSchema)
			var schema struct{ Properties map[string]json.RawMessage }
			if err := json.Unmarshal(b, &schema); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"from", "to", "applications", "users", "departments", "locations", "providers", "models", "view", "interval", "group_by", "limit"} {
				if _, ok := schema.Properties[name]; !ok {
					t.Fatalf("missing shared property %s", name)
				}
			}
			if reflect.ValueOf(tool.OutputSchema).IsZero() {
				t.Fatal("missing output schema")
			}
			return
		}
	}
	t.Fatal("get_activity not discoverable")
}

func TestActivityPluginSkillBoundsAndAdminGate(t *testing.T) {
	s, args := activityFixture(t)
	from, _ := time.Parse(time.RFC3339, args["from"].(string))
	for _, name := range []string{"engineering/extra-one", "engineering/extra-two"} {
		if err := s.InsertSpans(context.Background(), []store.SpanRow{{Time: from, ReceivedAt: from, TraceID: name, SpanID: "skill", ToolName: "load_skill", AppID: "activity", UserID: "alice", ProviderName: "openai", RequestModel: "gpt-5.1",
			Attributes: map[string]string{"gen_ai.skill.name": name, "wingman.plugin.id": "engineering"}}}); err != nil {
			t.Fatal(err)
		}
	}
	client, transport := connect(t, s, "admin", "admins")
	out := call[Response[ActivityData]](t, client, "get_activity", activityArgs(args, map[string]any{"view": "plugins", "group_by": "plugin", "limit": 1}))
	if len(out.Data.Groups) != 1 || len(out.Data.Groups[0].Skills) != 1 || out.Data.Groups[0].TotalSkills != 3 || !out.Data.Groups[0].SkillsTruncated || *out.Data.Totals.PluginExecutions != 5 {
		t.Fatalf("plugin skill limits lost full totals: %+v", out.Data)
	}
	assertActivityPrivacy(t, out)
	transport.token.Store(testToken("alice", "users"))
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_activity", Arguments: args}); err == nil {
		t.Fatal("non-admin activity call was accepted")
	}
}
