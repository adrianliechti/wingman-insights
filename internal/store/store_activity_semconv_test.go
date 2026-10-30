package store

import (
	"context"
	"testing"
	"time"
)

func TestActivityStandardAttributesAndLegacyHistory(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	span := func(id, tool string, attrs map[string]string) SpanRow {
		return SpanRow{ReceivedAt: now, Time: now, TraceID: "trace", SpanID: id, ToolName: tool,
			OperationName: "execute_tool", AgentName: "Coordinator", Duration: 1, Attributes: attrs}
	}
	legacySkill := span("old-skill", "load_skill", map[string]string{
		"wingman.skill.id": "office:reports", "wingman.skill.name": "reports", "wingman.plugin.id": "office",
	})
	standardSkill := span("new-skill", "load_skill", map[string]string{
		"gen_ai.skill.name": "office:reports", "wingman.plugin.id": "office",
	})
	standardSkill.Time = now.Add(time.Second)
	otherSkill := span("other-skill", "load_skill", map[string]string{
		"gen_ai.skill.name": "other:reports", "wingman.plugin.id": "other",
	})
	target := span("transfer", "research", map[string]string{
		"gen_ai.agent.id": "caller", "wingman.agent.kind": "configured",
		"gen_ai.transfer.target.name": "Researcher", "gen_ai.transfer.mode": "return_to_caller",
		"wingman.transfer.target.id": "mcp:research", "wingman.transfer.target.kind": "delegated",
	})
	unidentifiedTarget := span("unknown-transfer", "delegate", map[string]string{
		"gen_ai.agent.id": "caller", "wingman.agent.kind": "configured", "gen_ai.transfer.mode": "return_to_caller",
	})
	legacyTarget := span("old-transfer", "legacy_delegate", map[string]string{
		"wingman.activity.kind": "agent", "gen_ai.agent.id": "builtin:legacy", "wingman.agent.kind": "builtin",
	})
	legacyTarget.AgentName = "Legacy delegate"
	connector := span("connector", "search", map[string]string{
		"mcp.method.name": "tools/call", "gen_ai.tool.name": "search", "mcp.tool.name": "stale-name", "wingman.mcp.server.id": "files",
	})
	if err := s.InsertSpans(ctx, []SpanRow{legacySkill, standardSkill, otherSkill, target, unidentifiedTarget, legacyTarget, connector}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryActivity(ctx, now.Add(-time.Minute), now.Add(time.Minute), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ActivityRow{}
	for _, row := range rows {
		byID[row.EntityID] = row
	}
	if len(rows) != 6 {
		t.Fatalf("want 6 groups: %+v", rows)
	}
	if row := byID["office:reports"]; row.Type != "skill" || row.Name != "office:reports" || row.Count != 2 || row.WithSkillName != 2 {
		t.Fatalf("mixed skill history: %+v", row)
	}
	if row := byID["other:reports"]; row.Type != "skill" || row.Count != 1 {
		t.Fatalf("skill namespace collision: %+v", row)
	}
	if row := byID["mcp:research"]; row.Type != "agent" || row.Name != "Researcher" || row.AgentKind != "delegated" || row.WithAgentID != 1 {
		t.Fatalf("transfer attributed to caller: %+v", row)
	}
	if row := byID["delegate"]; row.Type != "agent" || row.Name != "delegate" || row.AgentKind != "" || row.WithAgentID != 0 {
		t.Fatalf("missing target must not inherit caller: %+v", row)
	}
	if row := byID["builtin:legacy"]; row.Type != "agent" || row.Name != "Legacy delegate" {
		t.Fatalf("legacy transfer: %+v", row)
	}
	if row := byID["search"]; row.Type != "connector" || row.Name != "search" {
		t.Fatalf("standard MCP tool name must win: %+v", row)
	}
	summary, err := s.QueryActivitySummary(ctx, now.Add(-time.Minute), now.Add(time.Minute), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.CustomAgentRuns != 0 || summary.ConfiguredAgents != 0 || summary.AgentRuns != 3 || summary.IdentifiedAgents != 2 || summary.Skills != 2 || summary.SkillLoads != 3 {
		t.Fatalf("summary includes caller or duplicate skills: %+v", summary)
	}
}
