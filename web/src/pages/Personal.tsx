import { useState } from 'react'
import { useApi, useDash } from '../dash'
import type { UsageByAppRow, UsageResponse, ContextBucketRow } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { Bar, Line, ChartLegend, Doughnut, PALETTE, chartOptions, useChartBlue, CHART } from '../components/charts'
import { fmtTokens } from '../lib/format'
import { format } from 'date-fns'
import { BarChart3, LineChart } from 'lucide-react'

// ChartType toggles the estimated-cost chart between grouped bars and a trend line.
type ChartType = 'bar' | 'line'

// fmtUsd formats a dollar amount with a single decimal place, so every cost on
// the personal dashboard reads consistently (e.g. $305.8, $4.5, $0.3) rather
// than the shared fmtCost's variable precision.
function fmtUsd(n: number): string {
  return '$' + n.toFixed(1)
}

// tokenVolume is the billed token total for one bucket/row: input + output +
// cached (reasoning is a subset of output, so it isn't added again). It matches
// the ledger described on store.TokenTotals.
function tokenVolume(t: { input: number; output: number; cached: number }): number {
  return t.input + t.output + t.cached
}

// ShareBars renders a labelled horizontal breakdown (usage or cost share) as a
// ranked list with a proportion bar — the same visual language as the rest of
// the dashboard, without needing a full chart. Only the top `limit` rows show
// initially, with a toggle to reveal the rest; shares stay relative to the full
// total so the collapsed view's percentages still add up correctly.
// MODEL_PALETTE is the per-model bar palette. It drops CHART.blue so the model
// bars never reuse the "Cost over Time" accent blue, keeping the two readings
// visually distinct.
const MODEL_PALETTE = PALETTE.filter((c) => c !== CHART.blue)

function ShareBars({
  rows,
  fmt,
  colorFor,
  limit = 5,
}: {
  rows: { label: string; value: number }[]
  fmt: (v: number) => string
  colorFor: (label: string) => string
  limit?: number
}) {
  const [expanded, setExpanded] = useState(false)
  const total = rows.reduce((acc, r) => acc + r.value, 0)
  const sorted = [...rows].filter((r) => r.value > 0).sort((a, b) => b.value - a.value)
  if (sorted.length === 0) return <PanelMessage>No data</PanelMessage>
  const visible = expanded ? sorted : sorted.slice(0, limit)
  const hidden = sorted.length - visible.length
  return (
    <div className="flex flex-col gap-3">
      {visible.map((r) => {
        const pct = total > 0 ? (100 * r.value) / total : 0
        const color = colorFor(r.label)
        return (
          <div key={r.label}>
            <div className="mb-1 flex items-baseline justify-between gap-2 text-xs">
              <span className="truncate text-gray-700 dark:text-gray-300">{r.label}</span>
              <span className="shrink-0 tabular-nums text-gray-500">
                {fmt(r.value)} · {pct.toFixed(1)}%
              </span>
            </div>
            <div className="h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-gray-800">
              <div className="h-full rounded-full" style={{ width: `${pct}%`, backgroundColor: color }} />
            </div>
          </div>
        )
      })}
      {sorted.length > limit && (
        <button
          onClick={() => setExpanded((v) => !v)}
          className="cursor-pointer self-start text-xs font-medium text-indigo-600 hover:text-indigo-700 dark:text-indigo-400 dark:hover:text-indigo-300"
        >
          {expanded ? 'Show less' : `Show ${hidden} more`}
        </button>
      )}
    </div>
  )
}

// CostChart plots estimated cost over time as bars or a line. Granularity is one
// point per hour for a day-or-less window, else one per calendar day (see
// Personal). Hourly points are labelled by clock time (HH:mm); daily points show
// the weekday and date (e.g. "Fri, Sep 5") so the day is unambiguous without a
// stray 00:00.
function CostChart({
  buckets,
  hourly,
  type,
}: {
  buckets: { bucket: string; value: number }[]
  hourly: boolean
  type: ChartType
}) {
  const blue = useChartBlue()
  if (buckets.length === 0) return <PanelMessage>No data</PanelMessage>
  const labelFor = (bucket: string) =>
    hourly ? format(new Date(bucket), 'HH:mm') : format(new Date(bucket), 'EEE, MMM d')
  const labels = buckets.map((b) => labelFor(b.bucket))
  const values = buckets.map((b) => b.value)
  const options = chartOptions({ yFmt: fmtUsd })
  return (
    <div className="h-64">
      {type === 'line' ? (
        <Line
          data={{
            labels,
            datasets: [
              {
                label: 'Cost',
                data: values,
                borderColor: blue,
                backgroundColor: blue,
                fill: false,
                tension: 0.3,
                pointRadius: 2,
              },
            ],
          }}
          options={options}
        />
      ) : (
        <Bar
          data={{
            labels,
            datasets: [
              {
                label: 'Cost',
                data: values,
                backgroundColor: blue,
                borderRadius: 4,
                maxBarThickness: 48,
              },
            ],
          }}
          options={options}
        />
      )}
    </div>
  )
}

// AppShareChart shows the caller's request cost split by application as a
// doughnut with a matching legend.
function AppShareChart() {
  const { data, loading } = useApi<UsageByAppRow[]>('/personal/usage-by-app')
  const blue = useChartBlue()
  if (loading) return <PanelMessage>Loading…</PanelMessage>
  const rows = (data ?? []).filter((r) => r.cost > 0 || tokenVolume(r.tokens) > 0)
  if (rows.length === 0) return <PanelMessage>No data</PanelMessage>
  const label = (r: UsageByAppRow) => r.app || 'unattributed'
  // Theme-aware palette: the leading blue tracks the topbar/chart accent
  // (#283c83 light, #4257a8 dark) instead of the static brand navy.
  const palette = [blue, ...PALETTE.slice(1)]
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4">
      <div className="h-48 w-48">
        <Doughnut
          data={{
            labels: rows.map(label),
            datasets: [{ data: rows.map((r) => tokenVolume(r.tokens)), backgroundColor: palette, borderWidth: 0 }],
          }}
          options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } } }}
        />
      </div>
      <ChartLegend align="center" items={rows.map((r, i) => ({ label: label(r), color: palette[i % palette.length] }))} />
    </div>
  )
}

// ContextChart shows how many of the caller's own LLM calls fall in each
// prompt-size bin. Bins arrive zero-filled and ordered from the API; the
// 200k/272k edges mark where long-context pricing kicks in, so the tail is the
// caller's premium-billed share.
function ContextChart({ rows, loading }: { rows: ContextBucketRow[] | null; loading: boolean }) {
  if (loading) return <PanelMessage>Loading…</PanelMessage>
  const bins = rows ?? []
  const total = bins.reduce((a, r) => a + r.requests, 0)
  if (total === 0) return <PanelMessage>No data</PanelMessage>
  const opts = chartOptions({ yFmt: fmtTokens })
  return (
    <div className="h-64">
      <Bar
        data={{
          labels: bins.map((r) => r.bucket),
          datasets: [
            {
              data: bins.map((r) => r.requests),
              backgroundColor: CHART.redwine,
              borderRadius: 4,
              maxBarThickness: 48,
            },
          ],
        }}
        options={{
          ...opts,
          plugins: {
            ...opts.plugins,
            tooltip: {
              ...opts.plugins.tooltip,
              callbacks: {
                label: (ctx: any) => {
                  const r = bins[ctx.dataIndex]
                  const share = ((r.requests / total) * 100).toFixed(1)
                  return ` ${r.requests.toLocaleString()} calls (${share}%) · ${fmtUsd(r.cost)}`
                },
              },
            },
          },
        }}
      />
    </div>
  )
}

// floorToHour / floorToLocalDay round an ISO instant down to the start of its
// clock hour / local calendar day and return an ISO string. Used to align the
// cost chart's bucket origin so bars fall on full hours and local midnights.
function floorToHour(iso: string): string {
  const d = new Date(iso)
  d.setMinutes(0, 0, 0)
  return d.toISOString()
}

function floorToLocalDay(iso: string): string {
  const d = new Date(iso)
  d.setHours(0, 0, 0, 0)
  return d.toISOString()
}

export function Personal() {
  const { from, to, spanMs } = useDash()
  const [costChartType, setCostChartType] = useState<ChartType>('bar')

  // The cost chart buckets on clean clock boundaries: one bar per hour for a
  // ~day-or-less window (aligned to full hours, e.g. 08:00–09:00), and one bar
  // per calendar day for longer windows. The server buckets from `from` as the
  // origin, so flooring `from` to the hour / local midnight makes the bars land
  // on those boundaries instead of the raw window start (e.g. 14:27).
  const hourly = spanMs <= 26 * 3600e3
  const alignedFrom = hourly ? floorToHour(from) : floorToLocalDay(from)
  const usage = useApi<UsageResponse>('/personal/usage', {
    from: alignedFrom,
    to,
    interval: hourly ? '1 hour' : '1 day',
  })
  const contextHist = useApi<ContextBucketRow[]>('/personal/context-histogram')

  const data = usage.data
  const buckets = data?.buckets ?? []
  const t = data?.tokens

  // Cost over time: buckets are per-model per interval, so fold every model's
  // cost into one total per interval (keyed by the bucket boundary).
  const costTotals = new Map<string, number>()
  for (const b of buckets) {
    costTotals.set(b.bucket, (costTotals.get(b.bucket) ?? 0) + b.cost)
  }
  const costSeries = [...costTotals.entries()]
    .sort(([a], [z]) => a.localeCompare(z))
    .map(([bucket, value]) => ({ bucket, value }))

  // Per-model usage (token volume) and cost share, folded from the same buckets.
  const byModel = new Map<string, { tokens: number; cost: number }>()
  for (const b of buckets) {
    const key = b.model || 'unknown'
    const cur = byModel.get(key) ?? { tokens: 0, cost: 0 }
    cur.tokens += tokenVolume(b.tokens)
    cur.cost += b.cost
    byModel.set(key, cur)
  }
  const modelTokens = [...byModel.entries()].map(([label, v]) => ({ label, value: v.tokens }))
  const modelCost = [...byModel.entries()].map(([label, v]) => ({ label, value: v.cost }))

  // Assign each model a stable color by its overall token volume, so a given
  // model shows the same color in both the "Usage by Model" and "Cost by Model"
  // charts regardless of how it ranks within each.
  const modelColor = new Map<string, string>()
  ;[...byModel.entries()]
    .sort(([, a], [, b]) => b.tokens - a.tokens)
    .forEach(([label], i) => modelColor.set(label, MODEL_PALETTE[i % MODEL_PALETTE.length]))
  const colorForModel = (label: string) => modelColor.get(label) ?? MODEL_PALETTE[0]

  const total = t ? tokenVolume(t) : 0

  const tokenStats = [
    { label: 'Total Tokens', value: fmtTokens(total), sub: undefined as string | undefined },
    { label: 'Input Tokens', value: fmtTokens(t?.input ?? 0), sub: t && t.cached > 0 ? `${fmtTokens(t.cached)} cached` : undefined },
    { label: 'Output Tokens', value: fmtTokens(t?.output ?? 0), sub: t && t.reasoning > 0 ? `${fmtTokens(t.reasoning)} reasoning` : undefined },
  ]

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <section className="rounded-lg border border-gray-200 p-5 lg:col-span-2 dark:border-gray-800">
        <div className="mb-5 flex items-start justify-between gap-4">
          <div>
            <p className="text-5xl font-bold tracking-tight tabular-nums text-gray-900 dark:text-white">
              {fmtUsd(data?.cost ?? 0)}
            </p>
            <p className="mt-1 text-sm text-gray-500">Estimated cost over time</p>
          </div>
          <div className="flex rounded-lg border border-gray-200 p-0.5 dark:border-gray-800">
            {([
              { type: 'bar' as const, Icon: BarChart3, label: 'Bar chart' },
              { type: 'line' as const, Icon: LineChart, label: 'Line chart' },
            ]).map(({ type, Icon, label }) => (
              <button
                key={type}
                onClick={() => setCostChartType(type)}
                aria-label={label}
                aria-pressed={costChartType === type}
                className={`cursor-pointer rounded-md px-3 py-1.5 transition-colors ${
                  costChartType === type
                    ? 'bg-indigo-600 text-white'
                    : 'text-gray-500 hover:text-gray-900 dark:hover:text-white'
                }`}
              >
                <Icon className="h-4 w-4" strokeWidth={2} />
              </button>
            ))}
          </div>
        </div>
        {usage.loading ? <PanelMessage>Loading…</PanelMessage> : <CostChart buckets={costSeries} hourly={hourly} type={costChartType} />}
      </section>

      <div className="lg:col-span-2">
        <StatStrip stats={tokenStats} />
      </div>

      <Panel title="Usage by Model" sub="Share of token volume">
        {usage.loading ? <PanelMessage>Loading…</PanelMessage> : <ShareBars rows={modelTokens} fmt={fmtTokens} colorFor={colorForModel} />}
      </Panel>

      <Panel title="Cost by Model" sub="Share of estimated spend">
        {usage.loading ? <PanelMessage>Loading…</PanelMessage> : <ShareBars rows={modelCost} fmt={fmtUsd} colorFor={colorForModel} />}
      </Panel>

      <Panel title="By Application" sub="Share of token volume">
        <AppShareChart />
      </Panel>

      <Panel
        title="Context Size"
        sub="Calls by prompt size · >200k/272k bill at long-context rates"
      >
        <ContextChart rows={contextHist.data} loading={contextHist.loading} />
      </Panel>
    </div>
  )
}
