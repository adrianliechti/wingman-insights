import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import type { ActiveUsersRow, SessionStats, TimeseriesPoint, UserTokenSummaryRow } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { TimeseriesPanel } from '../components/charts'
import { fmtTokens } from '../lib/format'

interface UserAgg {
  enduser_id: string
  enduser_email: string
  input: number
  output: number
  cache_creation: number
  cache_read: number
  total: number
  requests: number
}

function aggregate(rows: UserTokenSummaryRow[]): UserAgg[] {
  const map = new Map<string, UserAgg>()
  for (const r of rows) {
    let u = map.get(r.enduser_id)
    if (!u) {
      u = {
        enduser_id: r.enduser_id,
        enduser_email: r.enduser_email,
        input: 0,
        output: 0,
        cache_creation: 0,
        cache_read: 0,
        total: 0,
        requests: 0,
      }
      map.set(r.enduser_id, u)
    }
    switch (r.token_type) {
      case 'input':
        u.input += r.total_tokens
        u.requests += r.total_requests
        break
      case 'output':
        u.output += r.total_tokens
        break
      case 'cache_creation':
        u.cache_creation += r.total_tokens
        break
      case 'cache_read':
        u.cache_read += r.total_tokens
        break
    }
    u.total += r.total_tokens
  }
  return [...map.values()]
}

const userColumns: ColumnDef<UserAgg, any>[] = [
  {
    accessorKey: 'enduser_id',
    header: 'User',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue()}</span>,
  },
  { accessorKey: 'enduser_email', header: 'Email', cell: (c) => c.getValue() || '—' },
  { accessorKey: 'requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  {
    accessorKey: 'input',
    header: 'Input',
    meta: { align: 'right' },
    cell: (c) => <span className="text-indigo-500 dark:text-indigo-400">{fmtTokens(c.getValue())}</span>,
  },
  {
    accessorKey: 'output',
    header: 'Output',
    meta: { align: 'right' },
    cell: (c) => <span className="text-emerald-500 dark:text-emerald-400">{fmtTokens(c.getValue())}</span>,
  },
  {
    accessorKey: 'cache_read',
    header: 'Cache Read',
    meta: { align: 'right' },
    cell: (c) => <span className="text-cyan-500 dark:text-cyan-400">{fmtTokens(c.getValue())}</span>,
  },
  {
    accessorKey: 'total',
    header: 'Total',
    meta: { align: 'right' },
    cell: (c) => <span className="font-medium text-gray-900 dark:text-white">{fmtTokens(c.getValue())}</span>,
  },
]

const TOKEN_AVG_SPECS = {
  input: { label: 'Avg input / request', color: '#818cf8' },
  output: { label: 'Avg output / request', color: '#34d399' },
}

export function Product() {
  const { spanMs } = useDash()
  const active = useApi<ActiveUsersRow>('/api/genai/active-users')
  const activeSeries = useApi<TimeseriesPoint[]>('/api/genai/active-users-timeseries')
  const sessions = useApi<SessionStats>('/api/product/session-stats')
  const sessionSeries = useApi<TimeseriesPoint[]>('/api/product/sessions-timeseries')
  const modelMix = useApi<TimeseriesPoint[]>('/api/product/model-mix')
  const operationMix = useApi<TimeseriesPoint[]>('/api/product/operation-mix')
  const tokensPerRequest = useApi<TimeseriesPoint[]>('/api/product/tokens-per-request')
  const userSummary = useApi<UserTokenSummaryRow[]>('/api/genai/user-summary')

  const a = active.data
  const stickiness = a && a.mau > 0 ? Math.round((a.dau / a.mau) * 100) : 0

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <div className="lg:col-span-2">
        <StatStrip
          stats={[
            {
              label: 'Active Users',
              value: String(a?.dau ?? '—'),
              sub: `${a?.wau ?? '—'} weekly · ${a?.mau ?? '—'} monthly`,
            },
            { label: 'Stickiness', value: `${stickiness}%`, sub: 'daily / monthly active' },
            {
              label: 'Sessions',
              value: fmtTokens(sessions.data?.sessions ?? 0),
              sub: `${(sessions.data?.avg_per_user ?? 0).toFixed(1)} per user`,
            },
            {
              label: 'Tokens per Session',
              value: fmtTokens(sessions.data?.avg_tokens_per_session ?? 0),
            },
          ]}
        />
      </div>

      <Panel title="Active Users" sub="Distinct user.id values per interval">
        <TimeseriesPanel
          points={activeSeries.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Users', color: '#818cf8', fill: true } }}
          loading={activeSeries.loading}
        />
      </Panel>

      <Panel title="Sessions" sub="Distinct session.id values per interval">
        <TimeseriesPanel
          points={sessionSeries.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Sessions', color: '#22d3ee', fill: true } }}
          loading={sessionSeries.loading}
        />
      </Panel>

      <Panel title="Model Mix" sub="Token volume per model — which models win over time" className="lg:col-span-2">
        <TimeseriesPanel points={modelMix.data} spanMs={spanMs} yFmt={fmtTokens} stacked loading={modelMix.loading} />
      </Panel>

      <Panel title="Feature Adoption" sub="Requests per operation type">
        <TimeseriesPanel
          points={operationMix.data}
          spanMs={spanMs}
          yFmt={fmtTokens}
          stacked
          loading={operationMix.loading}
        />
      </Panel>

      <Panel title="Context Growth" sub="Average tokens per request">
        <TimeseriesPanel
          points={tokensPerRequest.data}
          spanMs={spanMs}
          specs={TOKEN_AVG_SPECS}
          yFmt={fmtTokens}
          loading={tokensPerRequest.loading}
        />
      </Panel>

      <Panel title="Token Consumption per User" className="lg:col-span-2">
        {userSummary.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (userSummary.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={aggregate(userSummary.data!)} columns={userColumns} initialSort={[{ id: 'total', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
