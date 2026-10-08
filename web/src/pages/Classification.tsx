import { useMemo } from 'react'
import { CircleCheck } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import type {
  ClassificationDepartmentRow,
  ClassificationMatrixCell,
  ClassificationRow,
  ClassificationStats,
  TimeseriesPoint,
} from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { Heatmap } from '../components/Heatmap'
import { Bar, TimeseriesPanel, chartOptions, ChartLegend, PALETTE, CHART } from '../components/charts'
import { fmtTokens, fmtTime } from '../lib/format'

// Category and risk ids are snake_case slugs from the wingman-chat config; show
// them as readable text but keep the raw slug as the tooltip.
const pretty = (id: string) => (id ? id.replace(/[_-]+/g, ' ') : 'unclassified')

const fmtPct = (v: number) => (v * 100).toFixed(1) + '%'
const fmtRate = (v: number) => v.toFixed(1)

// RankedBars is a sorted bar list rather than a bar chart: risk and category
// slugs are long, and reading the ranking top-down beats squinting at rotated
// axis labels.
function RankedBars({
  rows,
  color,
}: {
  rows: { id: string; value: number; note: string }[]
  color: string
}) {
  const max = Math.max(1, ...rows.map((r) => r.value))
  if (rows.length === 0) return <PanelMessage>No data</PanelMessage>
  return (
    <div className="space-y-3">
      {rows.map((r) => (
        <div key={r.id}>
          <div className="mb-1 flex items-baseline justify-between gap-3 text-xs">
            <span className="truncate font-medium text-gray-700 dark:text-gray-300" title={r.id}>
              {pretty(r.id)}
            </span>
            <span className="shrink-0 tabular-nums text-gray-500">{r.note}</span>
          </div>
          <div className="h-2.5 overflow-hidden rounded-full bg-gray-100 dark:bg-gray-800">
            <div
              className="h-full rounded-full"
              style={{ width: `${(r.value / max) * 100}%`, backgroundColor: color }}
            />
          </div>
        </div>
      ))}
    </div>
  )
}

// TopicRiskMatrix crosses topics with risks: the cell is how often a risk fired
// in conversations about that topic, shaded by triggers per 100 of that topic's
// prompts so a small-but-risky topic doesn't disappear next to a large one.
function TopicRiskMatrix({ cells }: { cells: ClassificationMatrixCell[] }) {
  const { categories, risks, byKey, prompts } = useMemo(() => {
    const triggersByCat = new Map<string, number>()
    const triggersByRisk = new Map<string, number>()
    const prompts = new Map<string, number>()
    const byKey = new Map<string, number>()
    for (const c of cells) {
      triggersByCat.set(c.category, (triggersByCat.get(c.category) ?? 0) + c.triggers)
      triggersByRisk.set(c.risk, (triggersByRisk.get(c.risk) ?? 0) + c.triggers)
      prompts.set(c.category, c.prompts)
      byKey.set(c.category + '\0' + c.risk, c.triggers)
    }
    const desc = (m: Map<string, number>) =>
      [...m.entries()].sort((a, b) => b[1] - a[1]).map(([k]) => k)
    return { categories: desc(triggersByCat), risks: desc(triggersByRisk), byKey, prompts }
  }, [cells])

  if (cells.length === 0) return <PanelMessage>No risk triggered in range</PanelMessage>

  const maxRate = Math.max(
    ...cells.map((c) => (c.prompts > 0 ? (c.triggers / c.prompts) * 100 : 0)),
    1,
  )
  return (
    <Heatmap
      rows={categories}
      cols={risks}
      corner="topic"
      rowHeader={(cat) => (
        <span title={`${cat} — ${fmtTokens(prompts.get(cat) ?? 0)} prompts in conversations with this dominant topic`}>
          {pretty(cat)}{' '}
          <span className="text-gray-400">({fmtTokens(prompts.get(cat) ?? 0)})</span>
        </span>
      )}
      colHeader={(risk) => (
        <span className="inline-block max-w-[7rem] truncate align-middle" title={risk}>
          {pretty(risk)}
        </span>
      )}
      cell={(cat, risk) => {
        const triggers = byKey.get(cat + '\0' + risk)
        if (!triggers) return null
        const p = prompts.get(cat) ?? 0
        const rate = p > 0 ? (triggers / p) * 100 : 0
        const intensity = 0.12 + (rate / maxRate) * 0.78
        return {
          bg: `rgba(203, 44, 48, ${intensity})`,
          text: intensity > 0.6 ? '#fff' : undefined,
          title: `${pretty(risk)} fired ${triggers}× in ${pretty(cat)} conversations (${fmtRate(rate)} per 100 prompts)`,
          content: fmtTokens(triggers),
        }
      }}
    />
  )
}

// DepartmentComparison compares departments by risk exposure as a grouped bar:
// triggers per 100 prompts (the size-independent "how risky is this team"
// metric) next to raw triggers (absolute volume). Two y-axes because the metrics
// have different units; the tooltip adds prompt volume and the department's top
// risk. Departments with no resolved name fold into an "Unassigned" bar.
function DepartmentComparison({ rows }: { rows: ClassificationDepartmentRow[] }) {
  const sorted = [...rows]
    .map((r) => ({ ...r, label: r.department || 'Unassigned' }))
    .sort((a, b) => b.triggers_per_hundred - a.triggers_per_hundred)

  if (sorted.length === 0 || sorted.every((r) => r.prompts === 0))
    return <PanelMessage>No classified activity in range</PanelMessage>

  const topRisk = new Map(sorted.map((r) => [r.label, r.top_risk]))
  const prompts = new Map(sorted.map((r) => [r.label, r.prompts]))

  const datasets = [
    {
      label: 'Triggers / 100 prompts',
      data: sorted.map((r) => Number(r.triggers_per_hundred.toFixed(1))),
      backgroundColor: CHART.negative,
      borderRadius: 4,
      yAxisID: 'y',
    },
    {
      label: 'Risk triggers',
      data: sorted.map((r) => r.triggers),
      backgroundColor: CHART.blue,
      borderRadius: 4,
      yAxisID: 'y1',
    },
  ]

  const base = chartOptions()
  const options = {
    ...base,
    scales: {
      x: base.scales.x,
      y: {
        ...base.scales.y,
        position: 'left' as const,
        ticks: { ...base.scales.y.ticks, callback: (v: any) => fmtRate(Number(v)) },
        title: { display: true, text: 'per 100 prompts', color: '#8a8b8e', font: { size: 10 } },
      },
      y1: {
        ...base.scales.y,
        position: 'right' as const,
        grid: { drawOnChartArea: false },
        ticks: { ...base.scales.y.ticks, callback: (v: any) => fmtTokens(Number(v)) },
        title: { display: true, text: 'triggers', color: '#8a8b8e', font: { size: 10 } },
      },
    },
    plugins: {
      ...base.plugins,
      tooltip: {
        ...base.plugins.tooltip,
        callbacks: {
          afterTitle: (items: any[]) => {
            const label = items[0]?.label
            const p = prompts.get(label) ?? 0
            const risk = topRisk.get(label)
            return `${fmtTokens(p)} prompts${risk ? ` · top: ${pretty(risk)}` : ''}`
          },
          label: (ctx: any) => {
            const v = Number(ctx.parsed.y)
            const text = ctx.dataset.yAxisID === 'y1' ? fmtTokens(v) : fmtRate(v)
            return ` ${ctx.dataset.label}: ${text}`
          },
        },
      },
    },
  }

  return (
    <div>
      <ChartLegend items={datasets.map((d) => ({ label: d.label, color: d.backgroundColor }))} />
      <div className="h-72">
        <Bar data={{ labels: sorted.map((r) => r.label), datasets }} options={options} />
      </div>
    </div>
  )
}

const riskColumns: ColumnDef<ClassificationRow, any>[] = [
  {
    accessorKey: 'id',
    header: 'Risk',
    cell: (c) => (
      <span className="font-medium text-gray-900 dark:text-white" title={c.getValue()}>
        {pretty(c.getValue())}
      </span>
    ),
  },
  {
    accessorKey: 'matched',
    header: 'Triggers',
    meta: { align: 'right' },
    cell: (c) => fmtTokens(c.getValue()),
  },
  {
    accessorKey: 'match_rate',
    header: 'Trigger Rate',
    meta: { align: 'right' },
    cell: (c) => fmtPct(c.getValue()),
  },
  {
    accessorKey: 'avg_score',
    header: 'Avg Score When Triggered',
    meta: { align: 'right' },
    cell: (c) => (
      <span className="tabular-nums text-gray-500 dark:text-gray-400">
        {c.row.original.matched > 0 ? Number(c.getValue()).toFixed(2) : '—'}
      </span>
    ),
  },
  {
    accessorKey: 'threshold',
    header: 'Threshold',
    meta: { align: 'right' },
    cell: (c) => (
      <span className="tabular-nums text-gray-500 dark:text-gray-400">
        {Number(c.getValue()).toFixed(2)}
      </span>
    ),
  },
  {
    accessorKey: 'last_matched',
    header: 'Last Trigger',
    meta: { align: 'right' },
    cell: (c) => (
      <span className="whitespace-nowrap text-xs text-gray-500">
        {c.getValue() ? fmtTime(c.getValue()) : '—'}
      </span>
    ),
  },
]

export function Classification() {
  const { spanMs } = useDash()
  const stats = useApi<ClassificationStats>('/api/classification/stats')
  const summary = useApi<ClassificationRow[]>('/api/classification/summary')
  const riskSeries = useApi<TimeseriesPoint[]>('/api/classification/risk-timeseries')
  const matrix = useApi<ClassificationMatrixCell[]>('/api/classification/matrix')
  const departments = useApi<ClassificationDepartmentRow[]>('/api/classification/departments')

  const { categories, risks, triggered, specs } = useMemo(() => {
    const rows = summary.data ?? []
    const categories = rows
      .filter((r) => r.kind === 'category')
      .sort((a, b) => b.matched - a.matched)
    const risks = rows.filter((r) => r.kind === 'risk').sort((a, b) => b.matched - a.matched)
    const triggered = risks.filter((r) => r.matched > 0)
    // Only risks that actually fired get a series; declaring the silent ones
    // would draw flat zero lines that hide the real ones.
    const specs = Object.fromEntries(
      triggered.map((r, i) => [
        r.id,
        { label: pretty(r.id), color: PALETTE[i % PALETTE.length], fill: true },
      ]),
    )
    return { categories, risks, triggered, specs }
  }, [summary.data])

  const prompts = stats.data?.prompts ?? 0
  const promptTotal = categories.reduce((sum, c) => sum + c.matched, 0)
  const topRisk = triggered[0]
  const empty = !summary.loading && (summary.data ?? []).length === 0

  const strip = [
    {
      label: 'Prompts classified',
      value: fmtTokens(prompts),
      sub: `${fmtTokens(stats.data?.conversations ?? 0)} conversations · ${fmtTokens(stats.data?.users ?? 0)} users`,
    },
    {
      label: 'Risk triggers',
      value: fmtTokens(stats.data?.triggers ?? 0),
      sub: `of ${fmtTokens(stats.data?.risk_evaluations ?? 0)} risk checks`,
    },
    {
      label: 'Triggers / 100 prompts',
      value: fmtRate(stats.data?.triggers_per_hundred ?? 0),
      sub: 'Several risks can fire on one prompt',
    },
    {
      label: 'Top risk',
      value: topRisk ? fmtTokens(topRisk.matched) : '—',
      sub: topRisk ? pretty(topRisk.id) : 'No risk triggered in range',
    },
  ]

  return (
    <div className="flex flex-col gap-5">
      <StatStrip stats={strip} />

      {empty && (
        <Panel title="Classification">
          <PanelMessage>
            <CircleCheck className="h-4 w-4 text-emerald-500" />
            No classification data in range — enable classification telemetry in wingman-chat
          </PanelMessage>
        </Panel>
      )}

      {!empty && (
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
          <Panel
            title="Risk triggers over time"
            sub="Matched risks per interval — spikes point at the risk that caused them"
            className="lg:col-span-2"
          >
            <TimeseriesPanel
              points={riskSeries.data}
              spanMs={spanMs}
              specs={Object.keys(specs).length > 0 ? specs : undefined}
              yFmt={fmtTokens}
              loading={riskSeries.loading}
              stacked
            />
          </Panel>

          <Panel title="Most triggered risks" sub="Times the risk fired, and its share of evaluations">
            {summary.loading ? (
              <PanelMessage>Loading…</PanelMessage>
            ) : (
              <RankedBars
                rows={triggered.map((r) => ({
                  id: r.id,
                  value: r.matched,
                  note: `${fmtTokens(r.matched)} · ${fmtPct(r.match_rate)}`,
                }))}
                color={CHART.negative}
              />
            )}
          </Panel>

          <Panel title="What users work on" sub="Winning topic per prompt, share of all classified prompts">
            {summary.loading ? (
              <PanelMessage>Loading…</PanelMessage>
            ) : (
              <RankedBars
                rows={categories.map((c) => ({
                  id: c.id,
                  value: c.matched,
                  note: `${fmtTokens(c.matched)} · ${promptTotal > 0 ? fmtPct(c.matched / promptTotal) : '0%'}`,
                }))}
                color={CHART.blue}
              />
            )}
          </Panel>

          <Panel
            title="Topics vs risks"
            sub="Conversations grouped by dominant topic (prompt count in brackets), shaded by triggers per 100 of those prompts"
            className="lg:col-span-2"
          >
            {matrix.loading ? <PanelMessage>Loading…</PanelMessage> : <TopicRiskMatrix cells={matrix.data ?? []} />}
          </Panel>

          <Panel
            title="Risk by department"
            sub="Which teams trigger the most risk — triggers per 100 prompts (size-independent) and absolute triggers"
            className="lg:col-span-2"
          >
            {departments.loading ? (
              <PanelMessage>Loading…</PanelMessage>
            ) : (
              <DepartmentComparison rows={departments.data ?? []} />
            )}
          </Panel>

          <Panel
            title="Risk detail"
            sub="Trigger rate, confidence of the triggers, and the configured threshold"
            className="lg:col-span-2"
          >
            {summary.loading ? (
              <PanelMessage>Loading…</PanelMessage>
            ) : risks.length === 0 ? (
              <PanelMessage>No risks configured</PanelMessage>
            ) : (
              <DataTable data={risks} columns={riskColumns} initialSort={[{ id: 'matched', desc: true }]} />
            )}
          </Panel>
        </div>
      )}
    </div>
  )
}
