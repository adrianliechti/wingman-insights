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

// Token composition stacks the disjoint token partitions from spans: non-cached
// input, cache read/write (subsets of input), output and reasoning (subset of
// output). "Cache write" tracks the forthcoming read/write cache naming.
const COMPOSITION_SPECS = {
  input: { label: 'Input', color: '#818cf8', fill: true },
  cache_read: { label: 'Cache read', color: '#22d3ee', fill: true },
  cache_creation: { label: 'Cache write', color: '#fbbf24', fill: true },
  output: { label: 'Output', color: '#34d399', fill: true },
  reasoning: { label: 'Reasoning', color: '#f472b6', fill: true },
}

export function Overview() {
  const { spanMs } = useDash()
  const summary = useApi<TokenSummaryRow[]>('/api/genai/token-summary')
  const costs = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })
  const active = useApi<ActiveUsersRow>('/api/genai/active-users')
  const operations = useApi<OperationRow[]>('/api/genai/operations')
  const errors = useApi<GenAIErrorRow[]>('/api/genai/errors')
  const duration = useApi<TimeseriesPoint[]>('/api/genai/operation-duration-timeseries')
  const composition = useApi<TimeseriesPoint[]>('/api/genai/token-composition')
  const cache = useApi<TimeseriesPoint[]>('/api/genai/cache-efficiency')
  const reasoning = useApi<TimeseriesPoint[]>('/api/genai/reasoning-share')

  const rows = summary.data ?? []
  const totalsByType = (type: string) =>
    rows.filter((r) => r.token_type === type).reduce((acc, r) => acc + r.total_tokens, 0)
  const input = totalsByType('input')
  const output = totalsByType('output')
  const total = rows.reduce((acc, r) => acc + r.total_tokens, 0)
  const requests = rows.filter((r) => r.token_type === 'input').reduce((acc, r) => acc + r.total_requests, 0)
  const spend = (costs.data ?? []).reduce((acc, r) => acc + r.total_cost, 0)
  const saved = (costs.data ?? []).reduce((acc, r) => acc + r.cache_savings, 0)

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <div className="lg:col-span-2">
        <StatStrip
          stats={[
            {
              label: 'Tokens',
              value: fmtTokens(total),
              sub: `${fmtTokens(input)} in · ${fmtTokens(output)} out`,
            },
            { label: 'Est. Cost', value: fmtCost(spend), sub: saved > 0 ? `${fmtCost(saved)} saved by cache` : 'models.dev pricing' },
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

      <Panel
        title="Token Composition"
        sub="Where tokens go — cache and reasoning broken out (from spans)"
        className="lg:col-span-2"
      >
        <TimeseriesPanel
          points={composition.data}
          spanMs={spanMs}
          specs={COMPOSITION_SPECS}
          yFmt={fmtTokens}
          stacked
          loading={composition.loading}
        />
      </Panel>

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

      <Panel title="Operation Duration" sub="Average duration per model" className="lg:col-span-2">
        <TimeseriesPanel
          points={duration.data}
          spanMs={spanMs}
          yFmt={(v) => fmtDuration(v)}
          loading={duration.loading}
        />
      </Panel>

      <Panel title="Cache Hit Rate" sub="Share of input tokens served from cache">
        <TimeseriesPanel
          points={cache.data}
          spanMs={spanMs}
          yFmt={(v) => v.toFixed(0) + '%'}
          specs={{ '': { label: 'Cache read share', color: '#22d3ee', fill: true } }}
          loading={cache.loading}
        />
      </Panel>

      <Panel title="Reasoning Share" sub="Share of output tokens spent on reasoning">
        <TimeseriesPanel
          points={reasoning.data}
          spanMs={spanMs}
          yFmt={(v) => v.toFixed(0) + '%'}
          specs={{ '': { label: 'Reasoning share', color: '#f472b6', fill: true } }}
          loading={reasoning.loading}
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
