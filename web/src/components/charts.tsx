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
import { Line, Bar, Doughnut } from 'react-chartjs-2'
import { fmtBucket } from '../lib/format'
import type { TimeseriesPoint } from '../types'
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
export function chartOptions(extra?: { yFmt?: (v: number) => string }) {
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
        ticks: { color: TICK, font: { size: 11 }, maxTicksLimit: 12 },
        grid: { color: GRID },
      },
      y: {
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

// groupSeries pivots timeseries points (one row per bucket × label) into
// chart.js datasets, one per label.
export function groupSeries(
  points: TimeseriesPoint[],
  spanMs: number,
  specs?: Record<string, SeriesSpec>,
) {
  const buckets = [...new Set(points.map((p) => p.bucket))].sort()
  const labels = specs ? Object.keys(specs) : [...new Set(points.map((p) => p.label ?? ''))]
  const series = new Map<string, Map<string, number>>(labels.map((l) => [l, new Map()]))
  for (const p of points) {
    const m = series.get(p.label ?? '')
    if (m) m.set(p.bucket, (m.get(p.bucket) ?? 0) + p.value)
  }
  return {
    buckets,
    labels: buckets.map((b) => fmtBucket(b, spanMs)),
    datasets: labels
      .filter((l) => [...(series.get(l)?.values() ?? [])].some((v) => v !== 0))
      .map((l, i) => {
        const spec = specs?.[l]
        const color = spec?.color ?? PALETTE[i % PALETTE.length]
        return {
          label: spec?.label ?? (l || 'value'),
          data: buckets.map((b) => series.get(l)?.get(b) ?? 0),
          borderColor: color,
          backgroundColor: spec?.fill ? color + '14' : color,
          fill: spec?.fill ?? false,
          tension: 0.4,
          pointRadius: 2,
          pointHoverRadius: 5,
          borderWidth: 2,
        }
      }),
  }
}

export function TimeseriesPanel({
  points,
  spanMs,
  specs,
  yFmt,
  height = 'h-64',
  loading,
  legend = true,
}: {
  points: TimeseriesPoint[] | null
  spanMs: number
  specs?: Record<string, SeriesSpec>
  yFmt?: (v: number) => string
  height?: string
  loading?: boolean
  legend?: boolean
}) {
  if (loading) return <PanelMessage>Loading…</PanelMessage>
  if (!points || points.length === 0) return <PanelMessage>No data</PanelMessage>
  const { labels, datasets } = groupSeries(points, spanMs, specs)
  return (
    <div>
      {legend && datasets.length > 1 && (
        <ChartLegend items={datasets.map((d) => ({ label: d.label, color: d.borderColor }))} />
      )}
      <div className={height}>
        <Line data={{ labels, datasets }} options={chartOptions({ yFmt })} />
      </div>
    </div>
  )
}

export { Line, Bar, Doughnut }
