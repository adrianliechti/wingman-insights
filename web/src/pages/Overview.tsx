import { useMemo } from 'react'
import { useApi, useDash } from '../dash'
import type {
  ActiveUsersRow,
  CostRow,
  ModelDistributionRow,
  OperationRow,
  TimeseriesPoint,
  TokenSummaryRow,
} from '../types'
import { Panel, PanelLoading, PanelMessage } from '../components/Panel'
import { DataTable } from '../components/DataTable'
import { TokenChart } from '../components/TokenChart'
import { ChartLegend, Doughnut, PALETTE, TimeseriesPanel, CHART } from '../components/charts'
import { fmtCost, fmtDuration, fmtTokens } from '../lib/format'
import type { ColumnDef } from '@tanstack/react-table'

function ModelDistributionChart() {
  const { data, loading } = useApi<ModelDistributionRow[]>('/api/genai/model-distribution')
  if (loading) return <PanelLoading />
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

// Spend is plotted as one total series. The endpoint returns a row per model,
// but which model the money went to is the FinOps page's question; here it only
// has to answer how much, and whether it is trending.
const SPEND_SPECS = {
  spend: { label: 'Spend', color: CHART.blue, fill: true },
}

// Health is summarised with percentiles rather than an average: an average
// duration hides the slow tail that users actually notice. The full latency
// breakdown (TTFC, throughput, HTTP) lives on Operations.
const PERCENTILE_SPECS = {
  p50: { label: 'p50', color: CHART.positive },
  p95: { label: 'p95', color: CHART.warning },
  p99: { label: 'p99', color: CHART.negative },
}

export function Overview() {
  const { spanMs } = useDash()
  const summary = useApi<TokenSummaryRow[]>('/api/genai/token-summary')
  const costs = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })
  const active = useApi<ActiveUsersRow>('/api/genai/active-users')
  const operations = useApi<OperationRow[]>('/api/genai/operations')
  const costTrend = useApi<TimeseriesPoint[]>('/api/finops/cost-timeseries', { by: 'model' })
  const percentiles = useApi<TimeseriesPoint[]>('/api/ops/latency-percentiles')
  const errorRate = useApi<TimeseriesPoint[]>('/api/ops/error-rate')

  // Sum the per-model rows into a single spend series, keeping buckets ordered
  // so the partial-interval detection in groupSeries can infer the interval.
  const spendSeries: TimeseriesPoint[] = useMemo(() => {
    const byBucket = new Map<string, number>()
    for (const p of costTrend.data ?? []) byBucket.set(p.bucket, (byBucket.get(p.bucket) ?? 0) + p.value)
    return [...byBucket.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([bucket, value]) => ({ bucket, label: 'spend', value, count: 0 }))
  }, [costTrend.data])

  const rows = summary.data ?? []
  const totalsByType = (type: string) =>
    rows.filter((r) => r.token_type === type).reduce((acc, r) => acc + r.total_tokens, 0)
  const input = totalsByType('input')
  const output = totalsByType('output')
  const total = rows.reduce((acc, r) => acc + r.total_tokens, 0)
  const requests = rows.filter((r) => r.token_type === 'input').reduce((acc, r) => acc + r.total_requests, 0)
  const spend = (costs.data ?? []).reduce((acc, r) => acc + r.total_cost, 0)
  const saved = (costs.data ?? []).reduce((acc, r) => acc + r.cache_savings, 0)
  const headlineLoading = costs.loading || summary.loading || active.loading

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <div className="lg:col-span-2">
        <div className="flex flex-col gap-6 rounded-lg border border-gray-200 px-6 py-5 sm:flex-row sm:items-center sm:justify-between dark:border-gray-800">
          <div>
            <p className="text-xs font-medium uppercase tracking-wider text-gray-500">Estimated Cost</p>
            {headlineLoading ? (
              <div className="mt-2 h-11 w-40 rounded-md shimmer" />
            ) : (
              <p className="mt-1 text-5xl font-bold tracking-tight tabular-nums text-gray-900 dark:text-white">
                {fmtCost(spend)}
              </p>
            )}
            <p className="mt-1 text-sm text-gray-500">
              {saved > 0 ? `${fmtCost(saved)} saved by cache · models.dev pricing` : 'models.dev pricing'}
            </p>
          </div>
          <div className="grid grid-cols-3 gap-x-8 gap-y-3 sm:text-right">
            {[
              { label: 'Tokens', value: fmtTokens(total), sub: `${fmtTokens(input)} in · ${fmtTokens(output)} out` },
              { label: 'Requests', value: fmtTokens(requests) },
              {
                label: 'Active Users',
                value: String(active.data?.dau ?? '—'),
                sub: `${active.data?.wau ?? '—'} wk · ${active.data?.mau ?? '—'} mo`,
              },
            ].map((s) => (
              <div key={s.label}>
                <p className="text-xs font-medium uppercase tracking-wider text-gray-500">{s.label}</p>
                {headlineLoading ? (
                  <>
                    <div className="mt-1 h-7 w-20 rounded shimmer sm:ml-auto" />
                    {s.sub && <div className="mt-1.5 h-3.5 w-24 rounded shimmer sm:ml-auto" />}
                  </>
                ) : (
                  <>
                    <p className="mt-0.5 text-xl font-semibold tabular-nums tracking-tight text-gray-900 dark:text-white">
                      {s.value}
                    </p>
                    {s.sub && <p className="mt-0.5 text-xs text-gray-500">{s.sub}</p>}
                  </>
                )}
              </div>
            ))}
          </div>
        </div>
      </div>

      <TokenChart className="lg:col-span-2" />

      <Panel
        title="Spend over Time"
        sub="Estimated cost per interval, priced from spans — unpriced models are not counted"
        className="lg:col-span-2"
      >
        <TimeseriesPanel
          points={spendSeries}
          spanMs={spanMs}
          specs={SPEND_SPECS}
          yFmt={fmtCost}
          loading={costTrend.loading}
        />
      </Panel>

      <Panel title="Models" sub="Requests per model and operation breakdown" className="lg:col-span-2">
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[18rem_1fr]">
          <ModelDistributionChart />
          {operations.firstLoad ? (
            <PanelLoading />
          ) : (operations.data ?? []).length === 0 ? (
            <PanelMessage>No data</PanelMessage>
          ) : (
            <DataTable loading={operations.loading} data={operations.data!} columns={operationColumns} initialSort={[{ id: 'total_count', desc: true }]} />
          )}
        </div>
      </Panel>

      <Panel title="Latency" sub="Per-request durations from spans — p99 is the slow tail users notice">
        <TimeseriesPanel
          points={percentiles.data}
          spanMs={spanMs}
          specs={PERCENTILE_SPECS}
          yFmt={(v) => fmtDuration(v)}
          loading={percentiles.loading}
        />
      </Panel>

      <Panel title="Error Rate" sub="Share of failed GenAI operations">
        <TimeseriesPanel
          points={errorRate.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Errors', color: CHART.negative, fill: true } }}
          yFmt={(v) => v.toFixed(2) + '%'}
          yMin={0}
          loading={errorRate.loading}
        />
      </Panel>
    </div>
  )
}
