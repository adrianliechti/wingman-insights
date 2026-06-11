import { Download } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi } from '../dash'
import { apiUrl } from '../api'
import type { CostRow } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { fmtCost, fmtTokens } from '../lib/format'

function tokenCols<T extends CostRow>(): ColumnDef<T, any>[] {
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
    { accessorKey: 'input_cost', header: 'Input $', meta: { align: 'right' }, cell: (c) => fmtCost(c.getValue()) },
    { accessorKey: 'output_cost', header: 'Output $', meta: { align: 'right' }, cell: (c) => fmtCost(c.getValue()) },
    {
      id: 'cache_cost',
      header: 'Cache $',
      meta: { align: 'right' },
      accessorFn: (r: T) => r.cache_read_cost + r.cache_creation_cost,
      cell: (c) => fmtCost(c.getValue()),
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
              <span className="ml-1.5 rounded bg-amber-500/10 px-1 py-0.5 text-[10px] font-medium text-amber-600 dark:text-amber-400" title="No pricing data for this model in models.dev">
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
  ...tokenCols(),
]

const modelColumns: ColumnDef<CostRow, any>[] = [
  { accessorKey: 'provider_name', header: 'Provider' },
  {
    accessorKey: 'request_model',
    header: 'Model',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue()}</span>,
  },
  ...tokenCols(),
]

export function Costs() {
  const byUser = useApi<CostRow[]>('/api/genai/costs', { group_by: 'user' })
  const byModel = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })

  const models = byModel.data ?? []
  const total = models.reduce((acc, r) => acc + r.total_cost, 0)
  const inputCost = models.reduce((acc, r) => acc + r.input_cost, 0)
  const outputCost = models.reduce((acc, r) => acc + r.output_cost, 0)
  const cacheCost = models.reduce((acc, r) => acc + r.cache_read_cost + r.cache_creation_cost, 0)

  const reportUrl = apiUrl('/api/genai/cost-report', byUser.params)

  return (
    <div className="grid grid-cols-1 gap-5">
      <StatStrip
        stats={[
          { label: 'Total Spend', value: fmtCost(total), sub: 'models.dev pricing' },
          { label: 'Input', value: fmtCost(inputCost) },
          { label: 'Output', value: fmtCost(outputCost) },
          { label: 'Cache', value: fmtCost(cacheCost) },
        ]}
      />

      <Panel
        title="Cost per User"
        sub="Priced token usage attributed via user.id"
        action={
          <a
            href={reportUrl}
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
