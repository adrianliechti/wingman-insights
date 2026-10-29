import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useApi, useDash } from '../dash'
import type { ActivityRow, ActivitySummary, TimeseriesPoint } from '../types'
import { DataTable, type DataTableColumns } from '../components/DataTable'
import { Panel, PanelLoadingRows, PanelMessage, Stale } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { SegToggle, TimeseriesPanel, CHART } from '../components/charts'
import { fmtDuration, fmtTime, fmtTokens } from '../lib/format'
import { calendarChartRange, calendarActivitySeries } from '../lib/chartBuckets'

const labels: Record<ActivityRow['type'], string> = {
  agent: 'Agent run', skill: 'Skill load', connector: 'MCP tool', mcp_server: 'MCP server', builtin: 'Built-in tool', tool: 'Other tool', plugin: 'Plugin', custom_agent: 'Custom agent', custom_skill: 'Custom skill',
}
const chartTypes = ['agents', 'mcp', 'plugins', 'builtin'] as const
type ChartType = (typeof chartTypes)[number]
const chartLabels: Record<ChartType, string> = { agents: 'Agents', mcp: 'MCP', plugins: 'Plugins', builtin: 'Built-in tools' }

const columnWidths = ['27%', '13%', '9%', '12%', '14%', '10%', '15%'] as const
const tableMinWidth = 896

function displayName(row: ActivityRow) {
  return row.name.replace(/^mcp__.+?__(.+)_[0-9a-f]{8}$/, '$1').replaceAll('_', ' ')
}

function rowKey(row: ActivityRow) {
  return JSON.stringify([row.type, row.entity_id, row.server, row.plugin_id, row.agent_kind])
}

function isGroup(row: ActivityRow) {
  return row.type === 'plugin' || row.type === 'mcp_server'
}

function groupRegionId(row: ActivityRow) {
  return `activity-group-${encodeURIComponent(rowKey(row))}`
}

function reportedCount(count: number, identified: number, total: number) {
  if (total > 0 && identified === 0) return 'Not reported'
  return fmtTokens(count)
}

function countLabel(count: number, label: string) {
  return `${fmtTokens(count)} ${label}${count === 1 ? '' : 's'}`
}

export function Activity() {
  const { to, spanMs } = useDash()
  const chartRange = calendarChartRange(to, spanMs)
  const period = { from: chartRange.from, to: new Date(new Date(to).getTime() - 1).toISOString() }
  const [chartType, setChartType] = useState<ChartType>('agents')
  const activity = useApi<ActivityRow[]>('/api/ops/activity', period)
  const overview = useApi<ActivitySummary>('/api/ops/activity-summary', period)
  const plugins = useApi<ActivityRow[]>('/api/ops/plugin-activity', period)
  const servers = useApi<ActivityRow[]>('/api/ops/mcp-server-activity', period)
  const hourlyPeriod = { ...period, interval: '1 hour' }
  const agentHistory = useApi<TimeseriesPoint[]>(chartType === 'agents' ? '/api/ops/custom-agent-timeseries' : null, hourlyPeriod)
  const mcpHistory = useApi<TimeseriesPoint[]>(chartType === 'mcp' ? '/api/ops/activity-breakdown-timeseries' : null, { ...hourlyPeriod, by: 'mcp' })
  const pluginHistory = useApi<TimeseriesPoint[]>(chartType === 'plugins' ? '/api/ops/activity-breakdown-timeseries' : null, { ...hourlyPeriod, by: 'plugin' })
  const builtinHistory = useApi<TimeseriesPoint[]>(chartType === 'builtin' ? '/api/ops/activity-breakdown-timeseries' : null, { ...hourlyPeriod, by: 'builtin' })
  const histories = { agents: agentHistory, mcp: mcpHistory, plugins: pluginHistory, builtin: builtinHistory }
  const history = histories[chartType]
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())
  const rows = activity.data ?? []
  const pluginRows = plugins.data ?? []
  const serverRows = servers.data ?? []
  const mostUsedMcpTool = rows.filter((row) => row.type === 'connector').sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))[0]
  const mostUsedMcpServer = mostUsedMcpTool && serverRows.find((row) => row.server === mostUsedMcpTool.server && row.plugin_id === mostUsedMcpTool.plugin_id)
  const builtins = rows.filter((row) => row.type === 'builtin')
  const rowsByChart: Record<ChartType, ActivityRow[]> = {
    agents: [],
    mcp: serverRows,
    plugins: pluginRows,
    builtin: builtins,
  }
  const visible = rowsByChart[chartType]
  const tableSource = chartType === 'mcp' ? servers : chartType === 'plugins' ? plugins : activity
  const { loading, firstLoad, stale } = tableSource
  const skillsByPlugin = new Map<string, ActivityRow[]>()
  for (const row of rows) {
    if (row.type !== 'skill' || row.plugin_id.trim() === '') continue
    const skills = skillsByPlugin.get(row.plugin_id) ?? []
    skills.push(row)
    skillsByPlugin.set(row.plugin_id, skills)
  }
  const childrenByGroup = new Map<string, ActivityRow[]>()
  for (const row of pluginRows) childrenByGroup.set(rowKey(row), skillsByPlugin.get(row.plugin_id) ?? [])
  for (const server of serverRows) childrenByGroup.set(rowKey(server), rows.filter((row) => row.type === 'connector' && row.server === server.server && row.plugin_id === server.plugin_id))
  const toggleGroup = (id: string) => setExpandedGroups((previous) => {
    const next = new Set(previous)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })
  const calls = visible.reduce((sum, row) => sum + row.count, 0)
  const successes = visible.reduce((sum, row) => sum + row.success_count, 0)
  const errors = visible.reduce((sum, row) => sum + row.error_count, 0)
  const unknown = calls - successes - errors

  const columns: DataTableColumns<ActivityRow> = [
    {
      id: 'name', accessorFn: displayName, header: () => <span className="pl-6">Name</span>,
      cell: (cell) => {
        const row = cell.row.original
        const group = isGroup(row)
        const child = row.type === 'connector' || row.type === 'skill'
        const expanded = expandedGroups.has(rowKey(row))
        const nameContent = (
          <>
            <span className="flex h-5 w-4 items-center justify-center" aria-hidden="true">
              {group && (expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />)}
            </span>
            <span className="min-w-0 break-words leading-5">{displayName(row)}</span>
          </>
        )
        const metadata = [
          row.server && row.type !== 'connector' && row.name !== row.server ? row.server : '',
          row.plugin_id && row.type !== 'plugin' && row.type !== 'connector' && row.type !== 'skill' ? `Plugin: ${row.plugin_id}` : '',
        ].filter(Boolean)
        return (
          <div className={`grid min-w-48 grid-cols-[1rem_minmax(0,1fr)] gap-x-2 gap-y-1 ${child ? 'pl-6' : ''}`}>
            {!group ? (
              <div className={`col-span-2 grid grid-cols-subgrid items-start ${child ? 'font-normal text-gray-700 dark:text-gray-300' : 'font-medium text-gray-900 dark:text-gray-100'}`}>{nameContent}</div>
            ) : (
              <button
                type="button"
                onClick={() => toggleGroup(rowKey(row))}
                aria-expanded={expanded}
                aria-controls={groupRegionId(row)}
                className="col-span-2 grid cursor-pointer grid-cols-subgrid items-start text-left font-semibold text-gray-900 hover:text-indigo-600 dark:text-gray-100 dark:hover:text-indigo-400"
                title={row.name}
              >
                {nameContent}
              </button>
            )}
            {metadata.length > 0 && (
              <div className="col-start-2 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs leading-4 text-gray-500 dark:text-gray-400">
                {metadata.map((value) => <span key={value} className="break-all">{value}</span>)}
              </div>
            )}
          </div>
        )
      },
    },
    { accessorKey: 'type', header: 'Type', cell: (cell) => labels[cell.row.original.type] },
    { accessorKey: 'count', header: 'Calls', meta: { align: 'right' }, cell: (cell) => fmtTokens(cell.getValue()) },
    { accessorKey: 'avg_duration', header: 'Avg time', meta: { align: 'right' }, cell: (cell) => cell.row.original.count > 0 ? fmtDuration(cell.getValue()) : '—' },
    { accessorKey: 'users', header: 'Users', meta: { align: 'right' }, cell: (cell) => {
      const row = cell.row.original
      return <span className={row.count > 0 && row.with_user === 0 ? 'text-gray-400 dark:text-gray-500' : ''} title={`${row.with_user} of ${row.count} calls have user identity`}>{reportedCount(row.users, row.with_user, row.count)}</span>
    } },
    {
      accessorKey: 'error_count', header: 'Errors', meta: { align: 'right' },
      cell: (cell) => <span className={cell.getValue() > 0 ? 'font-medium text-red-600 dark:text-red-400' : 'text-gray-400 dark:text-gray-500'}>{fmtTokens(cell.getValue())}</span>,
    },
    { accessorKey: 'last_seen', header: 'Last used', cell: (cell) => <span className="whitespace-nowrap">{cell.row.original.count > 0 ? fmtTime(cell.getValue()) : '—'}</span> },
  ]
  const skillColumns: DataTableColumns<ActivityRow> = columns
    .map((column) => 'accessorKey' in column && column.accessorKey === 'count' ? { ...column, header: 'Loads' } : column)

  if (activity.error || overview.error || plugins.error || servers.error) {
    return <Panel title="AI activity"><p role="alert" className="text-sm text-red-600 dark:text-red-400">Could not load activity. Refresh to try again.</p></Panel>
  }

  return (
    <div className="space-y-5">
      <StatStrip
        loading={overview.loading}
        firstLoad={overview.firstLoad}
        stats={[
          { label: 'Distinct custom agents used', value: overview.data ? reportedCount(overview.data.configured_agents, overview.data.identified_custom_agent_runs, overview.data.custom_agent_runs) : '—', sub: countLabel(overview.data?.custom_agent_runs ?? 0, 'agent run') },
          { label: 'MCP tool calls', value: overview.data ? fmtTokens(overview.data.connector_calls) : '—', sub: !activity.loading && !servers.loading && mostUsedMcpTool?.count > 0 ? `Most used: ${mostUsedMcpServer?.name || mostUsedMcpTool.server || 'Unidentified MCP server'} - ${displayName(mostUsedMcpTool)}` : undefined },
        ]}
      />
      <Panel
        title="What was used"
        sub={chartType === 'agents' ? 'First observed marks an agent’s earliest recorded run, not its creation date.' : chartType === 'plugins' ? 'Attributed executions, including skill loads.' : undefined}
        className="[&>div:first-child]:flex-wrap"
        action={<div className="max-w-full overflow-x-auto" role="group" aria-label="Chart activity type"><SegToggle value={chartType} options={chartTypes} labels={chartLabels} onChange={setChartType} /></div>}
      >
        {history.error ? <p role="alert" className="text-sm text-red-600 dark:text-red-400">Could not load activity history. Refresh to try again.</p> : (
          <TimeseriesPanel key={chartType} points={calendarActivitySeries(history.data, chartRange)} spanMs={spanMs} range={chartRange} loading={history.loading} yMin={0} yStepSize={1} yFmt={fmtTokens} stacked kind="bar" showTotal legendAlways specs={chartType === 'agents' ? { first_observed: { label: 'First observed runs', color: CHART.turquoise }, other_runs: { label: 'Other runs', color: CHART.blue } } : undefined} />
        )}
        {chartType !== 'agents' && (
          <div className="mt-6">
            {chartType === 'plugins' && pluginRows.length > 0 && <p className="mb-3 text-xs text-gray-500">Skill loads are included in plugin totals.</p>}
            {firstLoad ? <PanelLoadingRows /> : visible.length === 0 ? (
              <Stale when={stale}><PanelMessage>{chartType === 'plugins' ? 'No plugin-attributed calls in this range' : 'No observed calls in this range'}</PanelMessage></Stale>
            ) : (
              <>
                <DataTable
                  key={chartType} data={visible} columns={columns} loading={loading} initialSort={[{ id: 'count', desc: true }]} initialLimit={25} verticalAlign="top"
                  columnWidths={columnWidths} minWidth={tableMinWidth} expandedRowClassName="p-0"
                  rowClassName={(row) => isGroup(row) && expandedGroups.has(rowKey(row)) ? 'bg-indigo-50/60 dark:bg-indigo-500/10' : ''}
                  renderExpandedRow={(row) => isGroup(row) && expandedGroups.has(rowKey(row)) ? (
                    <div id={groupRegionId(row)} role="region" aria-label={`${row.type === 'plugin' ? 'Skills' : 'Tools'} used from ${displayName(row)}`} className="relative bg-gray-50/80 dark:bg-gray-900/40 before:pointer-events-none before:absolute before:inset-y-0 before:left-6 before:w-px before:bg-indigo-200 dark:before:bg-indigo-800">
                      {(childrenByGroup.get(rowKey(row)) ?? []).length === 0 ? (
                        <p className="px-9 py-4 text-sm text-gray-500">{row.type === 'plugin' ? 'No plugin skill loads recorded in this period' : 'No MCP tool calls recorded in this period'}</p>
                      ) : (
                        <DataTable nested data={childrenByGroup.get(rowKey(row))!} columns={row.type === 'plugin' ? skillColumns : columns} initialSort={[{ id: 'count', desc: true }]} initialLimit={10} verticalAlign="top" columnWidths={columnWidths} minWidth={tableMinWidth} />
                      )}
                    </div>
                  ) : null}
                />
                <Stale when={stale}>
                  <div className="mt-4 flex flex-wrap justify-end gap-2 text-xs text-gray-500" aria-live="polite">
                    <span>{fmtTokens(successes)} succeeded · {fmtTokens(errors)} failed{unknown > 0 && ` · ${fmtTokens(unknown)} other / unreported`}</span>
                  </div>
                </Stale>
              </>
            )}
          </div>
        )}
      </Panel>

    </div>
  )
}
