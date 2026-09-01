import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash, useFilterNav, usePrevRange } from '../dash'
import type {
  ActiveUsersRow,
  AppAdoptionRow,
  CohortCell,
  ModelPreferenceRow,
  SessionStats,
  TimeseriesPoint,
  UserSegmentRow,
  UserStatRow,
} from '../types'
import { AppCell, KindBadge, UserCell } from '../components/UserCell'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { Heatmap } from '../components/Heatmap'
import { Bar, ChartLegend, PALETTE, TimeseriesPanel, chartOptions } from '../components/charts'
import { fmtCost, fmtTokens, fmtTime, pctChange } from '../lib/format'

const SEGMENT_COLORS: Record<string, string> = {
  power: '#818cf8',
  frequent: '#34d399',
  regular: '#fbbf24',
  casual: '#9ca3af',
}

const NEW_RETURNING_SPECS = {
  new: { label: 'New', color: '#34d399', fill: true },
  returning: { label: 'Returning', color: '#818cf8', fill: true },
}

// SegmentBars shows the engagement pyramid: user count per segment with its
// share of spend.
function SegmentBars({ rows }: { rows: UserSegmentRow[] }) {
  const max = Math.max(1, ...rows.map((r) => r.users))
  if (rows.every((r) => r.users === 0)) return <PanelMessage>No data</PanelMessage>
  return (
    <div className="space-y-3">
      {rows.map((r) => (
        <div key={r.segment}>
          <div className="mb-1 flex items-baseline justify-between text-xs">
            <span className="font-medium capitalize text-gray-700 dark:text-gray-300">{r.segment}</span>
            <span className="tabular-nums text-gray-500">
              {r.users} users · {fmtCost(r.cost)} · {fmtTokens(r.tokens)} tok
            </span>
          </div>
          <div className="h-2.5 overflow-hidden rounded-full bg-gray-100 dark:bg-gray-800">
            <div className="h-full rounded-full" style={{ width: `${(r.users / max) * 100}%`, backgroundColor: SEGMENT_COLORS[r.segment] }} />
          </div>
        </div>
      ))}
    </div>
  )
}

// ModelPreference stacks each segment's token volume by model — do power users
// prefer premium models?
function ModelPreference({ rows }: { rows: ModelPreferenceRow[] }) {
  if (rows.length === 0) return <PanelMessage>No data</PanelMessage>
  const segments = [...new Set(rows.map((r) => r.segment))]
  const models = [...new Set(rows.map((r) => r.model))]
  const byKey = new Map(rows.map((r) => [r.segment + '\0' + r.model, r.tokens]))
  const datasets = models.map((m, i) => ({
    label: m,
    data: segments.map((s) => byKey.get(s + '\0' + m) ?? 0),
    backgroundColor: PALETTE[i % PALETTE.length],
    borderRadius: 3,
  }))
  return (
    <div>
      <ChartLegend items={datasets.map((d) => ({ label: d.label, color: d.backgroundColor }))} />
      <div className="h-64">
        <Bar
          data={{ labels: segments.map((s) => s[0].toUpperCase() + s.slice(1)), datasets }}
          options={chartOptions({ yFmt: fmtTokens, stacked: true })}
        />
      </div>
    </div>
  )
}

// CohortRetention renders the weekly signup-cohort retention matrix; each cell
// is the share of the cohort still active that many weeks after signup.
function CohortRetention({ cells }: { cells: CohortCell[] }) {
  if (cells.length === 0) return <PanelMessage>No data</PanelMessage>
  const cohorts = [...new Set(cells.map((c) => c.cohort))].sort()
  const offsets = [...new Set(cells.map((c) => c.week_offset))].sort((a, b) => a - b)
  const active = new Map(cells.map((c) => [c.cohort + '\0' + c.week_offset, c.active]))
  const size = new Map(cohorts.map((co) => [co, active.get(co + '\0' + 0) ?? 0]))
  return (
    <Heatmap
      rows={cohorts}
      cols={offsets}
      corner="cohort"
      rowHeader={(co) => (
        <span>
          {fmtTime(co).replace(/,.*/, '')} <span className="text-gray-400">({size.get(co)})</span>
        </span>
      )}
      colHeader={(w) => `W${w}`}
      cell={(co, w) => {
        const a = active.get(co + '\0' + w)
        const n = size.get(co) ?? 0
        if (a === undefined || n === 0) return null
        const pct = (a / n) * 100
        return {
          bg: `rgba(52, 211, 153, ${0.12 + (pct / 100) * 0.78})`,
          text: pct > 55 ? '#06281d' : undefined,
          title: `${a}/${n} active · week ${w}`,
          content: pct.toFixed(0) + '%',
        }
      }}
    />
  )
}

function SegmentTag({ seg }: { seg: string }) {
  return (
    <span
      className="rounded px-1.5 py-0.5 text-[11px] font-medium capitalize"
      style={{ backgroundColor: (SEGMENT_COLORS[seg] ?? '#9ca3af') + '22', color: SEGMENT_COLORS[seg] ?? '#9ca3af' }}
    >
      {seg}
    </span>
  )
}

const userColumns: ColumnDef<UserStatRow, any>[] = [
  {
    accessorKey: 'id',
    header: 'User',
    cell: (c) => <UserCell r={c.row.original} />,
  },
  { id: 'kind', accessorKey: 'kind', header: 'Kind', cell: (c) => <KindBadge kind={c.getValue()} /> },
  { id: 'segment', accessorKey: 'segment', header: 'Segment', cell: (c) => <SegmentTag seg={c.getValue()} /> },
  { accessorKey: 'active_days', header: 'Active days', meta: { align: 'right' }, cell: (c) => c.getValue() },
  { accessorKey: 'requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'tokens', header: 'Tokens', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  {
    accessorKey: 'top_model',
    header: 'Top model',
    cell: (c) => <span className="font-mono text-xs text-gray-500 dark:text-gray-400">{c.getValue() || '—'}</span>,
  },
  {
    accessorKey: 'cost',
    header: 'Cost',
    meta: { align: 'right' },
    cell: (c) => <span className="font-medium text-gray-900 dark:text-white">{fmtCost(c.getValue())}</span>,
  },
]

const appColumns: ColumnDef<AppAdoptionRow, any>[] = [
  {
    accessorKey: 'app_id',
    header: 'Application',
    cell: (c) => <AppCell id={c.row.original.app_id} name={c.row.original.app_name} />,
  },
  { accessorKey: 'users', header: 'Users', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'tokens', header: 'Tokens', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  {
    accessorKey: 'cost',
    header: 'Cost',
    meta: { align: 'right' },
    cell: (c) => <span className="font-medium text-gray-900 dark:text-white">{fmtCost(c.getValue())}</span>,
  },
]

const TOKEN_AVG_SPECS = {
  input: { label: 'Avg input / request', color: '#818cf8' },
  output: { label: 'Avg output / request', color: '#34d399' },
}

export function Customers() {
  const { spanMs } = useDash()
  const prev = usePrevRange()
  const setFilter = useFilterNav()
  const active = useApi<ActiveUsersRow>('/api/genai/active-users')
  const activePrev = useApi<ActiveUsersRow>('/api/genai/active-users', { to: prev.to })
  const interactions = useApi<{ count: number }>('/api/product/interactions')
  const interactionsPrev = useApi<{ count: number }>('/api/product/interactions', prev)
  const sessions = useApi<SessionStats>('/api/product/session-stats')
  const sessionsPrev = useApi<SessionStats>('/api/product/session-stats', prev)
  const newReturning = useApi<TimeseriesPoint[]>('/api/customers/new-vs-returning')
  const sessionSeries = useApi<TimeseriesPoint[]>('/api/product/sessions-timeseries')
  const cohort = useApi<CohortCell[]>('/api/customers/cohort-retention')
  const segments = useApi<UserSegmentRow[]>('/api/customers/segments')
  const modelPref = useApi<ModelPreferenceRow[]>('/api/customers/model-preference')
  const modelMix = useApi<TimeseriesPoint[]>('/api/product/model-mix')
  const operationMix = useApi<TimeseriesPoint[]>('/api/product/operation-mix')
  const tokensPerRequest = useApi<TimeseriesPoint[]>('/api/product/tokens-per-request')
  const appAdoption = useApi<AppAdoptionRow[]>('/api/customers/app-adoption')
  const userStats = useApi<UserStatRow[]>('/api/customers/user-stats')

  const a = active.data
  const stickiness = a && a.mau > 0 ? Math.round((a.dau / a.mau) * 100) : 0
  const sessionsEmpty = !sessionSeries.loading && (sessionSeries.data ?? []).length === 0

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <div className="lg:col-span-2">
        <StatStrip
          stats={[
            {
              label: 'Active Users',
              value: String(a?.dau ?? '—'),
              sub: `${a?.wau ?? '—'} weekly · ${a?.mau ?? '—'} monthly`,
              delta: { pct: pctChange(activePrev.data?.dau ?? 0, a?.dau ?? 0), positiveIsGood: true },
            },
            {
              label: 'Interactions',
              value: fmtTokens(interactions.data?.count ?? 0),
              sub: 'LLM calls in range',
              delta: { pct: pctChange(interactionsPrev.data?.count ?? 0, interactions.data?.count ?? 0), positiveIsGood: true },
            },
            {
              label: 'Sessions',
              value: fmtTokens(sessions.data?.sessions ?? 0),
              sub: `${(sessions.data?.avg_per_user ?? 0).toFixed(1)} per user`,
              delta: { pct: pctChange(sessionsPrev.data?.sessions ?? 0, sessions.data?.sessions ?? 0), positiveIsGood: true },
            },
            { label: 'Stickiness', value: `${stickiness}%`, sub: 'daily / monthly active' },
          ]}
        />
      </div>

      <Panel title="Active Users" sub="New vs. returning users per interval">
        <TimeseriesPanel points={newReturning.data} spanMs={spanMs} specs={NEW_RETURNING_SPECS} stacked loading={newReturning.loading} />
      </Panel>

      <Panel title="Sessions" sub="Distinct conversations per interval">
        {sessionsEmpty ? (
          <PanelMessage>Needs gen_ai.conversation.id from the gateway</PanelMessage>
        ) : (
          <TimeseriesPanel
            points={sessionSeries.data}
            spanMs={spanMs}
            specs={{ '': { label: 'Sessions', color: '#22d3ee', fill: true } }}
            loading={sessionSeries.loading}
          />
        )}
      </Panel>

      <Panel
        title="Cohort Retention"
        sub="Share of each weekly signup cohort still active N weeks later (fixed 12-week lookback)"
        className="lg:col-span-2"
      >
        {cohort.loading ? <PanelMessage>Loading…</PanelMessage> : <CohortRetention cells={cohort.data ?? []} />}
      </Panel>

      <Panel title="Engagement Segments" sub="Users by request frequency, with their share of spend">
        {segments.loading ? <PanelMessage>Loading…</PanelMessage> : <SegmentBars rows={segments.data ?? []} />}
      </Panel>

      <Panel title="Model Preference by Segment" sub="Token volume by model — do power users pick premium models?">
        {modelPref.loading ? <PanelMessage>Loading…</PanelMessage> : <ModelPreference rows={modelPref.data ?? []} />}
      </Panel>

      <Panel title="Model Mix" sub="Token volume per model — which models win over time" className="lg:col-span-2">
        <TimeseriesPanel points={modelMix.data} spanMs={spanMs} yFmt={fmtTokens} stacked loading={modelMix.loading} />
      </Panel>

      <Panel title="Feature Adoption" sub="Requests per operation type">
        <TimeseriesPanel points={operationMix.data} spanMs={spanMs} yFmt={fmtTokens} stacked loading={operationMix.loading} />
      </Panel>

      <Panel title="Context Growth" sub="Average tokens per request">
        <TimeseriesPanel points={tokensPerRequest.data} spanMs={spanMs} specs={TOKEN_AVG_SPECS} yFmt={fmtTokens} loading={tokensPerRequest.loading} />
      </Panel>

      <Panel
        title="Application Adoption"
        sub="Reach and spend per application (service.peer.name) · click a row to filter"
        className="lg:col-span-2"
      >
        {appAdoption.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (appAdoption.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable
            data={appAdoption.data!}
            columns={appColumns}
            initialSort={[{ id: 'cost', desc: true }]}
            onRowClick={(r) => r.app_id && setFilter({ app: [r.app_id] })}
          />
        )}
      </Panel>

      <Panel
        title="Users"
        sub="Per-user activity, engagement segment and spend · click a row to filter"
        className="lg:col-span-2"
      >
        {userStats.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (userStats.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable
            data={userStats.data!}
            columns={userColumns}
            initialSort={[{ id: 'cost', desc: true }]}
            initialLimit={25}
            onRowClick={(r) => r.id && setFilter({ user: [r.id] })}
          />
        )}
      </Panel>
    </div>
  )
}
