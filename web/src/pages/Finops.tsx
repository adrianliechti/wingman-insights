import { Download } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import { apiUrl } from '../api'
import type { BudgetResponse, CostRow, TimeseriesPoint } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { TimeseriesPanel } from '../components/charts'
import { fmtCost, fmtTokens } from '../lib/format'

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
    { accessorKey: 'input_cost', header: 'Input $', meta: { align: 'right' }, cell: (c) => fmtCost(c.getValue()) },
    { accessorKey: 'output_cost', header: 'Output $', meta: { align: 'right' }, cell: (c) => fmtCost(c.getValue()) },
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
                title="No pricing data for this model in models.dev"
              >
                partial
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

const modelColumns: ColumnDef<CostRow, any>[] = [
  { accessorKey: 'provider_name', header: 'Provider' },
  {
    accessorKey: 'request_model',
    header: 'Model',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue()}</span>,
  },
  ...costCols(),
]

export function Finops() {
  const { spanMs } = useDash()
  const byUser = useApi<CostRow[]>('/api/genai/costs', { group_by: 'user' })
  const byModel = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })
  const budget = useApi<BudgetResponse>('/api/finops/budget')
  const trend = useApi<TimeseriesPoint[]>('/api/finops/cost-timeseries')

  const models = byModel.data ?? []
  const total = models.reduce((acc, r) => acc + r.total_cost, 0)
  const savings = models.reduce((acc, r) => acc + r.cache_savings, 0)
  const b = budget.data

  return (
    <div className="grid grid-cols-1 gap-5">
      <StatStrip
        stats={[
          { label: 'Spend (range)', value: fmtCost(total), sub: 'models.dev pricing' },
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

      <Panel title="Spend" sub="Cost per interval, stacked by model">
        <TimeseriesPanel points={trend.data} spanMs={spanMs} yFmt={fmtCost} stacked loading={trend.loading} />
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

      <Panel title="Cost per Model">
        {byModel.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : models.length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={models} columns={modelColumns} initialSort={[{ id: 'total_cost', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
