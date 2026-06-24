import { useMemo, useState } from 'react'
import { Download } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash, usePrevRange } from '../dash'
import { apiUrl } from '../api'
import type { BudgetResponse, CostRow, TimeseriesPoint } from '../types'
import { KindBadge, UserCell } from '../components/UserCell'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { ChartLegend, Doughnut, PALETTE, Pie, TimeseriesPanel } from '../components/charts'
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
    accessorKey: 'id',
    header: 'User',
    cell: (c) => <UserCell r={c.row.original} />,
  },
  { id: 'kind', accessorKey: 'kind', header: 'Kind', cell: (c) => <KindBadge kind={c.getValue()} /> },
  ...costCols(),
]

const appColumns: ColumnDef<CostRow, any>[] = [
  {
    accessorKey: 'service_name',
    header: 'Application',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue() || 'unattributed'}</span>,
  },
  ...costCols(),
]

const departmentColumns: ColumnDef<CostRow, any>[] = [
  {
    accessorKey: 'department',
    header: 'Department',
    cell: (c) => <span className="text-gray-900 dark:text-gray-100">{c.getValue() || 'Unknown'}</span>,
  },
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

// Slate gray for the consolidated "Others" bucket — kept off the PALETTE so the
// long tail reads as residual, not as another named consumer.
const OTHER_COLOR = '#94a3b8'

// Human label for an allocation consumer — the user.id it's attributed to.
// Module-scope so its identity is stable across renders (keeps AllocationKey's
// useMemo from busting).
const userLabel = (r: CostRow) => r.name || r.id || 'unattributed'

interface AllocSlice {
  label: string
  cost: number
  pct: number // whole-percent share, rounded so the column sums to exactly 100
  isOther: boolean
  count: number // consumers folded into this slice (1, except for Others)
}

// largestRemainder rounds fractional shares (summing to ~1) to whole percentages
// that sum to exactly 100 — the Hamilton method. Without it, independently
// rounded rows drift to 99 or 101 and the suggested key looks broken.
function largestRemainder(shares: number[]): number[] {
  const raw = shares.map((s) => s * 100)
  const pct = raw.map(Math.floor)
  let left = 100 - pct.reduce((a, b) => a + b, 0)
  const byFrac = raw.map((v, i) => ({ i, frac: v - Math.floor(v) })).sort((a, b) => b.frac - a.frac)
  for (let k = 0; k < byFrac.length && left > 0; k++, left--) pct[byFrac[k].i]++
  return pct
}

// buildAllocation turns priced cost rows into a suggested distribution key. To
// stay readable at any scale (5000+ users), it names at most `maxSlices`
// consumers and folds the entire long tail into a single "Others" bucket — so
// thousands of small users collapse into one slice, not thousands. The biggest
// `minSlices` are named even below `minShare` (so a gentle, no-clear-winner
// distribution still shows who leads), but nothing under `floorShare` is ever
// named — that would only add meaningless ~0% slices.
function buildAllocation(
  rows: CostRow[],
  labelOf: (r: CostRow) => string,
  {
    minShare = 0.02,
    floorShare = 0.005,
    minSlices = 3,
    maxSlices = 6,
  }: { minShare?: number; floorShare?: number; minSlices?: number; maxSlices?: number } = {},
): { slices: AllocSlice[]; total: number } {
  const priced = rows.filter((r) => r.total_cost > 0)
  const total = priced.reduce((a, r) => a + r.total_cost, 0)
  if (total <= 0) return { slices: [], total: 0 }

  const sorted = [...priced].sort((a, b) => b.total_cost - a.total_cost)
  const top: CostRow[] = []
  const rest: CostRow[] = []
  for (const r of sorted) {
    // The biggest `minSlices` only need to clear `floorShare`; past that a
    // consumer must clear the stiffer `minShare`. Either way never exceed
    // `maxSlices`, so the long tail always collapses into a single "Others".
    const threshold = top.length < minSlices ? floorShare : minShare
    if (top.length < maxSlices && r.total_cost / total >= threshold) top.push(r)
    else rest.push(r)
  }
  // A lone leftover isn't worth hiding behind "Others (1)" — show it by name.
  if (rest.length === 1) top.push(rest.pop()!)

  const slices: AllocSlice[] = top.map((r) => ({ label: labelOf(r), cost: r.total_cost, pct: 0, isOther: false, count: 1 }))
  if (rest.length > 0) {
    slices.push({ label: 'Others', cost: rest.reduce((a, r) => a + r.total_cost, 0), pct: 0, isOther: true, count: rest.length })
  }
  const pct = largestRemainder(slices.map((s) => s.cost / total))
  slices.forEach((s, i) => (s.pct = pct[i]))
  return { slices, total }
}

// AllocationKey renders a suggested cost-sharing split: a pie of the biggest
// consumers plus a table of their whole-percent shares. Both are driven by the
// rounded key, so the chart and the table tell the identical story.
function AllocationKey({ rows, labelOf }: { rows: CostRow[]; labelOf: (r: CostRow) => string }) {
  const { slices, total } = useMemo(() => buildAllocation(rows, labelOf), [rows, labelOf])
  if (slices.length === 0) return <PanelMessage>No priced spend in range</PanelMessage>
  const colorOf = (s: AllocSlice, i: number) => (s.isOther ? OTHER_COLOR : PALETTE[i % PALETTE.length])

  return (
    <div>
      <div className="grid grid-cols-1 items-center gap-6 lg:grid-cols-[200px_1fr]">
        <div className="mx-auto h-48 w-48">
          <Pie
            data={{
              labels: slices.map((s) => s.label),
              datasets: [{ data: slices.map((s) => s.pct), backgroundColor: slices.map(colorOf), borderWidth: 0 }],
            }}
            options={{
              responsive: true,
              maintainAspectRatio: false,
              plugins: {
                legend: { display: false },
                tooltip: {
                  callbacks: { label: (ctx: any) => ` ${ctx.label}: ${ctx.parsed}% · ${fmtCost(slices[ctx.dataIndex].cost)}` },
                },
              },
            }}
          />
        </div>

        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-200 dark:border-gray-800">
                <th className="px-3 py-2 text-left text-xs font-medium uppercase tracking-wider text-gray-500">Consumer</th>
                <th className="px-3 py-2 text-right text-xs font-medium uppercase tracking-wider text-gray-500">Cost</th>
                <th className="px-3 py-2 text-right text-xs font-medium uppercase tracking-wider text-gray-500">Share</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800/50">
              {slices.map((s, i) => {
                const color = colorOf(s, i)
                return (
                  <tr key={`${i}-${s.label}`}>
                    <td className="px-3 py-2.5">
                      <span className="inline-flex items-center gap-2">
                        <span className="h-2.5 w-2.5 shrink-0 rounded-sm" style={{ backgroundColor: color }} />
                        <span className={s.isOther ? 'text-gray-500 dark:text-gray-400' : 'text-gray-700 dark:text-gray-300'}>
                          {s.label}
                          {s.isOther && <span className="ml-1 text-xs text-gray-400">· {s.count} consumers</span>}
                        </span>
                      </span>
                    </td>
                    <td className="px-3 py-2.5 text-right tabular-nums text-gray-700 dark:text-gray-300">{fmtCost(s.cost)}</td>
                    <td className="px-3 py-2.5">
                      <div className="flex items-center justify-end gap-2">
                        <div className="h-1.5 w-20 overflow-hidden rounded-full bg-gray-100 dark:bg-gray-800">
                          <div className="h-full rounded-full" style={{ width: `${s.pct}%`, backgroundColor: color }} />
                        </div>
                        <span className="w-9 text-right font-medium tabular-nums text-gray-900 dark:text-white">{s.pct}%</span>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
            <tfoot>
              <tr className="border-t border-gray-200 dark:border-gray-700">
                <td className="px-3 py-2.5 text-xs font-medium uppercase tracking-wider text-gray-500">Total</td>
                <td className="px-3 py-2.5 text-right font-medium tabular-nums text-gray-900 dark:text-white">{fmtCost(total)}</td>
                <td className="px-3 py-2.5 text-right font-medium tabular-nums text-gray-900 dark:text-white">100%</td>
              </tr>
            </tfoot>
          </table>
        </div>
      </div>
      <p className="mt-4 text-[11px] text-gray-400 dark:text-gray-500">
        Suggested split from priced spend in the selected range · the biggest consumers are shown individually, the long tail grouped as Others.
      </p>
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
  const byDept = useApi<CostRow[]>('/api/genai/costs', { group_by: 'department' })
  const byModel = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model' })
  const byModelPrev = useApi<CostRow[]>('/api/genai/costs', { group_by: 'model', ...prev })
  const budget = useApi<BudgetResponse>('/api/finops/budget')
  const trend = useApi<TimeseriesPoint[]>('/api/finops/cost-timeseries', { by: spendBy })
  const tokenTrend = useApi<TimeseriesPoint[]>('/api/finops/token-timeseries', { by: spendBy })

  const models = byModel.data ?? []
  // Only surface the department view when the directory actually attributes
  // departments (otherwise every row folds into the "Unknown" bucket).
  const hasDepartments = (byDept.data ?? []).some((r) => !!r.department)
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
        title="Cost Allocation"
        sub="Suggested cost-share split across the biggest consumers (by user.id) · small consumers rolled up as Others"
      >
        {byUser.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (
          <AllocationKey rows={byUser.data ?? []} labelOf={userLabel} />
        )}
      </Panel>

      <Panel title="Cost per Application" sub="Priced token usage attributed via service.name">
        {byApp.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (byApp.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={byApp.data!} columns={appColumns} initialSort={[{ id: 'total_cost', desc: true }]} />
        )}
      </Panel>

      {hasDepartments && (
        <Panel title="Cost per Department" sub="Priced token usage grouped by the directory department">
          {byDept.loading ? (
            <PanelMessage>Loading…</PanelMessage>
          ) : (
            <DataTable data={byDept.data!} columns={departmentColumns} initialSort={[{ id: 'total_cost', desc: true }]} />
          )}
        </Panel>
      )}

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
