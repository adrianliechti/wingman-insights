import { useMemo, useState } from 'react'
import { CircleCheck, Info } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash, useFilterNav } from '../dash'
import type {
  ClassificationDepartmentRow,
  ClassificationDepartmentTopicCell,
  ClassificationMatrixCell,
  ClassificationRow,
  ClassificationStats,
  TimeseriesPoint,
} from '../types'
import { Panel, PanelLoading, PanelMessage, Stale } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { Heatmap } from '../components/Heatmap'
import {
  Bar,
  TimeseriesPanel,
  chartOptions,
  ChartLegend,
  SegToggle,
  PALETTE,
  CHART,
} from '../components/charts'
import { fmtTokens, fmtTime } from '../lib/format'

// Category and risk ids are snake_case slugs from the wingman-chat config; show
// them as readable text but keep the raw slug as the tooltip.
const pretty = (id: string) => (id ? id.replace(/[_-]+/g, ' ') : 'unclassified')

const fmtPct = (v: number) => (v * 100).toFixed(1) + '%'
const fmtRate = (v: number) => v.toFixed(1)

// SectionHeader splits the page into its two questions: what users bring to the
// platform, and what that triggers. Panels carry their own smaller titles, so
// this is a size up with a rule under it to read as a divider.
function SectionHeader({ title, sub }: { title: string; sub: string }) {
  return (
    <div className="mt-2 border-b border-gray-200 pb-2 first:mt-0 dark:border-gray-800">
      <h2 className="text-base font-semibold tracking-tight text-gray-900 dark:text-white">{title}</h2>
      <p className="mt-0.5 text-xs text-gray-500">{sub}</p>
    </div>
  )
}

// RankedBars is a sorted bar list rather than a bar chart: risk and category
// slugs are long, and reading the ranking top-down beats squinting at rotated
// axis labels.
function RankedBars({ rows, color }: { rows: { id: string; value: number; note: string }[]; color: string }) {
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
    const desc = (m: Map<string, number>) => [...m.entries()].sort((a, b) => b[1] - a[1]).map(([k]) => k)
    return { categories: desc(triggersByCat), risks: desc(triggersByRisk), byKey, prompts }
  }, [cells])

  if (cells.length === 0) return <PanelMessage>No risk triggered in range</PanelMessage>

  const maxRate = Math.max(...cells.map((c) => (c.prompts > 0 ? (c.triggers / c.prompts) * 100 : 0)), 1)
  return (
    <Heatmap
      rows={categories}
      cols={risks}
      corner="topic"
      rowHeader={(cat) => (
        <span
          title={`${cat} — ${fmtTokens(prompts.get(cat) ?? 0)} prompts in conversations with this dominant topic`}
        >
          {pretty(cat)} <span className="text-gray-400">({fmtTokens(prompts.get(cat) ?? 0)})</span>
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

// rateVolumeFloor is the minimum classified prompts a department needs before
// its triggers-per-100 rate is allowed to rank it. Without a floor the rate
// ranking is pure noise in a large org: one prompt that happened to trigger
// reads as 100 per 100 and outranks every real hotspot. Departments below the
// floor are still listed in the table and still counted in the totals.
const rateVolumeFloor = 25

// deptAxisLabel keeps a department's free-text name from eating the plot area of
// a horizontal bar chart. Chart.js grows the tick area to fit the longest label,
// so an untruncated "Relationship Management Group 123" leaves almost no room
// for the bars; the full name stays in the tooltip.
const deptAxisLabel = (name: string) => (name.length > 26 ? name.slice(0, 25) + '…' : name)

// departmentChartRows is how many departments the risk chart bars. The full list
// lives in the table below it; this is only the leading slice, since a readable
// bar chart tops out long before a real tenant's department count.
const departmentChartRows = 15

// rankDepartments orders departments for the risk chart by the chosen metric and
// returns at most `limit` of them, plus how many were left out. Rate ranking
// additionally requires rateVolumeFloor prompts (see above); the absolute
// ranking needs no floor because volume speaks for itself.
function rankDepartments(rows: ClassificationDepartmentRow[], metric: 'rate' | 'triggers', limit: number) {
  const labelled = rows.map((r) => ({ ...r, label: r.department || 'Unassigned' }))
  const eligible =
    metric === 'rate'
      ? labelled.filter((r) => r.prompts >= rateVolumeFloor && r.triggers > 0)
      : labelled.filter((r) => r.triggers > 0)
  const sorted = [...eligible].sort((a, b) =>
    metric === 'rate'
      ? b.triggers_per_hundred - a.triggers_per_hundred || b.triggers - a.triggers
      : b.triggers - a.triggers || b.triggers_per_hundred - a.triggers_per_hundred,
  )
  return {
    shown: sorted.slice(0, limit),
    total: eligible.length,
    excluded: labelled.length - eligible.length,
  }
}

// DepartmentComparison ranks departments by risk exposure, either by rate
// (triggers per 100 prompts, which is comparable across team sizes) or by
// absolute triggers. Only the leading departments are plotted: a tenant can have
// hundreds, and a bar per department is neither readable nor useful — the table
// below the chart is the way to reach the rest.
//
// Bars are horizontal because department names are long free text, and a single
// metric is shown at a time rather than the two side by side: on one horizontal
// axis the two units cannot share a scale honestly.
function DepartmentComparison({
  rows,
  metric,
  limit,
}: {
  rows: ClassificationDepartmentRow[]
  metric: 'rate' | 'triggers'
  limit: number
}) {
  const { shown, total } = useMemo(() => rankDepartments(rows, metric, limit), [rows, metric, limit])

  if (rows.length === 0 || rows.every((r) => r.prompts === 0))
    return <PanelMessage>No classified activity in range</PanelMessage>
  if (shown.length === 0)
    return (
      <PanelMessage>
        <CircleCheck className="h-4 w-4 text-emerald-500" />
        {metric === 'rate'
          ? `No department with at least ${rateVolumeFloor} prompts triggered a risk`
          : 'No department triggered a risk in range'}
      </PanelMessage>
    )

  const rate = metric === 'rate'
  const fmtValue = rate ? fmtRate : fmtTokens
  const datasets = [
    {
      label: rate ? 'Triggers / 100 prompts' : 'Risk triggers',
      data: shown.map((r) => (rate ? Number(r.triggers_per_hundred.toFixed(1)) : r.triggers)),
      backgroundColor: rate ? CHART.negative : CHART.blue,
      borderRadius: 4,
    },
  ]

  const base = chartOptions()
  const options = {
    ...base,
    indexAxis: 'y' as const,
    scales: {
      x: {
        ...base.scales.y,
        ticks: { ...base.scales.y.ticks, callback: (v: any) => fmtValue(Number(v)) },
      },
      y: {
        ...base.scales.x,
        ticks: {
          ...base.scales.x.ticks,
          autoSkip: false,
          callback: (_v: any, i: number) => deptAxisLabel(shown[i]?.label ?? ''),
        },
      },
    },
    plugins: {
      ...base.plugins,
      tooltip: {
        ...base.plugins.tooltip,
        callbacks: {
          // Keyed on dataIndex, not the tick label, so the truncated axis text
          // can never break the lookup — and the tooltip shows the full name.
          title: (items: any[]) => shown[items[0]?.dataIndex]?.label ?? '',
          afterTitle: (items: any[]) => {
            const r = shown[items[0]?.dataIndex]
            if (!r) return ''
            return `${fmtTokens(r.prompts)} prompts · ${fmtTokens(r.users)} users${
              r.top_risk ? ` · top: ${pretty(r.top_risk)}` : ''
            }`
          },
          label: (ctx: any) => {
            const r = shown[ctx.dataIndex]
            if (!r) return ''
            return rate
              ? ` ${fmtRate(r.triggers_per_hundred)} per 100 prompts (${fmtTokens(r.triggers)} triggers)`
              : ` ${fmtTokens(r.triggers)} triggers (${fmtRate(r.triggers_per_hundred)} per 100)`
          },
        },
      },
    },
  }

  return (
    <div>
      <ChartLegend items={datasets.map((d) => ({ label: d.label, color: d.backgroundColor }))} />
      <div style={{ height: Math.max(180, shown.length * 26 + 40) }}>
        <Bar data={{ labels: shown.map((r) => r.label), datasets }} options={options} />
      </div>
      <p className="mt-2 text-[10px] text-gray-400 dark:text-gray-500">
        Top {shown.length} of {fmtTokens(total)} departments that triggered a risk
        {rate ? ` · rate ranking needs ≥ ${rateVolumeFloor} prompts` : ''} · full list below
      </p>
    </div>
  )
}

// DepartmentTopicMix stacks each department's prompts by topic — what each team
// actually brings to the platform. Topics keep one color across every stack, and
// departments stay ordered by prompt volume in both modes so bars don't jump
// when the mode is switched.
//
// Bars are horizontal because department names are free text and can be long;
// as vertical categories they collapse into rotated, unreadable labels well
// before the chart runs out of departments. The server caps how many are listed
// individually and folds the remainder into one "Other" group, which is pinned
// last — it is a summary of the tail, not a competitor in the ranking.
// Principals with no resolved department fold into "Unassigned".
//
// mode 'relative' normalizes each bar to 100%, which is what makes departments
// of very different sizes comparable: absolute bars are dominated by the biggest
// team, hiding that a small team's mix may be entirely different. The raw count
// stays in the tooltip either way, since a 100% bar built from a handful of
// prompts should not read as strongly as one built from thousands.
function DepartmentTopicMix({
  cells,
  mode,
}: {
  cells: ClassificationDepartmentTopicCell[]
  mode: 'absolute' | 'relative'
}) {
  const { departments, topics, byKey, totalByDept, otherLabel } = useMemo(() => {
    const totalByDept = new Map<string, number>()
    const totalByTopic = new Map<string, number>()
    const byKey = new Map<string, number>()
    let otherLabel = ''
    for (const c of cells) {
      // The Other group has no department name of its own, and must stay
      // distinct from the (also unnamed) unassigned bucket.
      if (c.other) otherLabel = `Other (${fmtTokens(c.other_count ?? 0)} depts)`
      const dept = c.other ? otherLabel : c.department || 'Unassigned'
      totalByDept.set(dept, (totalByDept.get(dept) ?? 0) + c.prompts)
      totalByTopic.set(c.category, (totalByTopic.get(c.category) ?? 0) + c.prompts)
      byKey.set(dept + '\0' + c.category, (byKey.get(dept + '\0' + c.category) ?? 0) + c.prompts)
    }
    const desc = (m: Map<string, number>) => [...m.entries()].sort((a, b) => b[1] - a[1]).map(([k]) => k)
    const ranked = desc(totalByDept).filter((d) => d !== otherLabel)
    return {
      departments: otherLabel ? [...ranked, otherLabel] : ranked,
      topics: desc(totalByTopic),
      byKey,
      totalByDept,
      otherLabel,
    }
  }, [cells])

  if (cells.length === 0) return <PanelMessage>No classified activity in range</PanelMessage>

  const relative = mode === 'relative'
  const raw = (dept: string, topic: string) => byKey.get(dept + '\0' + topic) ?? 0
  const datasets = topics.map((t, i) => ({
    label: pretty(t),
    data: departments.map((d) => {
      const n = raw(d, t)
      if (!relative) return n
      const total = totalByDept.get(d) ?? 0
      return total > 0 ? (n / total) * 100 : 0
    }),
    backgroundColor: PALETTE[i % PALETTE.length],
    borderRadius: 3,
  }))

  const base = chartOptions({ stacked: true })
  const fmtValue = relative ? (v: number) => v.toFixed(0) + '%' : fmtTokens
  const options = {
    ...base,
    indexAxis: 'y' as const,
    scales: {
      // Horizontal bars swap the roles: x carries the value, y the department.
      x: {
        ...base.scales.y,
        stacked: true,
        ticks: { ...base.scales.y.ticks, callback: (v: any) => fmtValue(Number(v)) },
        // Pin to 0–100 in relative mode so every bar is a full row and rounding
        // can't leave a sliver of empty space at the end.
        ...(relative ? { min: 0, max: 100 } : {}),
      },
      y: {
        ...base.scales.x,
        stacked: true,
        ticks: {
          ...base.scales.x.ticks,
          autoSkip: false,
          callback: (_v: any, i: number) => deptAxisLabel(departments[i] ?? ''),
        },
      },
    },
    plugins: {
      ...base.plugins,
      tooltip: {
        ...base.plugins.tooltip,
        callbacks: {
          // Keyed on dataIndex, not the tick label, so the truncated axis text
          // can never break the lookup — and the tooltip shows the full name.
          title: (items: any[]) => departments[items[0]?.dataIndex] ?? '',
          afterTitle: (items: any[]) =>
            `${fmtTokens(totalByDept.get(departments[items[0]?.dataIndex]) ?? 0)} prompts`,
          label: (ctx: any) => {
            const n = raw(departments[ctx.dataIndex] ?? '', topics[ctx.datasetIndex])
            if (n === 0) return ''
            return relative
              ? ` ${ctx.dataset.label}: ${Number(ctx.parsed.x).toFixed(1)}% (${fmtTokens(n)})`
              : ` ${ctx.dataset.label}: ${fmtTokens(n)}`
          },
        },
      },
    },
  }

  return (
    <div>
      <ChartLegend items={datasets.map((d) => ({ label: d.label, color: d.backgroundColor }))} />
      {/* Height follows the row count so bars keep a usable thickness whether
          the org has three departments or the capped maximum. */}
      <div style={{ height: Math.max(180, departments.length * 26 + 40) }}>
        <Bar data={{ labels: departments, datasets }} options={options} />
      </div>
      {otherLabel && (
        <p className="mt-2 text-[10px] text-gray-400 dark:text-gray-500">
          Smaller departments are summarized as “{otherLabel}”. Filter by department to inspect one.
        </p>
      )}
    </div>
  )
}

// departmentColumns is the full department list — the way to reach any of
// potentially hundreds of departments, which no chart can show. Sortable on
// every measure so the table doubles as the ranking the chart only previews.
const departmentColumns: ColumnDef<ClassificationDepartmentRow, any>[] = [
  {
    accessorKey: 'department',
    header: 'Department',
    cell: (c) => (
      <span className="font-medium text-gray-900 dark:text-white">
        {c.getValue() || <span className="font-normal text-gray-400">Unassigned</span>}
      </span>
    ),
  },
  {
    accessorKey: 'triggers',
    header: 'Triggers',
    meta: { align: 'right' },
    cell: (c) => fmtTokens(c.getValue()),
  },
  {
    accessorKey: 'triggers_per_hundred',
    header: 'Per 100 Prompts',
    meta: { align: 'right' },
    // Rates built on very little volume are shown but muted, so sorting by this
    // column doesn't present a one-prompt department as the top hotspot.
    cell: (c) => (
      <span
        className={
          c.row.original.prompts < rateVolumeFloor
            ? 'tabular-nums text-gray-400 dark:text-gray-600'
            : 'tabular-nums'
        }
        title={
          c.row.original.prompts < rateVolumeFloor
            ? `Only ${c.row.original.prompts} prompt${c.row.original.prompts === 1 ? '' : 's'} — low confidence`
            : undefined
        }
      >
        {fmtRate(c.getValue())}
      </span>
    ),
  },
  {
    accessorKey: 'top_risk',
    header: 'Top Risk',
    cell: (c) => (
      <span className="text-xs text-gray-600 dark:text-gray-300" title={c.getValue()}>
        {c.getValue() ? pretty(c.getValue()) : '—'}
      </span>
    ),
  },
  {
    accessorKey: 'prompts',
    header: 'Prompts',
    meta: { align: 'right' },
    cell: (c) => fmtTokens(c.getValue()),
  },
  {
    accessorKey: 'users',
    header: 'Users',
    meta: { align: 'right' },
    cell: (c) => fmtTokens(c.getValue()),
  },
]

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
      <span className="tabular-nums text-gray-500 dark:text-gray-400">{Number(c.getValue()).toFixed(2)}</span>
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
  const setFilter = useFilterNav()
  const [topicMixMode, setTopicMixMode] = useState<'absolute' | 'relative'>('absolute')
  const [deptMetric, setDeptMetric] = useState<'rate' | 'triggers'>('rate')
  const stats = useApi<ClassificationStats>('/api/classification/stats')
  const summary = useApi<ClassificationRow[]>('/api/classification/summary')

  const { categories, risks, triggered, riskSpecs, topicSpecs } = useMemo(() => {
    const rows = summary.data ?? []
    const categories = rows.filter((r) => r.kind === 'category').sort((a, b) => b.matched - a.matched)
    const risks = rows.filter((r) => r.kind === 'risk').sort((a, b) => b.matched - a.matched)
    const triggered = risks.filter((r) => r.matched > 0)
    // Only dimensions that actually occurred get a series; declaring the silent
    // ones would draw flat zero lines that hide the real ones.
    const spec = (ids: string[]) =>
      Object.fromEntries(
        ids.map((id, i) => [id, { label: pretty(id), color: PALETTE[i % PALETTE.length], fill: true }]),
      )
    return {
      categories,
      risks,
      triggered,
      riskSpecs: spec(triggered.map((r) => r.id)),
      topicSpecs: spec(categories.filter((c) => c.matched > 0).map((c) => c.id)),
    }
  }, [summary.data])

  // A deployment can enable either dimension on its own: wingman-chat emits a
  // data point per *configured* category/risk, so a kind absent from the summary
  // is not configured here (as opposed to configured-but-never-matched, which
  // does appear, with matched=0).
  //
  // Keyed on summary.data rather than a !loading flag: useApi keeps the previous
  // payload while refetching, so this stays stable across a range/filter change
  // instead of collapsing and re-expanding the section on every navigation. It
  // is null only before the first response, where the sections render their
  // normal loading state rather than claiming nothing is configured. Each
  // dimension's own endpoints are skipped (path=null) while it is known absent.
  const loaded = summary.data !== null
  const noRisks = loaded && risks.length === 0
  const noTopics = loaded && categories.length === 0
  const empty = loaded && (summary.data ?? []).length === 0

  const topicSeries = useApi<TimeseriesPoint[]>(noTopics ? null : '/api/classification/topic-timeseries')
  const deptTopics = useApi<ClassificationDepartmentTopicCell[]>(
    noTopics ? null : '/api/classification/department-topics',
  )
  const riskSeries = useApi<TimeseriesPoint[]>(noRisks ? null : '/api/classification/risk-timeseries')
  const matrix = useApi<ClassificationMatrixCell[]>(noRisks ? null : '/api/classification/matrix')
  const departments = useApi<ClassificationDepartmentRow[]>(
    noRisks ? null : '/api/classification/departments',
  )

  const prompts = stats.data?.prompts ?? 0
  const conversations = stats.data?.conversations ?? 0
  const promptTotal = categories.reduce((sum, c) => sum + c.matched, 0)
  const topTopic = categories.find((c) => c.matched > 0)
  const topRisk = triggered[0]

  // The riskiest team is only meaningful once the directory resolves departments
  // and something actually fired; without either, naming a department (at 0.0)
  // would read as a finding where there is none. It also shares the chart's
  // volume floor — in a large org the highest rate is otherwise always some
  // one-prompt department, which is noise rather than the headline.
  const riskiestDept = useMemo(() => {
    const named = (departments.data ?? []).filter(
      (d) => d.department && d.triggers > 0 && d.prompts >= rateVolumeFloor,
    )
    return [...named].sort((a, b) => b.triggers_per_hundred - a.triggers_per_hundred)[0]
  }, [departments.data])

  const topicStrip = [
    {
      label: 'Prompts classified',
      value: fmtTokens(prompts),
      sub: `${fmtTokens(conversations)} conversations · ${fmtTokens(stats.data?.users ?? 0)} users`,
    },
    {
      label: 'Top topic',
      value: topTopic && promptTotal > 0 ? fmtPct(topTopic.matched / promptTotal) : '—',
      sub: topTopic ? pretty(topTopic.id) : 'No topic classified in range',
    },
    {
      label: 'Topics in use',
      value: String(categories.filter((c) => c.matched > 0).length),
      sub: 'Distinct topics users brought',
    },
    {
      label: 'Prompts / conversation',
      value: conversations > 0 ? fmtRate(prompts / conversations) : '—',
      sub: 'How deep a typical conversation runs',
    },
  ]

  const riskStrip = [
    {
      label: 'Risk triggers',
      value: fmtTokens(stats.data?.triggers ?? 0),
      sub: `of ${fmtTokens(stats.data?.risk_evaluations ?? 0)} risk checks`,
    },
    {
      label: 'Triggers / 100 prompts',
      // Prompts come from the category dimension, so a risks-only deployment has
      // no denominator — report that rather than a misleading 0.0.
      value: prompts > 0 ? fmtRate(stats.data?.triggers_per_hundred ?? 0) : '—',
      sub: prompts > 0 ? 'Several risks can fire on one prompt' : 'No classified prompts to divide by',
    },
    {
      label: 'Top risk',
      value: topRisk ? fmtTokens(topRisk.matched) : '—',
      sub: topRisk ? pretty(topRisk.id) : 'No risk triggered in range',
    },
    {
      label: 'Riskiest department',
      value: riskiestDept ? fmtRate(riskiestDept.triggers_per_hundred) : '—',
      sub: riskiestDept
        ? `${riskiestDept.department} · per 100 prompts`
        : triggered.length === 0
          ? 'No risk triggered in range'
          : 'No departments resolved',
    },
  ]

  if (empty) {
    return (
      <div className="flex flex-col gap-5">
        <StatStrip stats={topicStrip} loading={stats.loading} firstLoad={stats.firstLoad} />
        <Panel title="Classification">
          <PanelMessage>
            <CircleCheck className="h-4 w-4 text-emerald-500" />
            No classification data in range — enable classification telemetry in wingman-chat
          </PanelMessage>
        </Panel>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-5">
      <SectionHeader
        title="What users are doing"
        sub={
          noTopics
            ? 'No topic taxonomy is enabled in this deployment'
            : 'The work users bring to the platform — one winning topic per classified prompt'
        }
      />
      {noTopics ? (
        <Panel title="No topics configured">
          <PanelMessage>
            <Info className="h-4 w-4 text-gray-400" />
            wingman-chat reports one evaluation per configured category; none appeared in this range. Define a
            topic taxonomy to see what users work on.
          </PanelMessage>
        </Panel>
      ) : (
        <>
          <StatStrip
            stats={topicStrip}
            loading={stats.loading || summary.loading}
            firstLoad={stats.firstLoad}
          />

          <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
            <Panel
              title="Topics over time"
              sub="Classified prompts per interval, stacked by topic"
              className="lg:col-span-2"
            >
              <TimeseriesPanel
                points={topicSeries.data}
                spanMs={spanMs}
                specs={Object.keys(topicSpecs).length > 0 ? topicSpecs : undefined}
                yFmt={fmtTokens}
                loading={topicSeries.loading}
                stacked
              />
            </Panel>

            <Panel title="What users work on" sub="Winning topic per prompt, share of all classified prompts">
              {summary.firstLoad ? (
                <PanelLoading />
              ) : (
                <Stale when={summary.stale}>
                  <RankedBars
                    rows={categories.map((c) => ({
                      id: c.id,
                      value: c.matched,
                      note: `${fmtTokens(c.matched)} · ${promptTotal > 0 ? fmtPct(c.matched / promptTotal) : '0%'}`,
                    }))}
                    color={CHART.blue}
                  />
                </Stale>
              )}
            </Panel>

            <Panel
              title="Topics by department"
              sub={
                topicMixMode === 'relative'
                  ? 'Each team’s topic mix as a share of its own prompts — comparable across team sizes'
                  : 'What each team uses the platform for, by prompt volume'
              }
              action={
                <SegToggle
                  value={topicMixMode}
                  options={['absolute', 'relative'] as const}
                  onChange={setTopicMixMode}
                />
              }
            >
              {deptTopics.firstLoad ? (
                <PanelLoading />
              ) : (
                <Stale when={deptTopics.stale}>
                  <DepartmentTopicMix cells={deptTopics.data ?? []} mode={topicMixMode} />
                </Stale>
              )}
            </Panel>
          </div>
        </>
      )}

      <SectionHeader
        title="Risks triggered"
        sub={
          noRisks
            ? 'No risk evaluation is enabled in this deployment'
            : 'What that work set off — every prompt is checked against each configured risk'
        }
      />
      {noRisks ? (
        <Panel title="No risks configured">
          <PanelMessage>
            <Info className="h-4 w-4 text-gray-400" />
            wingman-chat reports one evaluation per configured risk; none appeared in this range. Define risks
            to see which prompts trigger them.
          </PanelMessage>
        </Panel>
      ) : (
        <>
          <StatStrip
            stats={riskStrip}
            loading={stats.loading || summary.loading || departments.loading}
            firstLoad={stats.firstLoad}
          />

          <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
            <Panel
              title="Risk triggers over time"
              sub="Matched risks per interval — spikes point at the risk that caused them"
              className="lg:col-span-2"
            >
              <TimeseriesPanel
                points={riskSeries.data}
                spanMs={spanMs}
                specs={Object.keys(riskSpecs).length > 0 ? riskSpecs : undefined}
                yFmt={fmtTokens}
                loading={riskSeries.loading}
                stacked
              />
            </Panel>

            <Panel title="Most triggered risks" sub="Times the risk fired, and its share of evaluations">
              {summary.firstLoad ? (
                <PanelLoading />
              ) : triggered.length === 0 ? (
                <PanelMessage>
                  <CircleCheck className="h-4 w-4 text-emerald-500" />
                  No configured risk triggered in range
                </PanelMessage>
              ) : (
                <Stale when={summary.stale}>
                  <RankedBars
                    rows={triggered.map((r) => ({
                      id: r.id,
                      value: r.matched,
                      note: `${fmtTokens(r.matched)} · ${fmtPct(r.match_rate)}`,
                    }))}
                    color={CHART.negative}
                  />
                </Stale>
              )}
            </Panel>

            <Panel
              title="Risk by department"
              sub={
                deptMetric === 'rate'
                  ? 'Ranked by triggers per 100 prompts — comparable across team sizes'
                  : 'Ranked by absolute risk triggers'
              }
              action={
                <SegToggle
                  value={deptMetric}
                  options={['rate', 'triggers'] as const}
                  onChange={setDeptMetric}
                  labels={{ rate: 'per 100', triggers: 'total' }}
                />
              }
            >
              {departments.firstLoad ? (
                <PanelLoading />
              ) : (
                <Stale when={departments.stale}>
                  <DepartmentComparison
                    rows={departments.data ?? []}
                    metric={deptMetric}
                    limit={departmentChartRows}
                  />
                </Stale>
              )}
            </Panel>

            <Panel
              title="All departments"
              sub="Every department with classified activity — sort any column to rank"
              className="lg:col-span-2"
            >
              {departments.firstLoad ? (
                <PanelLoading />
              ) : (departments.data ?? []).length === 0 ? (
                <PanelMessage>No classified activity in range</PanelMessage>
              ) : (
                <DataTable
                  loading={departments.loading}
                  data={departments.data ?? []}
                  columns={departmentColumns}
                  initialSort={[{ id: 'triggers', desc: true }]}
                  initialLimit={25}
                  onRowClick={(r) => r.department && setFilter({ department: [r.department] })}
                />
              )}
            </Panel>

            <Panel
              title="Topics vs risks"
              sub="Conversations grouped by dominant topic (prompt count in brackets), shaded by triggers per 100 of those prompts"
              className="lg:col-span-2"
            >
              {matrix.firstLoad ? (
                <PanelLoading />
              ) : (
                <Stale when={matrix.stale}>
                  <TopicRiskMatrix cells={matrix.data ?? []} />
                </Stale>
              )}
            </Panel>

            <Panel
              title="Risk detail"
              sub="Trigger rate, confidence of the triggers, and the configured threshold"
              className="lg:col-span-2"
            >
              {summary.firstLoad ? (
                <PanelLoading />
              ) : (
                <DataTable
                  loading={summary.loading}
                  data={risks}
                  columns={riskColumns}
                  initialSort={[{ id: 'matched', desc: true }]}
                />
              )}
            </Panel>
          </div>
        </>
      )}
    </div>
  )
}
