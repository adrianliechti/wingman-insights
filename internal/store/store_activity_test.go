package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCustomAgentHistoryMatchesCustomSummary(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	from := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	to := from.Add(4*time.Hour - time.Second)
	span := func(spanID, agentID, kind, plugin string, offset time.Duration) SpanRow {
		return SpanRow{ReceivedAt: from, Time: from.Add(offset), TraceID: "trace", SpanID: spanID,
			Name: "PRIVATE name", AgentName: "PRIVATE name", AppID: "chat", OperationName: "invoke_agent",
			Attributes: map[string]string{"gen_ai.agent.id": agentID, "wingman.agent.kind": kind, "wingman.plugin.id": plugin}}
	}
	spans := []SpanRow{
		span("first", "PRIVATE-a", "configured", "", time.Minute),
		span("repeat", "PRIVATE-a", "configured", "", 2*time.Minute),
		span("later", "PRIVATE-a", "configured", "", 2*time.Hour),
		span("second", "PRIVATE-b", "configured", "", 2*time.Hour+time.Minute),
		span("missing-id", "", "configured", "", 2*time.Hour+2*time.Minute),
		span("delegated", "PRIVATE-d", "delegated", "", time.Minute),
		span("plugin", "PRIVATE-p", "configured", "plugin", time.Minute),
		span("legacy", "", "", "", time.Minute),
		span("outside", "PRIVATE-outside", "configured", "", -time.Minute),
	}
	historical := span("historical", "PRIVATE-a", "configured", "", -3*time.Hour)
	historical.AppID = "other-app"
	spans = append(spans, historical)
	duplicate := spans[0]
	duplicate.ReceivedAt = from.Add(time.Second)
	spans = append(spans, duplicate)
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	summary, err := s.QueryActivitySummary(ctx, from, to, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ConfiguredAgents != 2 || summary.CustomAgentRuns != 5 || summary.IdentifiedCustomRuns != 4 {
		t.Fatalf("custom scope must exclude delegated, plugin, legacy and outside runs: %+v", summary)
	}
	points, err := s.QueryCustomAgentTimeseries(ctx, from, to, "1 hour", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 8 {
		t.Fatalf("want four buckets for each series including zeros: %+v", points)
	}
	var runPoints, firstObserved []TimeseriesPoint
	for _, point := range points {
		if point.Label == "other_runs" {
			runPoints = append(runPoints, point)
		} else if point.Label == "first_observed" {
			firstObserved = append(firstObserved, point)
		}
	}
	if len(runPoints) != 4 || len(firstObserved) != 4 {
		t.Fatalf("missing series: %+v", points)
	}
	for i, want := range []float64{2, 0, 2, 0} {
		if runPoints[i].Value != want || !runPoints[i].Bucket.Equal(from.Add(time.Duration(i)*time.Hour)) {
			t.Fatalf("bucket %d: %+v, want %v", i, runPoints[i], want)
		}
	}
	for i, want := range []float64{0, 0, 1, 0} {
		if firstObserved[i].Value != want {
			t.Fatalf("first observed must use full retained history: %+v", firstObserved)
		}
	}
	var runs int64
	var chartRuns float64
	for _, point := range points {
		runs += point.Count
		chartRuns += point.Value
	}
	if runs != summary.CustomAgentRuns || chartRuns != float64(summary.CustomAgentRuns) {
		t.Fatalf("history runs %d disagree with summary %+v", runs, summary)
	}
	payload, _ := json.Marshal(points)
	if strings.Contains(string(payload), "PRIVATE") {
		t.Fatalf("private identity leaked: %s", payload)
	}
	for _, f := range []Filter{{App: []string{"missing"}}, {User: []string{"missing"}}, {Provider: []string{"missing"}}} {
		filtered, err := s.QueryCustomAgentTimeseries(ctx, from, to, "1 hour", f)
		if err != nil {
			t.Fatal(err)
		}
		if len(filtered) != 8 {
			t.Fatalf("empty filter must retain zero buckets: %+v", filtered)
		}
		for _, p := range filtered {
			if p.Value != 0 || p.Count != 0 {
				t.Fatalf("filter did not apply: %+v", filtered)
			}
		}
	}
	for _, f := range []Filter{{}, {App: []string{"chat"}}} {
		later, err := s.QueryCustomAgentTimeseries(ctx, from.Add(2*time.Hour), to, "1 hour", f)
		if err != nil {
			t.Fatal(err)
		}
		var newlyObserved float64
		for _, point := range later {
			if point.Label == "first_observed" {
				newlyObserved += point.Value
			}
		}
		if newlyObserved != 1 {
			t.Fatalf("changing range or app must not make an old agent new: %+v", later)
		}
	}
}

func TestAgentStackDoesNotCountExcludedFirstRunAsNew(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	from := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	to := from.Add(2*time.Hour - time.Second)
	for i, provider := range []string{"original", "later"} {
		if err := s.InsertSpans(ctx, []SpanRow{{ReceivedAt: from, Time: from.Add(time.Duration(i) * time.Hour), TraceID: provider, SpanID: "run", OperationName: "invoke_agent", ProviderName: provider,
			Attributes: map[string]string{"gen_ai.agent.id": "agent-1", "wingman.agent.kind": "configured"}}}); err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.QueryCustomAgentTimeseries(ctx, from, to, "1 hour", Filter{Provider: []string{"later"}})
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, point := range points {
		if point.Value < 0 || point.Label == "first_observed" && point.Value != 0 {
			t.Fatalf("excluded first run must not add a new-agent segment: %+v", points)
		}
		total += point.Value
	}
	if total != 1 {
		t.Fatalf("stack should contain only the matching repeat run: %+v", points)
	}
}

func TestCustomActivityCombinesPrivateUsageWithoutExposingIdentities(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var spans []SpanRow
	for i := 0; i < 3; i++ {
		suffix := []string{"first", "second", "third"}[i]
		user := "PRIVATE-alice"
		if i == 2 {
			user = "PRIVATE-bob"
		}
		status := "ok"
		if i == 2 {
			status = "error"
		}
		spans = append(spans,
			SpanRow{ReceivedAt: now, Time: now.Add(time.Duration(i) * time.Second), TraceID: user, SpanID: "agent-" + suffix, Name: "PRIVATE-agent-span", AgentName: "PRIVATE-agent-name-" + suffix, UserID: user, AppID: "chat", OperationName: "invoke_agent", Duration: float64(1 + i*2), Status: status, Attributes: map[string]string{"gen_ai.agent.id": "PRIVATE-agent-id-" + suffix, "wingman.agent.kind": "configured"}},
			SpanRow{ReceivedAt: now, Time: now.Add(time.Duration(i) * time.Second), TraceID: user, SpanID: "skill-" + suffix, Name: "PRIVATE-skill-span", UserID: user, AppID: "chat", ToolName: "load_skill", Duration: float64(2 + i*2), Status: status, Attributes: map[string]string{"wingman.skill.id": "PRIVATE-skill-id-" + suffix, "wingman.skill.name": "PRIVATE-skill-name-" + suffix, "wingman.skill.source": "personal"}},
		)
	}
	// The pooled custom rows must not absorb shipped, plugin or untyped skills,
	// or delegated agents, and duplicate exports must still count once.
	duplicate := spans[0]
	duplicate.ReceivedAt = now.Add(-time.Second)
	spans = append(spans, duplicate,
		SpanRow{ReceivedAt: now, Time: now, TraceID: "extra", SpanID: "builtin", ToolName: "load_skill", Attributes: map[string]string{"wingman.skill.source": "builtin"}},
		SpanRow{ReceivedAt: now, Time: now, TraceID: "extra", SpanID: "plugin", ToolName: "load_skill", Attributes: map[string]string{"wingman.skill.source": "personal", "wingman.plugin.id": "reports"}},
		SpanRow{ReceivedAt: now, Time: now, TraceID: "extra", SpanID: "untyped", ToolName: "load_skill"},
		SpanRow{ReceivedAt: now, Time: now, TraceID: "extra", SpanID: "delegate", OperationName: "invoke_agent", Attributes: map[string]string{"wingman.agent.kind": "delegated"}},
	)
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryCustomActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("custom rows: %+v, %v", rows, err)
	}
	for _, row := range rows {
		if row.Count != 3 || row.Users != 2 || row.ErrorCount != 1 || row.SuccessCount != 2 {
			t.Fatalf("pooled custom usage: %+v", row)
		}
		if row.Type == "custom_agent" && row.AvgDuration != 3 || row.Type == "custom_skill" && row.AvgDuration != 4 {
			t.Fatalf("pooled weighted duration: %+v", row)
		}
	}
	payload, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(payload), "PRIVATE") {
		t.Fatalf("private names, IDs or trace references leaked: %s, %v", payload, err)
	}
	filtered, err := s.QueryCustomActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{User: []string{"PRIVATE-alice"}})
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered custom rows: %+v, %v", filtered, err)
	}
	for _, row := range filtered {
		if row.Count != 2 || row.Users != 1 || row.ErrorCount != 0 {
			t.Fatalf("filtered pooled custom usage: %+v", row)
		}
	}
	empty, err := s.QueryCustomActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{App: []string{"missing"}})
	if err != nil || len(empty) != 2 || empty[0].Count != 0 || empty[1].Count != 0 {
		t.Fatalf("empty custom entries: %+v, %v", empty, err)
	}
}

func TestActivityCountsExecutionsAndCoverage(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	span := func(id, operation, tool string, attrs map[string]string) SpanRow {
		return SpanRow{ReceivedAt: now, Time: now, TraceID: "trace", SpanID: id, Name: id,
			AppID: "chat", OperationName: operation, ToolName: tool, Duration: 2,
			Attributes: attrs}
	}
	agent := span("delegate", "", "mcp__internet___agent_142ca91f", map[string]string{"tanstack.ai.tool.outcome": "success"})
	duplicate := agent
	duplicate.ReceivedAt = now.Add(-time.Second)
	duplicate.Duration = 100
	model := span("model", "chat", "", nil)
	model.AgentName = "assistant" // A model tagged with an agent is not another run.
	model.Attributes = map[string]string{"wingman.operation.name": agent.ToolName}
	native := span("native", "invoke_agent", "", map[string]string{"gen_ai.agent.id": "agent-1", "wingman.agent.kind": "configured"})
	native.AgentName, native.UserID, native.Status = "Researcher", "user-1", "ok"
	failed := span("failed", "execute_tool", "mcp__files__search_abcd1234", map[string]string{
		"wingman.mcp.server.id": "files", "mcp.tool.name": "search", "tanstack.ai.tool.outcome": "error", "wingman.plugin.id": "documents",
	})
	skill := span("skill", "", "load_skill", map[string]string{"tanstack.ai.tool.outcome": "success"})
	namedSkill := span("named-skill", "", "load_skill", map[string]string{"wingman.skill.name": "Writing", "tanstack.ai.tool.outcome": "success"})
	unknown := span("other", "execute_tool", "execute_script", nil)
	custom := span("custom", "execute_tool", "artifacts_custom_action", nil)
	// This remote tool is called "agent", but is not the browser's @agent wrapper.
	remoteAgent := span("remote-agent", "", "mcp__files__agent_abcd1234", nil)
	old := agent
	old.SpanID, old.Time = "old", now.Add(-2*time.Hour)
	if err := s.InsertSpans(ctx, []SpanRow{agent, duplicate, model, native, failed, skill, namedSkill, unknown, custom, remoteAgent, old}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatalf("rows = %+v, want 8 capability groups", rows)
	}
	byName := map[string]ActivityRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if row := byName[agent.ToolName]; row.Type != "agent" || row.Count != 1 || row.SuccessCount != 1 || row.AvgDuration != 2 || row.WithAgentID != 0 {
		t.Fatalf("delegated run (deduplicated): %+v", row)
	}
	if row := byName["Researcher"]; row.Type != "agent" || row.WithAgentID != 1 || row.WithUser != 1 || row.SuccessCount != 1 {
		t.Fatalf("native run: %+v", row)
	}
	if row := byName["search"]; row.Type != "connector" || row.Server != "files" || row.ErrorCount != 1 || row.SuccessCount != 0 || row.WithPlugin != 1 {
		t.Fatalf("failed connector: %+v", row)
	}
	if row := byName["Unidentified skill"]; row.Count != 1 || row.WithSkillName != 0 || row.Type != "skill" {
		t.Fatalf("unidentified skill: %+v", row)
	}
	if row := byName["Writing"]; row.WithSkillName != 1 || row.Type != "skill" {
		t.Fatalf("named skill: %+v", row)
	}
	if row := byName["execute_script"]; row.Type != "builtin" || row.SuccessCount != 0 || row.ErrorCount != 0 {
		t.Fatalf("unreported outcome must not become success: %+v", row)
	}
	if row := byName[custom.ToolName]; row.Type != "tool" {
		t.Fatalf("unknown tools must stay separate from built-ins, including similar names: %+v", row)
	}
	if row := byName[remoteAgent.ToolName]; row.Type != "connector" {
		t.Fatalf("remote agent tool must not be confused with delegation: %+v", row)
	}
	if rows, err := s.QueryActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{User: []string{"user-1"}}); err != nil || len(rows) != 1 || rows[0].Name != "Researcher" {
		t.Fatalf("user filter: %+v, %v", rows, err)
	}
	if rows, err := s.QueryActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{App: []string{"other"}}); err != nil || len(rows) != 0 {
		t.Fatalf("app filter: %+v, %v", rows, err)
	}
	summary, err := s.QueryActivitySummary(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Agents != 1 || summary.ConfiguredAgents != 1 || summary.AgentRuns != 2 || summary.Skills != 1 || summary.SkillLoads != 2 || summary.Connectors != 1 || summary.ConnectorCalls != 2 || summary.Plugins != 1 || summary.PluginCalls != 1 || summary.Users != 1 || summary.Calls != 8 {
		t.Fatalf("summary must distinguish executions, known identities and unknowns: %+v", summary)
	}
	plugins, err := s.QueryPluginActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil || len(plugins) != 1 || plugins[0].Name != "documents" || plugins[0].Count != 1 || plugins[0].ErrorCount != 1 {
		t.Fatalf("plugin attribution: %+v, %v", plugins, err)
	}
	for _, f := range []Filter{{}, {App: []string{"other"}}, {User: []string{"user-1"}}} {
		from, to := now.Add(-time.Hour), now.Add(time.Minute)
		points, err := s.QueryActivityTimeseries(ctx, from, to, "1 hour", f)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := s.QueryActivitySummary(ctx, from, to, f)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.QueryActivity(ctx, from, to, f)
		if err != nil {
			t.Fatal(err)
		}
		var builtin int64
		for _, row := range rows {
			if row.Type == "builtin" {
				builtin += row.Count
			}
		}
		if summary.BuiltinCalls != builtin {
			t.Fatalf("builtin summary %d disagrees with table %d", summary.BuiltinCalls, builtin)
		}
		want := map[string]int64{"skills": summary.SkillLoads, "mcp": summary.ConnectorCalls, "plugins": summary.PluginCalls, "builtin": builtin}
		totals := map[string]float64{}
		buckets := map[time.Time]int{}
		for _, point := range points {
			if _, ok := want[point.Label]; !ok {
				t.Fatalf("unexpected series: %+v", point)
			}
			if point.Value != float64(point.Count) {
				t.Fatalf("count mismatch: %+v", point)
			}
			totals[point.Label] += point.Value
			buckets[point.Bucket]++
		}
		for label, count := range want {
			if totals[label] != float64(count) {
				t.Fatalf("%s trend total %v, want %d", label, totals[label], count)
			}
		}
		if len(buckets) < 2 {
			t.Fatalf("missing zero-filled buckets: %+v", points)
		}
		for _, count := range buckets {
			if count != 4 {
				t.Fatalf("each bucket needs all four categories: %+v", points)
			}
		}
	}
}

func TestActivityBreakdownUsesStableServerAndPluginIdentities(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	from := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	to := from.Add(2*time.Hour - time.Second)
	span := func(id, tool, server, name, plugin string, offset time.Duration) SpanRow {
		return SpanRow{ReceivedAt: from, Time: from.Add(offset), TraceID: "trace", SpanID: id, Name: "PRIVATE trace", AppID: "chat", ToolName: tool,
			Attributes: map[string]string{"wingman.mcp.server.id": server, "wingman.mcp.server.name": name, "wingman.plugin.id": plugin}}
	}
	spans := []SpanRow{
		span("one", "mcp__s1__search", "s1", "Old name", "", time.Minute),
		span("renamed", "mcp__s1__read", "s1", "Shared", "", time.Hour),
		span("two", "mcp__s2__read", "s2", "Shared", "plugin-one", time.Minute),
		span("unknown", "mcp__unknown__read", "", "", "", time.Minute),
		span("skill", "load_skill", "", "", "plugin-one", time.Hour),
		span("builtin", "ask_questions", "", "", "plugin-two", time.Hour),
		span("repeat-builtin", "ask_questions", "", "", "", time.Minute),
		span("other-builtin", "artifacts_create", "", "", "", time.Hour),
	}
	duplicate := spans[0]
	duplicate.ReceivedAt = from.Add(time.Second)
	spans = append(spans, duplicate)
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		by     string
		totals map[string]float64
	}{
		{"mcp", map[string]float64{"Shared (s1)": 2, "Shared (s2 / plugin-one)": 1, "Unidentified MCP server": 1}},
		{"plugin", map[string]float64{"plugin-one": 2, "plugin-two": 1}},
		{"builtin", map[string]float64{"ask questions": 2, "artifacts create": 1}},
	} {
		points, err := s.QueryActivityBreakdownTimeseries(ctx, from, to, "1 hour", tc.by, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(points) != len(tc.totals)*2 {
			t.Fatalf("each entity needs two zero-filled buckets: %+v", points)
		}
		totals := map[string]float64{}
		for _, point := range points {
			totals[point.Label] += point.Value
		}
		if len(totals) != len(tc.totals) {
			t.Fatalf("unexpected identities: %+v", totals)
		}
		for label, want := range tc.totals {
			if totals[label] != want {
				t.Fatalf("%s %s: got %v, want %v", tc.by, label, totals[label], want)
			}
		}
		payload, _ := json.Marshal(points)
		if strings.Contains(string(payload), "PRIVATE") {
			t.Fatalf("trace metadata leaked: %s", payload)
		}
		empty, err := s.QueryActivityBreakdownTimeseries(ctx, from, to, "1 hour", tc.by, Filter{App: []string{"missing"}})
		if err != nil || len(empty) != 0 {
			t.Fatalf("empty filter: %+v, %v", empty, err)
		}
	}
}

func TestActivityStableIdentityAndCanonicalUsers(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO directory (alias, id, kind) VALUES ('old-id', 'alice', 'user'), ('new-id', 'alice', 'user')`); err != nil {
		t.Fatal(err)
	}
	var spans []SpanRow
	for i, name := range []string{"Original name", "Renamed agent"} {
		user := []string{"old-id", "new-id"}[i]
		spans = append(spans, SpanRow{
			ReceivedAt: now, Time: now.Add(time.Duration(i) * time.Second), TraceID: user, SpanID: "run", Name: "run",
			AppID: "chat", UserID: user, OperationName: "invoke_agent", AgentName: name, SessionID: "conversation",
			Attributes: map[string]string{"gen_ai.agent.id": "agent-1", "wingman.agent.kind": "configured"},
		})
	}
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil || len(rows) != 1 || rows[0].Count != 2 || rows[0].Name != "Renamed agent" || rows[0].Users != 1 || rows[0].Conversations != 1 {
		t.Fatalf("renames and user aliases must not split identities: %+v, %v", rows, err)
	}
	summary, err := s.QueryActivitySummary(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil || summary.Agents != 1 || summary.Users != 1 || summary.IdentifiedCalls != 2 {
		t.Fatalf("distinct summary: %+v, %v", summary, err)
	}
	summary, err = s.QueryActivitySummary(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{User: []string{"bob"}})
	if err != nil || summary.Calls != 0 || summary.Agents != 0 || summary.Users != 0 {
		t.Fatalf("filtered empty summary: %+v, %v", summary, err)
	}
}

func TestMCPServerActivityAggregatesToolsWithoutDuplicatingUsers(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var spans []SpanRow
	for i, tool := range []string{"search", "read", "read"} {
		user := []string{"alice", "alice", "bob"}[i]
		status := "ok"
		if i == 2 {
			status = "error"
		}
		spans = append(spans, SpanRow{
			ReceivedAt: now, Time: now.Add(time.Duration(i) * time.Second), TraceID: tool + user, SpanID: "tool", Name: tool,
			ToolName: tool, AppID: "chat", UserID: user, SessionID: "conversation", Duration: float64(1 + i*2), Status: status,
			Attributes: map[string]string{"wingman.mcp.server.id": "documents", "wingman.mcp.server.name": "Documents"},
		})
	}
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryMCPServerActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != "mcp_server" || rows[0].Name != "Documents" || rows[0].Server != "documents" || rows[0].Count != 3 || rows[0].Users != 2 || rows[0].Conversations != 1 || rows[0].AvgDuration != 3 || rows[0].ErrorCount != 1 || rows[0].SuccessCount != 2 {
		t.Fatalf("server must aggregate raw calls, weighted duration and distinct users: %+v", rows)
	}
	rows, err = s.QueryMCPServerActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{User: []string{"alice"}})
	if err != nil || len(rows) != 1 || rows[0].Count != 2 || rows[0].Users != 1 || rows[0].AvgDuration != 2 || rows[0].ErrorCount != 0 {
		t.Fatalf("filtered server: %+v, %v", rows, err)
	}
	rows, err = s.QueryMCPServerActivity(ctx, now.Add(-time.Hour), now.Add(time.Minute), Filter{App: []string{"other"}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("filtered empty servers: %+v, %v", rows, err)
	}
}
