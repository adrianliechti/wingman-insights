import { useState } from 'react'
import { useApi, useDash } from '../dash'
import type { UsageByAppRow, UsageResponse, ContextBucketRow } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { Bar, Line, ChartLegend, Doughnut, PALETTE, chartOptions, useChartBlue, CHART } from '../components/charts'
import { fmtTokens, fmtModelName } from '../lib/format'
import { priceTier, tierRate, type ModelRate } from '../lib/costs'
import { format } from 'date-fns'
import { BarChart3, LineChart, Lightbulb, Coins, MessageSquareDashed } from 'lucide-react'

// ChartType toggles the estimated-cost chart between grouped bars and a trend line.
type ChartType = 'bar' | 'line'

// fmtUsd formats a dollar amount to cents, so every personal-dashboard cost
// uses the same two-decimal precision.
function fmtUsd(n: number): string {
  return '$' + n.toFixed(2)
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

// fmtRate renders a $/1M rate with cent precision below $100, whole dollars above.
function fmtRate(rate: number): string {
  return '$' + (rate >= 100 ? rate.toFixed(0) : rate.toFixed(2))
}

// PriceBadge renders the tier as filled/dimmed dollar signs with a hover
// popover breaking the blended rate into its input and output components.
function PriceBadge({ rate }: { rate?: ModelRate }) {
  const tier = rate ? priceTier(tierRate(rate)) : null
  if (!rate || !tier) return null
  return (
    <span className="group relative inline-flex shrink-0 cursor-default items-center font-medium tabular-nums">
      {[1, 2, 3].map((i) => (
        <span
          key={i}
          className={i <= tier.dollars ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-300 dark:text-gray-700'}
        >
          $
        </span>
      ))}
      <span className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden w-max -translate-x-1/2 rounded-md bg-gray-900 px-2.5 py-2 text-left text-[11px] font-normal text-white shadow-lg group-hover:block dark:bg-gray-700">
        <span className="flex items-center justify-between gap-4">
          <span className="text-gray-300 dark:text-gray-400">Input</span>
          <span>{rate.input > 0 ? `${fmtRate(rate.input)}/1M` : '—'}</span>
        </span>
        <span className="flex items-center justify-between gap-4">
          <span className="text-gray-300 dark:text-gray-400">Output</span>
          <span>{rate.output > 0 ? `${fmtRate(rate.output)}/1M` : '—'}</span>
        </span>
      </span>
    </span>
  )
}

// Hint is one actionable suggestion for the caller, derived from their own
// usage. `id` keys the list; `Icon` and `tone` colour the card by theme.
interface Hint {
  id: string
  Icon: typeof Lightbulb
  title: string
  body: string
}

// Minimum activity floors so trivial usage doesn't trigger a hint: a share
// threshold on its own fires at 100% off a single small call. MIN_PREMIUM_SPEND
// is the least premium cost (USD) worth flagging; MIN_LONG_CONTEXT_CALLS the
// least number of large-prompt calls that counts as a pattern.
const MIN_PREMIUM_SPEND = 20
const MIN_LONG_CONTEXT_CALLS = 20

// deriveHints turns the caller's model mix and prompt-size distribution into
// coaching tips. It only surfaces a hint when its threshold is clearly crossed,
// so a light user isn't nagged; each hint states the observed share so the
// suggestion is grounded in their actual numbers.
function deriveHints(
  modelCost: { label: string; value: number }[],
  rateFor: (label: string) => ModelRate | undefined,
  contextRows: ContextBucketRow[] | null,
): Hint[] {
  const hints: Hint[] = []

  // Premium-model spend: what share of cost went to models whose blended rate
  // lands in the top tier. A majority of spend on premium frontier models is
  // the signal that a cheaper model might cover some of the work.
  const totalCost = modelCost.reduce((a, r) => a + r.value, 0)
  if (totalCost > 0) {
    const premiumCost = modelCost.reduce((a, r) => {
      const rate = rateFor(r.label)
      const tier = rate ? priceTier(tierRate(rate)) : null
      return a + (tier?.dollars === 3 ? r.value : 0)
    }, 0)
    const premiumShare = premiumCost / totalCost
    // Require a floor of actual premium spend so a single small premium call
    // (100% share of a few cents) doesn't trip the hint — only real, sustained
    // premium usage is worth suggesting a switch for.
    if (premiumShare >= 0.6 && premiumCost >= MIN_PREMIUM_SPEND) {
      hints.push({
        id: 'premium-models',
        Icon: Coins,
        title: 'Try a cheaper model for routine work',
        body: `${(premiumShare * 100).toFixed(0)}% of your spend is on premium models. For everyday drafting, edits and simple questions, a budget or standard model often gives comparable results at a fraction of the cost.`,
      })
    }
  }

  // Long contexts: share of calls that landed in the long-context bins, which
  // bill at premium rates. A meaningful tail suggests chats are growing large
  // and a fresh conversation would trim the prompt back down.
  const rows = contextRows ?? []
  const totalCalls = rows.reduce((a, r) => a + r.requests, 0)
  if (totalCalls > 0) {
    const longCalls = rows
      .filter((r) => r.bucket === '200k–272k' || r.bucket === '> 272k')
      .reduce((a, r) => a + r.requests, 0)
    const longShare = longCalls / totalCalls
    // A single large-context call shouldn't nag; require a minimum count of
    // them alongside the share so the pattern is a habit, not a one-off.
    if (longShare >= 0.15 && longCalls >= MIN_LONG_CONTEXT_CALLS) {
      hints.push({
        id: 'long-context',
        Icon: MessageSquareDashed,
        title: 'Start a new chat to shrink long prompts',
        body: `${(longShare * 100).toFixed(0)}% of your calls carry very large prompts that bill at long-context rates. Starting a fresh conversation once a chat gets long keeps prompts — and cost — smaller.`,
      })
    }
  }

  return hints
}

// HintCards renders the derived suggestions as a compact list.
function HintCards({ hints }: { hints: Hint[] }) {
  return (
    <div className="flex flex-col gap-3">
      {hints.map(({ id, Icon, title, body }) => (
        <div
          key={id}
          className="flex gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3.5 dark:border-amber-900/50 dark:bg-amber-950/30"
        >
          <Icon className="mt-0.5 h-5 w-5 shrink-0 text-amber-600 dark:text-amber-400" strokeWidth={2} />
          <div className="min-w-0">
            <p className="text-sm font-semibold text-gray-900 dark:text-gray-100">{title}</p>
            <p className="mt-0.5 text-xs leading-relaxed text-gray-600 dark:text-gray-400">{body}</p>
          </div>
        </div>
      ))}
    </div>
  )
}

function ShareBars({
  rows,
  fmt,
  colorFor,
  rateFor,
  labelFor,
  limit = 5,
  expanded: expandedProp,
  onToggle,
}: {
  rows: { label: string; value: number }[]
  fmt: (v: number) => string
  colorFor: (label: string) => string
  rateFor?: (label: string) => ModelRate | undefined
  labelFor?: (label: string) => string
  limit?: number
  expanded?: boolean
  onToggle?: () => void
}) {
  // Expansion is controlled when the caller passes `expanded`/`onToggle` (so
  // sibling breakdowns expand together), else self-managed.
  const [expandedState, setExpandedState] = useState(false)
  const expanded = expandedProp ?? expandedState
  const toggle = onToggle ?? (() => setExpandedState((v) => !v))
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
              <span className="flex min-w-0 items-baseline gap-1.5">
                <span className="truncate text-gray-700 dark:text-gray-300">{labelFor ? labelFor(r.label) : r.label}</span>
                {rateFor && <PriceBadge rate={rateFor(r.label)} />}
              </span>
              <span className="shrink-0 tabular-nums text-gray-500">
                {fmt(r.value)} · {pct.toFixed(2)}%
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
          onClick={toggle}
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
  const { data, loading } = useApi<UsageByAppRow[]>('/api/personal/usage-by-app')
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
                  const share = ((r.requests / total) * 100).toFixed(2)
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

// latestBucketStart returns the first of exactly `count` clock-hour or local
// calendar-day buckets ending in the current bucket. Thus a 7-day selection
// always shows seven labelled daily bars (including today), rather than eight
// when a rolling 7×24h range touches parts of both its start and end dates.
function latestBucketStart(to: string, count: number, hourly: boolean): string {
  // `to` is the exclusive end of the selected range. Stepping back a
  // millisecond selects the bucket that actually contains the range's end;
  // it also handles custom ranges that finish exactly at midnight cleanly.
  const d = new Date(new Date(to).getTime() - 1)
  if (hourly) {
    d.setMinutes(0, 0, 0)
    d.setTime(d.getTime() - (count - 1) * 3600e3)
  } else {
    d.setHours(0, 0, 0, 0)
    d.setDate(d.getDate() - (count - 1))
  }
  return d.toISOString()
}

// fillCostSeries keeps every interval in the selected chart range, including
// ones for which the API has no matching spans. Without this, Chart.js receives
// only non-empty buckets, which makes a quiet hour or day disappear rather than
// rendering as a zero-height bar with its time label.
function fillCostSeries(
  totals: Map<string, number>,
  from: string,
  to: string,
  hourly: boolean,
): { bucket: string; value: number }[] {
  // Daily buckets are keyed by their local calendar date. The server's
  // time_bucket origin is an instant, while local midnights move at DST; a
  // date key keeps a cost on the right labelled day across that boundary.
  const keyFor = (bucket: string) => (hourly ? String(new Date(bucket).getTime()) : format(new Date(bucket), 'yyyy-MM-dd'))
  const valuesByBucket = new Map<string, number>()
  for (const [bucket, value] of totals) {
    const key = keyFor(bucket)
    valuesByBucket.set(key, (valuesByBucket.get(key) ?? 0) + value)
  }

  const end = new Date(to).getTime()
  const cursor = new Date(from)
  const series: { bucket: string; value: number }[] = []
  while (cursor.getTime() < end) {
    series.push({
      bucket: cursor.toISOString(),
      value: valuesByBucket.get(keyFor(cursor.toISOString())) ?? 0,
    })
    if (hourly) {
      cursor.setTime(cursor.getTime() + 3600e3)
    } else {
      // Advance calendar days in local time so labels remain one day apart
      // when the selected range crosses a daylight-saving transition.
      cursor.setDate(cursor.getDate() + 1)
    }
  }
  return series
}

export function Personal() {
  const { to, spanMs } = useDash()
  const [costChartType, setCostChartType] = useState<ChartType>('bar')
  const [modelExpanded, setModelExpanded] = useState(false)

  // The cost chart uses one full set of clock-hour or calendar-day buckets,
  // including the current partial bucket. Selecting 24h/7d therefore produces
  // exactly 24 hourly/7 daily labels, respectively.
  const hourly = spanMs <= 26 * 3600e3
  const bucketCount = Math.max(1, Math.ceil(spanMs / (hourly ? 3600e3 : 24 * 3600e3)))
  const alignedFrom = latestBucketStart(to, bucketCount, hourly)
  const usage = useApi<UsageResponse>('/api/personal/usage', {
    from: alignedFrom,
    to,
    interval: hourly ? '1 hour' : '1 day',
  })
  const contextHist = useApi<ContextBucketRow[]>('/api/personal/context-histogram')

  const data = usage.data
  const buckets = data?.buckets ?? []
  const t = data?.tokens

  // Cost over time: buckets are per-model per interval, so fold every model's
  // cost into one total per interval (keyed by the bucket boundary).
  const costTotals = new Map<string, number>()
  for (const b of buckets) {
    costTotals.set(b.bucket, (costTotals.get(b.bucket) ?? 0) + b.cost)
  }
  const costSeries = fillCostSeries(costTotals, alignedFrom, to, hourly)

  // Per-model usage (token volume) and cost share, folded from the same buckets.
  const byModel = new Map<string, { tokens: number; cost: number; inputCost: number; outputCost: number; inputTokens: number; outputTokens: number }>()
  for (const b of buckets) {
    const key = b.model || 'unknown'
    const cur = byModel.get(key) ?? { tokens: 0, cost: 0, inputCost: 0, outputCost: 0, inputTokens: 0, outputTokens: 0 }
    cur.tokens += tokenVolume(b.tokens)
    cur.cost += b.cost
    cur.inputCost += b.input_cost
    cur.outputCost += b.output_cost
    cur.inputTokens += b.tokens.input + b.tokens.cached
    cur.outputTokens += b.tokens.output
    byModel.set(key, cur)
  }
  const modelTokens = [...byModel.entries()].map(([label, v]) => ({ label, value: v.tokens }))
  const modelCost = [...byModel.entries()].map(([label, v]) => ({ label, value: v.cost }))

  // Per-model blended and directional $/1M-token rates, derived from the same
  // folded buckets so each card can flag how expensive a model is — and why
  // (input- vs output-heavy) — without a separate pricing call. Input tokens
  // fold in cache (prompt-side), matching how input cost includes cache spend.
  const perMillion = (cost: number, tokens: number) => (tokens > 0 ? (cost / tokens) * 1_000_000 : 0)
  const modelRate = new Map<string, ModelRate>()
  for (const [label, v] of byModel) {
    modelRate.set(label, {
      blended: perMillion(v.cost, v.tokens),
      input: perMillion(v.inputCost, v.inputTokens),
      output: perMillion(v.outputCost, v.outputTokens),
    })
  }
  const rateForModel = (label: string) => modelRate.get(label)

  // Assign each model a stable color by its overall token volume, so a given
  // model shows the same color in both the "Usage by Model" and "Cost by Model"
  // charts regardless of how it ranks within each.
  const modelColor = new Map<string, string>()
  ;[...byModel.entries()]
    .sort(([, a], [, b]) => b.tokens - a.tokens)
    .forEach(([label], i) => modelColor.set(label, MODEL_PALETTE[i % MODEL_PALETTE.length]))
  const colorForModel = (label: string) => modelColor.get(label) ?? MODEL_PALETTE[0]

  const hints = deriveHints(modelCost, rateForModel, contextHist.data)

  const total = t ? tokenVolume(t) : 0

  const tokenStats = [
    { label: 'Total Tokens', value: fmtTokens(total), sub: undefined as string | undefined },
    { label: 'Input Tokens', value: fmtTokens(t?.input ?? 0), sub: t && t.cached > 0 ? `${fmtTokens(t.cached)} cached` : undefined },
    { label: 'Output Tokens', value: fmtTokens(t?.output ?? 0), sub: t && t.reasoning > 0 ? `${fmtTokens(t.reasoning)} reasoning` : undefined },
  ]

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      {hints.length > 0 && (
        <div className="lg:col-span-2">
          <HintCards hints={hints} />
        </div>
      )}

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
        {usage.loading ? <PanelMessage>Loading…</PanelMessage> : <ShareBars rows={modelTokens} fmt={fmtTokens} colorFor={colorForModel} rateFor={rateForModel} labelFor={fmtModelName} expanded={modelExpanded} onToggle={() => setModelExpanded((v) => !v)} />}
      </Panel>

      <Panel title="Cost by Model" sub="Share of estimated spend">
        {usage.loading ? <PanelMessage>Loading…</PanelMessage> : <ShareBars rows={modelCost} fmt={fmtUsd} colorFor={colorForModel} rateFor={rateForModel} labelFor={fmtModelName} expanded={modelExpanded} onToggle={() => setModelExpanded((v) => !v)} />}
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
