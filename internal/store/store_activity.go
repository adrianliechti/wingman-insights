package store

import (
	"context"
	"time"
)

// ActivityRow describes observed executions, not an inventory of installed
// agents, skills or plugins. A named agent on a model span is not an agent run.
type ActivityRow struct {
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	Server        string    `json:"server"`
	Count         int64     `json:"count"`
	SuccessCount  int64     `json:"success_count"`
	ErrorCount    int64     `json:"error_count"`
	AvgDuration   float64   `json:"avg_duration"`
	LastSeen      time.Time `json:"last_seen"`
	TraceID       string    `json:"trace_id"`
	TraceName     string    `json:"trace_name"`
	WithUser      int64     `json:"with_user"`
	WithAgentID   int64     `json:"with_agent_id"`
	WithSkillName int64     `json:"with_skill_name"`
	WithPlugin    int64     `json:"with_plugin"`
	EntityID      string    `json:"entity_id"`
	AgentKind     string    `json:"agent_kind"`
	PluginID      string    `json:"plugin_id"`
	Users         int64     `json:"users"`
	Conversations int64     `json:"conversations"`
	WithConnector int64     `json:"with_connector"`
}

func activityCTE(from, to time.Time, f Filter) (string, []any) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	r := dirResolve("named", "user_id", "user_email")
	return `
		WITH events AS (
			SELECT *,
				COALESCE(NULLIF(attributes ->> '$."gen_ai.transfer.target.name"', '') IS NOT NULL
					OR attributes ->> '$."gen_ai.transfer.mode"' IN ('return_to_caller', 'pass_control'), false) AS is_transfer,
				COALESCE(attributes ->> '$."tanstack.ai.tool.outcome"', attributes ->> '$."wingman.agent.outcome"', '') AS outcome,
				COALESCE(attributes ->> '$."wingman.mcp.server.id"', attributes ->> '$."server.address"', '') AS server,
				CASE WHEN is_transfer
					THEN COALESCE(attributes ->> '$."wingman.transfer.target.id"', '')
					ELSE COALESCE(attributes ->> '$."gen_ai.agent.id"', '') END AS agent_id,
				CASE WHEN is_transfer
					THEN attributes ->> '$."gen_ai.transfer.target.name"'
					ELSE COALESCE(agent_name, '') END AS activity_agent_name,
				CASE WHEN is_transfer
					THEN COALESCE(attributes ->> '$."wingman.transfer.target.kind"', '')
					ELSE COALESCE(attributes ->> '$."wingman.agent.kind"', '') END AS agent_kind,
				COALESCE(NULLIF(attributes ->> '$."gen_ai.skill.name"', ''), attributes ->> '$."wingman.skill.id"', '') AS skill_id,
				COALESCE(attributes ->> '$."wingman.skill.source"', 'unknown') AS skill_source,
				COALESCE(NULLIF(attributes ->> '$."gen_ai.skill.name"', ''), attributes ->> '$."wingman.skill.name"', attributes ->> '$."wingman.skill.id"', '') AS skill_name,
				COALESCE(attributes ->> '$."wingman.plugin.id"', '') AS plugin_id,
				CASE
					WHEN is_transfer
						OR attributes ->> '$."wingman.activity.kind"' = 'agent'
						OR operation_name = 'invoke_agent' OR tool_name = 'agent'
						OR regexp_matches(COALESCE(tool_name, ''), '^mcp__.+___agent_[0-9a-f]{8}$') THEN 'agent'
					WHEN tool_name = 'load_skill' THEN 'skill'
					WHEN starts_with(COALESCE(tool_name, ''), 'mcp__')
						OR attributes ->> '$."mcp.method.name"' = 'tools/call'
						OR NULLIF(attributes ->> '$."wingman.mcp.server.id"', '') IS NOT NULL THEN 'connector'
					WHEN tool_name IN (
						'execute_script', 'ask_questions', 'create_image', 'read_skill_resource',
						'list_skills', 'create_skill', 'update_skill', 'write_skill_resource', 'delete_skill_resource', 'delete_skill',
						'artifacts_read', 'artifacts_create', 'artifacts_edit', 'artifacts_delete', 'artifacts_move', 'artifacts_grep', 'artifacts_glob',
						'repository_read', 'repository_grep', 'repository_glob', 'repository_search'
					) THEN 'builtin'
					ELSE 'tool'
				END AS activity_type
			FROM genai_spans
			WHERE time >= ? AND time <= ?
				AND (NULLIF(tool_name, '') IS NOT NULL OR operation_name = 'invoke_agent')` + clause + `
			QUALIFY COALESCE(trace_id, '') = '' OR COALESCE(span_id, '') = ''
				OR ROW_NUMBER() OVER (PARTITION BY trace_id, span_id ORDER BY received_at DESC) = 1
		), named AS (
			SELECT *,
				CASE activity_type
					WHEN 'agent' THEN COALESCE(NULLIF(activity_agent_name, ''), NULLIF(agent_id, ''), NULLIF(tool_name, ''), 'Unidentified agent')
					WHEN 'skill' THEN COALESCE(NULLIF(skill_name, ''), 'Unidentified skill')
					ELSE COALESCE(NULLIF(attributes ->> '$."gen_ai.tool.name"', ''), NULLIF(attributes ->> '$."mcp.tool.name"', ''), tool_name)
				END AS activity_name,
				CASE activity_type
					WHEN 'agent' THEN COALESCE(NULLIF(agent_id, ''), NULLIF(activity_agent_name, ''), NULLIF(tool_name, ''), 'Unidentified agent')
					WHEN 'skill' THEN COALESCE(NULLIF(skill_id, ''), NULLIF(plugin_id, '') || ':' || NULLIF(skill_name, ''), NULLIF(skill_name, ''), 'Unidentified skill')
					ELSE COALESCE(NULLIF(attributes ->> '$."gen_ai.tool.name"', ''), NULLIF(attributes ->> '$."mcp.tool.name"', ''), tool_name)
				END AS entity_id,
				COALESCE(status = 'error', false) OR COALESCE(error_type, '') != '' OR outcome IN ('error', 'failed') AS failed
			FROM events
		), facts AS (
			SELECT named.*, ` + r.ID + ` AS principal, ` + r.Kind + ` AS principal_kind
			FROM named` + r.Join + `
		)
`, args
}

func (s *Store) QueryActivity(ctx context.Context, from, to time.Time, f Filter) ([]ActivityRow, error) {
	return s.queryActivity(ctx, from, to, f, "tool")
}

// Plugin executions overlap with agents/skills/connectors; never add these
// rows to the capability totals. Ownership is explicit, not guessed from names.
func (s *Store) QueryPluginActivity(ctx context.Context, from, to time.Time, f Filter) ([]ActivityRow, error) {
	return s.queryActivity(ctx, from, to, f, "plugin")
}

func (s *Store) QueryMCPServerActivity(ctx context.Context, from, to time.Time, f Filter) ([]ActivityRow, error) {
	return s.queryActivity(ctx, from, to, f, "mcp_server")
}

// Only aggregate rows leave this endpoint: private identities and trace links
// remain stored, but are not part of the custom capability presentation.
func (s *Store) QueryCustomActivity(ctx context.Context, from, to time.Time, f Filter) ([]ActivityRow, error) {
	rows, err := s.queryActivity(ctx, from, to, f, "custom")
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for i := range rows {
		rows[i].TraceID, rows[i].TraceName = "", ""
		found[rows[i].Type] = true
	}
	for _, row := range []ActivityRow{
		{Type: "custom_agent", Name: "Custom agents", EntityID: "custom_agent"},
		{Type: "custom_skill", Name: "Custom skills", EntityID: "custom_skill"},
	} {
		if !found[row.Type] {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (s *Store) queryActivity(ctx context.Context, from, to time.Time, f Filter, grouping string) ([]ActivityRow, error) {
	cte, args := activityCTE(from, to, f)
	switch grouping {
	case "plugin":
		cte += `, grouped AS (SELECT * REPLACE ('plugin' AS activity_type, plugin_id AS activity_name,
			plugin_id AS entity_id, '' AS server, '' AS agent_kind) FROM facts WHERE plugin_id != '')`
	case "mcp_server":
		cte += `, grouped AS (SELECT * REPLACE ('mcp_server' AS activity_type,
			COALESCE(NULLIF(attributes ->> '$."wingman.mcp.server.name"', ''), NULLIF(server, ''), 'Unidentified MCP server') AS activity_name,
			server AS entity_id, '' AS agent_kind) FROM facts WHERE activity_type = 'connector')`
	case "custom":
		cte += `, grouped AS (SELECT * REPLACE (
			CASE WHEN activity_type = 'agent' THEN 'custom_agent' ELSE 'custom_skill' END AS activity_type,
			CASE WHEN activity_type = 'agent' THEN 'Custom agents' ELSE 'Custom skills' END AS activity_name,
			CASE WHEN activity_type = 'agent' THEN 'custom_agent' ELSE 'custom_skill' END AS entity_id,
			'' AS server, '' AS agent_kind)
			FROM facts WHERE plugin_id = '' AND (
				(activity_type = 'agent' AND agent_kind = 'configured')
				OR (activity_type = 'skill' AND skill_source = 'personal')))`
	default:
		cte += `, grouped AS (SELECT * FROM facts)`
	}
	rows, err := s.db.QueryContext(ctx, cte+`
		SELECT activity_type, arg_max(activity_name, time), server, COUNT(*),
			COUNT(*) FILTER (WHERE NOT COALESCE(failed, false) AND (status = 'ok' OR outcome = 'success')),
			COUNT(*) FILTER (WHERE failed), AVG(duration), MAX(time),
			arg_max(trace_id, time), arg_max(name, time),
			COUNT(*) FILTER (WHERE principal != '' AND principal_kind != 'application'),
			COUNT(*) FILTER (WHERE agent_id != ''),
			COUNT(*) FILTER (WHERE skill_name != ''),
			COUNT(*) FILTER (WHERE plugin_id != ''),
			entity_id, agent_kind, plugin_id,
			COUNT(DISTINCT NULLIF(principal, '')) FILTER (WHERE principal_kind != 'application'),
			COUNT(DISTINCT NULLIF(session_id, '')),
			COUNT(*) FILTER (WHERE server != '')
		FROM grouped
		GROUP BY activity_type, entity_id, server, agent_kind, plugin_id
		ORDER BY COUNT(*) DESC, activity_type, entity_id, server
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ActivityRow{}
	for rows.Next() {
		var row ActivityRow
		if err := rows.Scan(&row.Type, &row.Name, &row.Server, &row.Count,
			&row.SuccessCount, &row.ErrorCount, &row.AvgDuration, &row.LastSeen,
			&row.TraceID, &row.TraceName, &row.WithUser, &row.WithAgentID,
			&row.WithSkillName, &row.WithPlugin, &row.EntityID, &row.AgentKind,
			&row.PluginID, &row.Users, &row.Conversations, &row.WithConnector); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type ActivitySummary struct {
	Agents               int64 `json:"agents"`
	ConfiguredAgents     int64 `json:"configured_agents"`
	CustomAgentRuns      int64 `json:"custom_agent_runs"`
	IdentifiedCustomRuns int64 `json:"identified_custom_agent_runs"`
	AgentRuns            int64 `json:"agent_runs"`
	IdentifiedAgents     int64 `json:"identified_agent_runs"`
	Skills               int64 `json:"skills"`
	SkillLoads           int64 `json:"skill_loads"`
	IdentifiedSkills     int64 `json:"identified_skill_loads"`
	Connectors           int64 `json:"connectors"`
	ConnectorCalls       int64 `json:"connector_calls"`
	IdentifiedConnectors int64 `json:"identified_connector_calls"`
	BuiltinCalls         int64 `json:"builtin_calls"`
	Plugins              int64 `json:"plugins"`
	PluginCalls          int64 `json:"plugin_calls"`
	Users                int64 `json:"users"`
	Calls                int64 `json:"calls"`
	IdentifiedCalls      int64 `json:"identified_calls"`
}

func (s *Store) QueryActivitySummary(ctx context.Context, from, to time.Time, f Filter) (*ActivitySummary, error) {
	cte, args := activityCTE(from, to, f)
	var result ActivitySummary
	err := s.db.QueryRowContext(ctx, cte+`
		SELECT COUNT(DISTINCT NULLIF(agent_id, '')) FILTER (WHERE activity_type = 'agent'),
			COUNT(DISTINCT NULLIF(agent_id, '')) FILTER (WHERE activity_type = 'agent' AND agent_kind = 'configured' AND plugin_id = ''),
			COUNT(*) FILTER (WHERE activity_type = 'agent' AND agent_kind = 'configured' AND plugin_id = ''),
			COUNT(*) FILTER (WHERE activity_type = 'agent' AND agent_kind = 'configured' AND plugin_id = '' AND agent_id != ''),
			COUNT(*) FILTER (WHERE activity_type = 'agent'),
			COUNT(*) FILTER (WHERE activity_type = 'agent' AND agent_id != ''),
			COUNT(DISTINCT entity_id) FILTER (WHERE activity_type = 'skill' AND skill_name != ''),
			COUNT(*) FILTER (WHERE activity_type = 'skill'),
			COUNT(*) FILTER (WHERE activity_type = 'skill' AND skill_name != ''),
			COUNT(DISTINCT NULLIF(server, '')) FILTER (WHERE activity_type = 'connector'),
			COUNT(*) FILTER (WHERE activity_type = 'connector'),
			COUNT(*) FILTER (WHERE activity_type = 'connector' AND server != ''),
			COUNT(*) FILTER (WHERE activity_type = 'builtin'),
			COUNT(DISTINCT NULLIF(plugin_id, '')), COUNT(*) FILTER (WHERE plugin_id != ''),
			COUNT(DISTINCT NULLIF(principal, '')) FILTER (WHERE principal_kind != 'application'),
			COUNT(*), COUNT(*) FILTER (WHERE principal != '' AND principal_kind != 'application')
		FROM facts`, args...).Scan(&result.Agents, &result.ConfiguredAgents, &result.CustomAgentRuns, &result.IdentifiedCustomRuns, &result.AgentRuns,
		&result.IdentifiedAgents, &result.Skills, &result.SkillLoads, &result.IdentifiedSkills,
		&result.Connectors, &result.ConnectorCalls, &result.IdentifiedConnectors, &result.BuiltinCalls,
		&result.Plugins, &result.PluginCalls, &result.Users, &result.Calls, &result.IdentifiedCalls)
	return &result, err
}

// Run counts share the pooled custom-agent scope. First-observed counts use all
// retained history before applying the selected range and filters, so an agent
// returning after an earlier run is not counted as new. These are not creation events.
func (s *Store) QueryCustomAgentTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	cte, args := activityCTE(time.Unix(0, 0), to, Filter{})
	clause, fargs := f.spansClause()
	args = append(args, from, to)
	args = append(args, fargs...)
	args = append(args, interval, interval, from, to, interval, from, interval, to, interval)
	return s.queryTimeseries(ctx, cte+`, custom_facts AS (
		SELECT * FROM facts
		WHERE activity_type = 'agent' AND agent_kind = 'configured' AND plugin_id = ''
	), first_seen AS (
		SELECT agent_id, MIN(time) AS first_time FROM custom_facts
		WHERE agent_id != '' GROUP BY agent_id
	), selected AS (
		SELECT * FROM custom_facts WHERE time >= ? AND time <= ?`+clause+`
	), usage AS (
		SELECT time_bucket(CAST(? AS INTERVAL), time) AS bucket, COUNT(*) AS value
		FROM selected GROUP BY bucket
	), new_usage AS (
		SELECT time_bucket(CAST(? AS INTERVAL), first_time) AS bucket, COUNT(*) AS value
		FROM first_seen WHERE first_time >= ? AND first_time <= ?
			AND EXISTS (SELECT 1 FROM selected WHERE selected.agent_id = first_seen.agent_id AND selected.time = first_seen.first_time)
		GROUP BY bucket
	), buckets AS (
		SELECT unnest(generate_series(
			time_bucket(CAST(? AS INTERVAL), CAST(? AS TIMESTAMP)),
			time_bucket(CAST(? AS INTERVAL), CAST(? AS TIMESTAMP)),
			CAST(? AS INTERVAL))) AS bucket
	)
	SELECT buckets.bucket, 'other_runs' AS label,
		COALESCE(usage.value, 0) - COALESCE(new_usage.value, 0),
		COALESCE(usage.value, 0) - COALESCE(new_usage.value, 0)
	FROM buckets LEFT JOIN usage USING (bucket) LEFT JOIN new_usage USING (bucket)
	UNION ALL
	SELECT buckets.bucket, 'first_observed' AS label, COALESCE(new_usage.value, 0), COALESCE(new_usage.value, 0)
	FROM buckets LEFT JOIN new_usage USING (bucket)
	ORDER BY bucket, label`, args...)
}

// Capability usage counts executions, not configured catalog entries. Plugin
// attribution overlaps with skill/tool usage and is a separate chart view.
func (s *Store) QueryActivityTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	cte, args := activityCTE(from, to, f)
	args = append(args, interval, interval, interval, from, interval, to, interval)
	return s.queryTimeseries(ctx, cte+`, usage AS (
		SELECT time_bucket(CAST(? AS INTERVAL), time) AS bucket,
			CASE activity_type WHEN 'skill' THEN 'skills' WHEN 'connector' THEN 'mcp' ELSE 'builtin' END AS label,
			COUNT(*) AS value
		FROM facts WHERE activity_type IN ('skill', 'connector', 'builtin') GROUP BY bucket, label
		UNION ALL
		SELECT time_bucket(CAST(? AS INTERVAL), time) AS bucket, 'plugins' AS label, COUNT(*) AS value
		FROM facts WHERE plugin_id != '' GROUP BY bucket
	), buckets AS (
		SELECT unnest(generate_series(
			time_bucket(CAST(? AS INTERVAL), CAST(? AS TIMESTAMP)),
			time_bucket(CAST(? AS INTERVAL), CAST(? AS TIMESTAMP)),
			CAST(? AS INTERVAL))) AS bucket
	), series AS (
		SELECT bucket, label FROM buckets CROSS JOIN (VALUES ('skills'), ('mcp'), ('plugins'), ('builtin')) AS categories(label)
	)
	SELECT series.bucket, series.label, COALESCE(usage.value, 0), COALESCE(usage.value, 0)
	FROM series LEFT JOIN usage USING (bucket, label) ORDER BY bucket, label`, args...)
}

// Breakdowns group by stable identity and use the most recent display name,
// matching the parent rows in the usage table. Only observed entities appear.
func (s *Store) QueryActivityBreakdownTimeseries(ctx context.Context, from, to time.Time, interval, by string, f Filter) ([]TimeseriesPoint, error) {
	cte, args := activityCTE(from, to, f)
	selection := `SELECT time, server AS entity_id, plugin_id,
		COALESCE(NULLIF(attributes ->> '$."wingman.mcp.server.name"', ''), NULLIF(server, ''), 'Unidentified MCP server') AS display_name
		FROM facts WHERE activity_type = 'connector'`
	if by == "plugin" {
		selection = `SELECT time, plugin_id AS entity_id, '' AS plugin_id, plugin_id AS display_name
			FROM facts WHERE plugin_id != ''`
	} else if by == "builtin" {
		selection = `SELECT time, entity_id, '' AS plugin_id, replace(activity_name, '_', ' ') AS display_name
			FROM facts WHERE activity_type = 'builtin'`
	}
	args = append(args, interval, interval, from, interval, to, interval)
	return s.queryTimeseries(ctx, cte+`, selected AS (`+selection+`), entities AS (
		SELECT entity_id, plugin_id, arg_max(display_name, time) AS display_name
		FROM selected GROUP BY entity_id, plugin_id
	), labels AS (
		SELECT entity_id, plugin_id,
			CASE WHEN COUNT(*) OVER (PARTITION BY display_name) > 1
					THEN display_name || ' (' || entity_id || CASE WHEN plugin_id != '' THEN ' / ' || plugin_id ELSE '' END || ')'
					ELSE display_name END AS label
		FROM entities
	), usage AS (
		SELECT time_bucket(CAST(? AS INTERVAL), time) AS bucket, entity_id, plugin_id, COUNT(*) AS value
		FROM selected GROUP BY bucket, entity_id, plugin_id
	), buckets AS (
		SELECT unnest(generate_series(
			time_bucket(CAST(? AS INTERVAL), CAST(? AS TIMESTAMP)),
			time_bucket(CAST(? AS INTERVAL), CAST(? AS TIMESTAMP)),
			CAST(? AS INTERVAL))) AS bucket
	), series AS (SELECT bucket, entity_id, plugin_id, label FROM buckets CROSS JOIN labels)
	SELECT series.bucket, series.label, COALESCE(usage.value, 0), COALESCE(usage.value, 0)
	FROM series LEFT JOIN usage USING (bucket, entity_id, plugin_id) ORDER BY bucket, label`, args...)
}
