import { useState } from 'react'
import { Download } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash, usePrevRange } from '../dash'
import { apiUrl } from '../api'
import type { BudgetResponse, CostRow, TimeseriesPoint } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { ChartLegend, Doughnut, PALETTE, TimeseriesPanel } from '../components/charts'
import { fmtCost, fmtTokens, pctChange } from '../lib/format'

function costCols<T extends CostRow>(): ColumnDef<T, any>[] {
  return [
    { accessorKey: 'input_tokens', header: 'Input', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
    { accessorKey: 'output_tokens', header: 'Output', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
    {
      id: 'cached_tokens',
      header: 'Cached',
      meta: { align: 'right' },
      accessorFn: (r: T) => r.cache_read_tokens + r.cache_creation_tokens,
      cell: (c) => fmtTokens(c.getValue()),
    },
    {
      accessorKey: 'reasoning_tokens',
      header: 'Reasoning',
      meta: { align: 'right' },
      cell: (c) => <span className="text-pink-500 dark:text-pink-400">{fmtTokens(c.getValue())}</span>,
    },
    {
      accessorKey: 'cache_savings',
      header: 'Cache Saved',
      meta: { align: 'right' },
      cell: (c) => <span className="text-emerald-600 dark:text-emerald-400">{fmtCost(c.getValue())}</span>,
    },
    {
      id: 'effective_rate',
      header: 'Eff. $/1M',
      meta: { align: 'right' },
      // Blended price per million billed tokens (reasoning is inside output).
      accessorFn: (r: T) => {
        const tokens = r.input_tokens + r.output_tokens + r.cache_read_tokens + r.cache_creation_tokens
        return tokens > 0 ? (r.total_cost / tokens) * 1_000_000 : 0
      },
      cell: (c) => <span className="text-gray-500 dark:text-gray-400">{fmtCost(c.getValue())}</span>,
    },
    {
      accessorKey: 'total_cost',
      header: 'Total',
      meta: { align: 'right' },
      cell: (c) => {
        const row = c.row.original as CostRow
        return (
          <span className="font-medium text-gray-900 dark:text-white">
            {fmtCost(c.getValue())}
            {!row.priced && (
              <span
                className="ml-1.5 rounded bg-amber-500/10 px-1 py-0.5 text-[10px] font-medium text-amber-600 dark:text-amber-400"
                title="No models.dev price for this model — tokens counted, cost excluded"
              >
                unpriced
              </span>
            )}
          </span>
        )
      },
    },
  ]
}

const userColumns: ColumnDef<CostRow, any>[] = [
  {
    accessorKey: 'enduser_id',
    header: 'User',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue() || 'unattributed'}</span>,
  },
  { accessorKey: 'enduser_email', header: 'Email', cell: (c) => c.getValue() || '—' },
  ...costCols(),
]

// SegToggle is a compact segmented control for switching a chart dimension.
function SegToggle<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T
  options: readonly T[]
  onChange: (v: T) => void
}) {
  return (
    <div className="flex rounded-lg border border-gray-200 p-0.5 dark:border-gray-800">
      {options.map((g) => (
        <button
          key={g}
          onClick={() => onChange(g)}
          className={`rounded-md px-2.5 py-1 text-xs font-medium capitalize transition-colors ${
            value === g
              ? 'bg-indigo-600 text-white'
              : 'text-gray-500 hover:text-gray-900 dark:hover:text-white'
          }`}
        >
          {g}
        </button>
      ))}
    </div>
  )
}

// SpendDoughnut shows cost share across a dimension (app or model).
function SpendDoughnut({ rows, labelOf }: { rows: CostRow[]; labelOf: (r: CostRow) => string }) {
  const sorted = [...rows].filter((r) => r.total_cost > 0).sort((a, b) => b.total_cost - a.total_cost)
  if (sorted.length === 0) return <PanelMessage>No data</PanelMessage>
  return (
    <div className="flex flex-col items-center gap-3">
      <div className="h-44 w-44">
        <Doughnut
          data={{
            labels: sorted.map(labelOf),
            datasets: [{ data: sorted.map((r) => r.total_cost), backgroundColor: PALETTE, borderWidth: 0 }],
          }}
          options={{
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
              legend: { display: false },
              tooltip: { callbacks: { label: (ctx: any) => ` ${ctx.label}: ${fmtCost(ctx.parsed)}` } },
            },
          }}
        />
      </div>
      <ChartLegend align="center" items={sorted.map((r, i) => ({ label: labelOf(r), color: PALETTE[i % PALETTE.length] }))} />
    </div>
  )
}

export function Finops() {
  const { spanMs } = useDash()
  const prev = usePrevRange()
  const [spendBy, setSpendBy] = useState<'model' | 'app'>('model')
  const [trendMetric, setTrendMetric] = useState<'cost' | 'tokens'>('cost')
  const byUser = useApi<CostRow[]>('/api/genai/costs', { group_by: 'user' })
  const byApp = useApi<CostRow[]>('/api/genai/costs', { group_by: 'app' })
  const byModel = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })
  const byModelPrev = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model', ...prev })
  const budget = useApi<BudgetResponse>('/api/finops/budget')
  const trend = useApi<TimeseriesPoint[]>('/api/finops/cost-timeseries', { by: spendBy })
  const tokenTrend = useApi<TimeseriesPoint[]>('/api/finops/token-timeseries', { by: spendBy })

  const models = byModel.data ?? []
  const total = models.reduce((acc, r) => acc + r.total_cost, 0)
  const totalPrev = (byModelPrev.data ?? []).reduce((acc, r) => acc + r.total_cost, 0)
  const savings = models.reduce((acc, r) => acc + r.cache_savings, 0)
  const b = budget.data

  // Share of token volume from models with no models.dev price — surfaced so the
  // priced "Spend" figure isn't mistaken for total usage.
  const rowTokens = (r: CostRow) =>
    r.input_tokens + r.output_tokens + r.cache_read_tokens + r.cache_creation_tokens
  const allTokens = models.reduce((acc, r) => acc + rowTokens(r), 0)
  const unpricedTokens = models.filter((r) => !r.priced).reduce((acc, r) => acc + rowTokens(r), 0)
  const unpricedPct = allTokens > 0 ? (unpricedTokens / allTokens) * 100 : 0

  return (
    <div className="grid grid-cols-1 gap-5">
      <StatStrip
        stats={[
          {
            label: 'Spend (range)',
            value: fmtCost(total),
            sub: unpricedPct >= 0.5 ? `⚠ ${unpricedPct.toFixed(0)}% of tokens unpriced` : 'models.dev pricing',
            delta: { pct: pctChange(totalPrev, total), positiveIsGood: false },
          },
          { label: 'Cache Savings', value: fmtCost(savings), sub: 'vs full input rate' },
          {
            label: 'Month to Date',
            value: fmtCost(b?.month_to_date ?? 0),
            sub: b?.budget ? `of ${fmtCost(b.budget)} budget` : 'no budget set (INSIGHTS_BUDGET_MONTHLY)',
          },
          {
            label: 'Projected Month-End',
            value: fmtCost(b?.projected ?? 0),
            sub: b?.budget
              ? (b.projected > b.budget ? 'over budget' : 'within budget')
              : `${Math.round((b?.month_elapsed ?? 0) * 100)}% of month elapsed`,
          },
        ]}
      />

      <Panel
        title="Spend over time"
        sub={`${trendMetric === 'cost' ? 'Cost' : 'Tokens'} per interval, stacked by ${spendBy}${
          trendMetric === 'tokens' ? ' · includes unpriced models' : ''
        }`}
        action={
          <div className="flex items-center gap-2">
            <SegToggle value={trendMetric} options={['cost', 'tokens'] as const} onChange={setTrendMetric} />
            <SegToggle value={spendBy} options={['model', 'app'] as const} onChange={setSpendBy} />
          </div>
        }
      >
        <TimeseriesPanel
          points={trendMetric === 'cost' ? trend.data : tokenTrend.data}
          spanMs={spanMs}
          yFmt={trendMetric === 'cost' ? fmtCost : fmtTokens}
          stacked
          loading={trendMetric === 'cost' ? trend.loading : tokenTrend.loading}
        />
      </Panel>

      <Panel title="Spend share" sub="Distribution of spend across apps and models">
        <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
          <div>
            <p className="mb-2 text-center text-xs font-medium uppercase tracking-wider text-gray-500">By App</p>
            {byApp.loading ? (
              <PanelMessage>Loading…</PanelMessage>
            ) : (
              <SpendDoughnut rows={byApp.data ?? []} labelOf={(r) => r.service_name || 'unattributed'} />
            )}
          </div>
          <div>
            <p className="mb-2 text-center text-xs font-medium uppercase tracking-wider text-gray-500">By Model</p>
            {byModel.loading ? (
              <PanelMessage>Loading…</PanelMessage>
            ) : (
              <SpendDoughnut rows={models} labelOf={(r) => r.request_model || '—'} />
            )}
          </div>
        </div>
      </Panel>

      <Panel
        title="Cost per User"
        sub="Priced token usage attributed via user.id"
        action={
          <a
            href={apiUrl('/api/genai/cost-report', byUser.params)}
            download
            className="flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-indigo-500"
          >
            <Download className="h-3.5 w-3.5" />
            Report (CSV)
          </a>
        }
      >
        {byUser.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (byUser.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={byUser.data!} columns={userColumns} initialSort={[{ id: 'total_cost', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
