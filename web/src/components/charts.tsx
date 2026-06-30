import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Tooltip,
  Filler,
} from 'chart.js'
import { Line, Bar, Doughnut, Pie } from 'react-chartjs-2'
import { fmtBucket } from '../lib/format'
import type { TimeseriesPoint } from '../types'
import { useDash } from '../dash'
import { PanelMessage } from './Panel'

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Tooltip,
  Filler,
)

export const PALETTE = [
  '#818cf8', '#34d399', '#fbbf24', '#22d3ee', '#f472b6',
  '#a78bfa', '#fb923c', '#4ade80', '#60a5fa', '#e879f9',
]

// Neutral colors that stay readable in both light and dark mode.
const TICK = '#9ca3af'
const GRID = 'rgba(107, 114, 128, 0.18)'

// Chart.js legends render poorly (hollow rings, no spacing); ChartLegend
// below replaces them, so the built-in legend is always off.
export function chartOptions(extra?: { yFmt?: (v: number) => string; stacked?: boolean }) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { intersect: false, mode: 'index' as const },
    plugins: {
      legend: { display: false },
      tooltip: {
        backgroundColor: '#1f2937',
        titleColor: '#f3f4f6',
        bodyColor: '#d1d5db',
        borderColor: '#374151',
        borderWidth: 1,
        padding: 10,
        cornerRadius: 8,
        usePointStyle: true,
        boxWidth: 6,
        boxHeight: 6,
        boxPadding: 4,
      },
    },
    scales: {
      x: {
        stacked: extra?.stacked ?? false,
        ticks: { color: TICK, font: { size: 11 }, maxTicksLimit: 12 },
        grid: { color: GRID },
      },
      y: {
        stacked: extra?.stacked ?? false,
        ticks: {
          color: TICK,
          font: { size: 11 },
          callback: extra?.yFmt ? (v: any) => extra.yFmt!(Number(v)) : undefined,
        },
        grid: { color: GRID },
      },
    },
  }
}

export interface LegendItem {
  label: string
  color: string
}

export function ChartLegend({ items, align = 'end' }: { items: LegendItem[]; align?: 'start' | 'center' | 'end' }) {
  const justify = align === 'center' ? 'justify-center' : align === 'start' ? 'justify-start' : 'justify-end'
  return (
    <div className={`mb-3 flex flex-wrap items-center gap-x-4 gap-y-1.5 ${justify}`}>
      {items.map((item) => (
        <span key={item.label} className="inline-flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
          <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: item.color }} />
          {item.label}
        </span>
      ))}
    </div>
  )
}

export interface SeriesSpec {
  label: string
  color: string
  fill?: boolean
}

// partialBuckets flags each bucket whose interval is not fully contained in
// [from, to] — the leading bucket (clipped at the range start) and the trailing
// one (still filling, since `to` is usually "now"). These render dashed so a
// short final interval doesn't read as a real dip. Width is inferred from the
// smallest gap between consecutive buckets (== the interval). Returns all-false
// when the range or bucket spacing is unknown.
function partialBuckets(buckets: string[], from?: string, to?: string): boolean[] {
  if (!from || !to || buckets.length < 2) return buckets.map(() => false)
  const ts = buckets.map((b) => new Date(b).getTime())
  let width = Infinity
  for (let i = 1; i < ts.length; i++) width = Math.min(width, ts[i] - ts[i - 1])
  if (!isFinite(width) || width <= 0) return buckets.map(() => false)
  const fromMs = new Date(from).getTime()
  const toMs = new Date(to).getTime()
  const eps = 1000
  return ts.map((t) => t < fromMs - eps || t + width > toMs + eps)
}

// groupSeries pivots timeseries points (one row per bucket × label) into
// chart.js datasets, one per label. When opts.from/opts.to are supplied, the
// leading/trailing partial intervals are drawn dashed and dimmed.
export function groupSeries(
  points: TimeseriesPoint[],
  spanMs: number,
  specs?: Record<string, SeriesSpec>,
  opts?: { stacked?: boolean; from?: string; to?: string },
) {
  const buckets = [...new Set(points.map((p) => p.bucket))].sort()
  const labels = specs ? Object.keys(specs) : [...new Set(points.map((p) => p.label ?? ''))]
  const series = new Map<string, Map<string, number>>(labels.map((l) => [l, new Map()]))
  for (const p of points) {
    const m = series.get(p.label ?? '')
    if (m) m.set(p.bucket, (m.get(p.bucket) ?? 0) + p.value)
  }
  const partial = partialBuckets(buckets, opts?.from, opts?.to)
  const partialCount = partial.filter(Boolean).length
  const hasPartial = partialCount > 0
  return {
    buckets,
    hasPartial,
    partialCount,
    labels: buckets.map((b) => fmtBucket(b, spanMs)),
    datasets: labels
      // Drop genuinely-absent auto-derived labels, but keep caller-declared
      // series (specs) even when all-zero so e.g. a 0% cache-hit panel renders a
      // flat line instead of looking broken/blank.
      .filter((l) => specs || [...(series.get(l)?.values() ?? [])].some((v) => v !== 0))
      .map((l, i) => {
        const spec = specs?.[l]
        const color = spec?.color ?? PALETTE[i % PALETTE.length]
        const fill = opts?.stacked ? true : (spec?.fill ?? false)
        const basePoint = opts?.stacked ? 0 : 2
        return {
          label: spec?.label ?? (l || 'value'),
          data: buckets.map((b) => series.get(l)?.get(b) ?? 0),
          borderColor: color,
          backgroundColor: fill ? color + (opts?.stacked ? '4d' : '14') : color,
          fill,
          tension: 0.4,
          pointRadius: hasPartial
            ? (ctx: any) => (partial[ctx.dataIndex] ? Math.max(basePoint - 1, 0) : basePoint)
            : basePoint,
          pointHoverRadius: 5,
          borderWidth: opts?.stacked ? 1 : 2,
          // Dash the segments touching a partial interval and dim their border.
          ...(hasPartial && {
            segment: {
              borderDash: (ctx: any) =>
                partial[ctx.p0DataIndex] || partial[ctx.p1DataIndex] ? [5, 4] : undefined,
              borderColor: (ctx: any) =>
                partial[ctx.p0DataIndex] || partial[ctx.p1DataIndex] ? color + '80' : undefined,
            },
          }),
        }
      }),
  }
}

// PartialNote captions a chart whose edge intervals are incomplete, naming how
// many intervals are affected so a snapshot's clipped edges are quantified.
export function PartialNote({ count }: { count: number }) {
  return (
    <p className="mt-1.5 text-right text-[10px] text-gray-400 dark:text-gray-500">
      dashed = {count} incomplete interval{count === 1 ? '' : 's'}
    </p>
  )
}

export function TimeseriesPanel({
  points,
  spanMs,
  specs,
  yFmt,
  height = 'h-64',
  loading,
  legend = true,
  stacked = false,
}: {
  points: TimeseriesPoint[] | null
  spanMs: number
  specs?: Record<string, SeriesSpec>
  yFmt?: (v: number) => string
  height?: string
  loading?: boolean
  legend?: boolean
  stacked?: boolean
}) {
  const { from, to } = useDash()
  if (loading) return <PanelMessage>Loading…</PanelMessage>
  if (!points || points.length === 0) return <PanelMessage>No data</PanelMessage>
  const { labels, datasets, hasPartial, partialCount } = groupSeries(points, spanMs, specs, { stacked, from, to })
  return (
    <div>
      {legend && datasets.length > 1 && (
        <ChartLegend items={datasets.map((d) => ({ label: d.label, color: d.borderColor }))} />
      )}
      <div className={height}>
        <Line data={{ labels, datasets }} options={chartOptions({ yFmt, stacked })} />
      </div>
      {hasPartial && <PartialNote count={partialCount} />}
    </div>
  )
}

export { Line, Bar, Doughnut, Pie }
