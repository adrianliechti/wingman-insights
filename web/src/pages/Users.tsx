import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import type { ActiveUsersRow, TimeseriesPoint, UserTokenSummaryRow } from '../types'
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

const columns: ColumnDef<UserAgg, any>[] = [
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
    accessorKey: 'cache_creation',
    header: 'Cache Create',
    meta: { align: 'right' },
    cell: (c) => <span className="text-amber-500 dark:text-amber-400">{fmtTokens(c.getValue())}</span>,
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

export function Users() {
  const { spanMs } = useDash()
  const active = useApi<ActiveUsersRow>('/api/genai/active-users')
  const timeseries = useApi<TimeseriesPoint[]>('/api/genai/active-users-timeseries')
  const summary = useApi<UserTokenSummaryRow[]>('/api/genai/user-summary')

  return (
    <div className="grid grid-cols-1 gap-5">
      <StatStrip
        stats={[
          { label: 'Daily Active', value: String(active.data?.dau ?? '—') },
          { label: 'Weekly Active', value: String(active.data?.wau ?? '—') },
          { label: 'Monthly Active', value: String(active.data?.mau ?? '—') },
        ]}
      />

      <Panel title="Active Users" sub="Distinct user.id values per interval">
        <TimeseriesPanel
          points={timeseries.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Distinct users', color: '#818cf8', fill: true } }}
          loading={timeseries.loading}
        />
      </Panel>

      <Panel title="Token Consumption per User">
        {summary.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (summary.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={aggregate(summary.data!)} columns={columns} initialSort={[{ id: 'total', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
