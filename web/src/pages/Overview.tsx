import { CircleCheck } from 'lucide-react'
import { useApi, useDash } from '../dash'
import type {
  ActiveUsersRow,
  CostRow,
  GenAIErrorRow,
  ModelDistributionRow,
  OperationRow,
  TimeseriesPoint,
  TokenSummaryRow,
} from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { TokenChart } from '../components/TokenChart'
import { ChartLegend, Doughnut, PALETTE, TimeseriesPanel } from '../components/charts'
import { fmtCost, fmtDuration, fmtTokens } from '../lib/format'
import type { ColumnDef } from '@tanstack/react-table'

function ModelDistributionChart() {
  const { data, loading } = useApi<ModelDistributionRow[]>('/api/genai/model-distribution')
  if (loading) return <PanelMessage>Loading…</PanelMessage>
  if (!data || data.length === 0) return <PanelMessage>No data</PanelMessage>
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4">
      <div className="h-48 w-48">
        <Doughnut
          data={{
            labels: data.map((d) => d.request_model),
            datasets: [
              {
                data: data.map((d) => d.total_requests),
                backgroundColor: PALETTE,
                borderWidth: 0,
              },
            ],
          }}
          options={{
            responsive: true,
            maintainAspectRatio: false,
            plugins: { legend: { display: false } },
          }}
        />
      </div>
      <ChartLegend
        align="center"
        items={data.map((d, i) => ({ label: d.request_model, color: PALETTE[i % PALETTE.length] }))}
      />
    </div>
  )
}

const operationColumns: ColumnDef<OperationRow, any>[] = [
  { accessorKey: 'operation_name', header: 'Operation' },
  { accessorKey: 'request_model', header: 'Model' },
  { accessorKey: 'total_count', header: 'Count', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'avg_duration', header: 'Avg Duration', meta: { align: 'right' }, cell: (c) => fmtDuration(c.getValue()) },
]

const errorColumns: ColumnDef<GenAIErrorRow, any>[] = [
  {
    accessorKey: 'error_type',
    header: 'Error',
    cell: (c) => (
      <span className="rounded bg-red-500/10 px-1.5 py-0.5 font-mono text-xs text-red-500 dark:text-red-400">{c.getValue()}</span>
    ),
  },
  { accessorKey: 'request_model', header: 'Model' },
  { accessorKey: 'count', header: 'Count', meta: { align: 'right' } },
]

export function Overview() {
  const { spanMs } = useDash()
  const summary = useApi<TokenSummaryRow[]>('/api/genai/token-summary')
  const costs = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })
  const active = useApi<ActiveUsersRow>('/api/genai/active-users')
  const operations = useApi<OperationRow[]>('/api/genai/operations')
  const errors = useApi<GenAIErrorRow[]>('/api/genai/errors')
  const duration = useApi<TimeseriesPoint[]>('/api/genai/operation-duration-timeseries')
  const cache = useApi<TimeseriesPoint[]>('/api/genai/cache-efficiency')

  const rows = summary.data ?? []
  const totalsByType = (type: string) =>
    rows.filter((r) => r.token_type === type).reduce((acc, r) => acc + r.total_tokens, 0)
  const input = totalsByType('input')
  const output = totalsByType('output')
  const cacheRead = totalsByType('cache_read')
  const total = rows.reduce((acc, r) => acc + r.total_tokens, 0)
  const requests = rows.filter((r) => r.token_type === 'input').reduce((acc, r) => acc + r.total_requests, 0)
  const spend = (costs.data ?? []).reduce((acc, r) => acc + r.total_cost, 0)

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <div className="lg:col-span-2">
        <StatStrip
          stats={[
            {
              label: 'Tokens',
              value: fmtTokens(total),
              sub: `${fmtTokens(input)} in · ${fmtTokens(output)} out · ${fmtTokens(cacheRead)} cached`,
            },
            { label: 'Est. Cost', value: fmtCost(spend), sub: 'models.dev pricing' },
            { label: 'Requests', value: fmtTokens(requests) },
            {
              label: 'Active Users',
              value: String(active.data?.dau ?? '—'),
              sub: `${active.data?.wau ?? '—'} weekly · ${active.data?.mau ?? '—'} monthly`,
            },
          ]}
        />
      </div>

      <TokenChart className="lg:col-span-2" />

      <Panel title="Models" sub="Requests per model and operation breakdown" className="lg:col-span-2">
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[18rem_1fr]">
          <ModelDistributionChart />
          {operations.loading ? (
            <PanelMessage>Loading…</PanelMessage>
          ) : (operations.data ?? []).length === 0 ? (
            <PanelMessage>No data</PanelMessage>
          ) : (
            <DataTable data={operations.data!} columns={operationColumns} initialSort={[{ id: 'total_count', desc: true }]} />
          )}
        </div>
      </Panel>

      <Panel title="Operation Duration" sub="Average duration per model">
        <TimeseriesPanel
          points={duration.data}
          spanMs={spanMs}
          yFmt={(v) => fmtDuration(v)}
          loading={duration.loading}
        />
      </Panel>

      <Panel title="Cache Hit Rate" sub="Share of tokens served from cache">
        <TimeseriesPanel
          points={cache.data}
          spanMs={spanMs}
          yFmt={(v) => v.toFixed(0) + '%'}
          specs={{ '': { label: 'Cache read share', color: '#22d3ee', fill: true } }}
          loading={cache.loading}
        />
      </Panel>

      <Panel title="Errors" sub="GenAI request failures by type and model" className="lg:col-span-2">
        {errors.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (errors.data ?? []).length === 0 ? (
          <PanelMessage>
            <CircleCheck className="h-4 w-4 text-emerald-500" />
            No errors
          </PanelMessage>
        ) : (
          <DataTable data={errors.data!} columns={errorColumns} initialSort={[{ id: 'count', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
