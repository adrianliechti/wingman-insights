package mcpserver

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"insights/internal/store"
)

type ActivityInput struct {
	RangeFilters
	ModelFilters
	View     string `json:"view,omitempty" jsonschema:"summary (default), agents, mcp, plugins, or builtin. Agents are custom non-plugin configured agents only, without private names or IDs. Summary returns headline totals only."`
	Interval string `json:"interval,omitempty" jsonschema:"auto, hour, or day adds trend points for agents, mcp, plugins, or builtin. auto chooses hour up to 48 hours, otherwise day. Cannot be combined with group_by. Activity trends include zero-filled intervals, meaning no matching stored executions, not guaranteed absence of usage."`
	GroupBy  string `json:"group_by,omitempty" jsonschema:"With view=mcp: server or tool; with view=plugins: plugin; with view=builtin: tool. Adds groups ranked by executions. Not supported for summary or agents. Cannot be combined with interval."`
	Limit    int    `json:"limit,omitempty" jsonschema:"With group_by: number of ranked groups, 1 to 100, default 20. Also bounds skill details within each plugin. Totals cover all groups."`
}

type ActivityTotals struct {
	DistinctCustomAgents *int64        `json:"distinct_custom_agents,omitempty"`
	AgentRuns            *int64        `json:"agent_runs,omitempty"`
	MCPToolCalls         *int64        `json:"mcp_tool_calls,omitempty"`
	PluginExecutions     *int64        `json:"plugin_executions,omitempty"`
	BuiltinToolCalls     *int64        `json:"builtin_tool_calls,omitempty"`
	FirstObservedRuns    *int64        `json:"first_observed_runs,omitempty" jsonschema:"Agents trend only. First recorded identified runs in retained history, not agent creation."`
	OtherRuns            *int64        `json:"other_runs,omitempty" jsonschema:"Agents trend only. Repeat runs and runs without a known agent ID."`
	MostUsedMCPTool      *ActivityTool `json:"most_used_mcp_tool,omitempty"`
}

// Deliberately project execution rows instead of serializing store.ActivityRow:
// it also carries private agent identities and trace references.
type ActivityExecution struct {
	Executions    int64   `json:"executions"`
	Succeeded     int64   `json:"succeeded"`
	Failed        int64   `json:"failed"`
	Unreported    int64   `json:"unreported"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
	LastUsed      string  `json:"last_used"`
}

type ActivityTool struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ServerID   string `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	PluginID   string `json:"plugin_id,omitempty"`
	ActivityExecution
}

type ActivitySkill struct {
	Name string `json:"name"`
	ActivityExecution
}

type ActivityGroup struct {
	ActivityTool
	Skills          []ActivitySkill `json:"skills,omitempty" jsonschema:"Plugin groups only. Only explicitly plugin-owned skill names are returned."`
	TotalSkills     int             `json:"total_skills,omitempty"`
	SkillsTruncated bool            `json:"skills_truncated,omitempty"`
}

type ActivityPoint struct {
	Time       string `json:"time"`
	Series     string `json:"series"`
	Executions int64  `json:"executions"`
}

type ActivityData struct {
	Totals      ActivityTotals  `json:"totals"`
	Groups      []ActivityGroup `json:"groups,omitempty"`
	TotalGroups int             `json:"total_groups,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
	Points      []ActivityPoint `json:"points,omitempty"`
}

func activityCount(n int64) *int64 { return &n }

func activityExecution(r store.ActivityRow) ActivityExecution {
	return ActivityExecution{Executions: r.Count, Succeeded: r.SuccessCount, Failed: r.ErrorCount,
		Unreported: r.Count - r.SuccessCount - r.ErrorCount, AvgDurationMS: round(r.AvgDuration*1000, 2),
		LastUsed: r.LastSeen.UTC().Format(time.RFC3339Nano)}
}

func activityTool(r store.ActivityRow, servers []store.ActivityRow) ActivityTool {
	tool := ActivityTool{ID: r.EntityID, Name: r.Name, PluginID: r.PluginID, ActivityExecution: activityExecution(r)}
	if r.Type == "connector" || r.Type == "builtin" {
		tool.Name = strings.ReplaceAll(activityMCPName.ReplaceAllString(r.Name, "$1"), "_", " ")
	}
	if r.Type == "connector" || r.Type == "mcp_server" {
		tool.ServerID = r.Server
		for _, server := range servers {
			if server.Server == r.Server && server.PluginID == r.PluginID {
				tool.ServerName = server.Name
				break
			}
		}
	}
	return tool
}

var activityMCPName = regexp.MustCompile(`^mcp__.+?__(.+)_[0-9a-f]{8}$`)

func (t tools) activity(ctx context.Context, in ActivityInput) (Response[ActivityData], error) {
	view := in.View
	if view == "" {
		view = "summary"
	}
	switch view {
	case "summary", "agents", "mcp", "plugins", "builtin":
	default:
		return Response[ActivityData]{}, inputError("view must be summary, agents, mcp, plugins, or builtin")
	}
	if in.GroupBy != "" && in.Interval != "" {
		return Response[ActivityData]{}, inputError("group_by cannot be combined with interval")
	}
	if view == "summary" && (in.GroupBy != "" || in.Interval != "") {
		return Response[ActivityData]{}, inputError("summary supports totals only; select agents, mcp, plugins, or builtin")
	}
	if in.GroupBy != "" && !((view == "mcp" && (in.GroupBy == "server" || in.GroupBy == "tool")) || (view == "plugins" && in.GroupBy == "plugin") || (view == "builtin" && in.GroupBy == "tool")) {
		return Response[ActivityData]{}, inputError("group_by must be server or tool for mcp, plugin for plugins, or tool for builtin; agents cannot be grouped")
	}
	q, from, to, f, err := t.query(in.RangeFilters, in.ModelFilters)
	if err != nil {
		return Response[ActivityData]{}, err
	}
	iv, err := resolveInterval(in.Interval, true, from, to)
	if err != nil {
		return Response[ActivityData]{}, err
	}
	limit, err := rowLimit(in.Limit)
	if err != nil {
		return Response[ActivityData]{}, err
	}
	q.View, q.GroupBy = view, in.GroupBy
	out := Response[ActivityData]{Query: q, Warnings: []string{}}
	summary, err := t.store.QueryActivitySummary(ctx, from, to, f)
	if err != nil {
		return out, err
	}
	var executions int64
	if view == "summary" || view == "agents" {
		out.Data.Totals.DistinctCustomAgents = activityCount(summary.ConfiguredAgents)
		out.Data.Totals.AgentRuns = activityCount(summary.CustomAgentRuns)
		executions += summary.CustomAgentRuns
		if summary.IdentifiedCustomRuns < summary.CustomAgentRuns {
			out.Warnings = append(out.Warnings, "Some custom-agent runs have no agent ID and are excluded from the distinct-agent count.")
		}
	}
	if view == "summary" || view == "mcp" {
		out.Data.Totals.MCPToolCalls = activityCount(summary.ConnectorCalls)
		executions += summary.ConnectorCalls
	}
	if view == "summary" || view == "plugins" {
		out.Data.Totals.PluginExecutions = activityCount(summary.PluginCalls)
		executions += summary.PluginCalls
	}
	if view == "summary" || view == "builtin" {
		out.Data.Totals.BuiltinToolCalls = activityCount(summary.BuiltinCalls)
		executions += summary.BuiltinCalls
	}
	if executions == 0 {
		out.Warnings = append(out.Warnings, "No matching activity.")
	}
	if iv != "" {
		return t.activityTrend(ctx, out, from, to, iv, view, f)
	}
	// These rows may contain private identities. Only approved tool and plugin
	// projections below may leave this handler.
	if view == "agents" || view == "builtin" && in.GroupBy == "" || view == "plugins" && in.GroupBy == "" {
		return out, nil
	}
	rows, err := t.store.QueryActivity(ctx, from, to, f)
	if err != nil {
		return out, err
	}
	var servers []store.ActivityRow
	if view == "mcp" || view == "summary" {
		servers, err = t.store.QueryMCPServerActivity(ctx, from, to, f)
		if err != nil {
			return out, err
		}
		var candidates []store.ActivityRow
		for _, r := range rows {
			if r.Type == "connector" {
				candidates = append(candidates, r)
			}
		}
		sortActivityRows(candidates)
		if len(candidates) > 0 {
			top := activityTool(candidates[0], servers)
			out.Data.Totals.MostUsedMCPTool = &top
		}
	}
	if in.GroupBy == "" {
		return out, nil
	}
	var groups []store.ActivityRow
	switch {
	case view == "mcp" && in.GroupBy == "server":
		groups = servers
	case view == "plugins":
		groups, err = t.store.QueryPluginActivity(ctx, from, to, f)
		if err != nil {
			return out, err
		}
	default:
		for _, r := range rows {
			if view == "mcp" && r.Type == "connector" || view == "builtin" && r.Type == "builtin" {
				groups = append(groups, r)
			}
		}
	}
	sortActivityRows(groups)
	out.Data.TotalGroups = len(groups)
	if len(groups) > limit {
		groups = groups[:limit]
		out.Data.Truncated = true
	}
	for _, r := range groups {
		group := ActivityGroup{ActivityTool: activityTool(r, servers)}
		if view == "plugins" {
			var skills []store.ActivityRow
			for _, skill := range rows {
				if skill.Type == "skill" && skill.PluginID != "" && skill.PluginID == r.PluginID {
					skills = append(skills, skill)
				}
			}
			sortActivityRows(skills)
			group.TotalSkills = len(skills)
			if len(skills) > limit {
				skills = skills[:limit]
				group.SkillsTruncated = true
			}
			for _, skill := range skills {
				group.Skills = append(group.Skills, ActivitySkill{Name: skill.Name, ActivityExecution: activityExecution(skill)})
			}
		}
		out.Data.Groups = append(out.Data.Groups, group)
	}
	return out, nil
}

func sortActivityRows(rows []store.ActivityRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.EntityID != b.EntityID {
			return a.EntityID < b.EntityID
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.PluginID < b.PluginID
	})
}

func (t tools) activityTrend(ctx context.Context, out Response[ActivityData], from, to time.Time, iv, view string, f store.Filter) (Response[ActivityData], error) {
	var points []store.TimeseriesPoint
	var err error
	if view == "agents" {
		points, err = t.store.QueryCustomAgentTimeseries(ctx, from, to, iv, f)
	} else {
		by := view
		if by == "plugins" {
			by = "plugin"
		}
		points, err = t.store.QueryActivityBreakdownTimeseries(ctx, from, to, iv, by, f)
	}
	if err != nil {
		return out, err
	}
	if len(points) > maxPoints {
		return out, inputError("trend exceeds 2000 points; use day or narrow the range and filters")
	}
	out.Query.Interval = strings.TrimPrefix(iv, "1 ")
	if view == "agents" {
		var first, other int64
		for _, p := range points {
			if p.Label == "first_observed" {
				first += int64(p.Value)
			} else {
				other += int64(p.Value)
			}
		}
		out.Data.Totals.FirstObservedRuns, out.Data.Totals.OtherRuns = activityCount(first), activityCount(other)
		out.Data.Totals.AgentRuns = activityCount(first + other)
	} else {
		var total int64
		for _, p := range points {
			total += int64(p.Value)
		}
		switch view {
		case "mcp":
			out.Data.Totals.MCPToolCalls = activityCount(total)
		case "plugins":
			out.Data.Totals.PluginExecutions = activityCount(total)
		case "builtin":
			out.Data.Totals.BuiltinToolCalls = activityCount(total)
		}
	}
	for _, p := range points {
		out.Data.Points = append(out.Data.Points, ActivityPoint{Time: p.Bucket.UTC().Format(time.RFC3339), Series: p.Label, Executions: int64(p.Value)})
	}
	if len(points) > 0 {
		out.Warnings = append(out.Warnings, bucketWarnings(from, to, iv)...)
	}
	return out, nil
}
