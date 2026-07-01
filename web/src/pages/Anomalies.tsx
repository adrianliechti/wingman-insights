import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash, useFilterNav } from '../dash'
import type { AnomalyFeedRow, BurstRow, ScorePoint, TopConsumerRow } from '../types'
import type { TimeseriesPoint } from '../types'
import { KindBadge, UserCell } from '../components/UserCell'
import { Panel, PanelMessage } from '../components/Panel'
import { StatStrip } from '../components/StatCard'
import { DataTable } from '../components/DataTable'
import { TokenChart } from '../components/TokenChart'
import { Heatmap } from '../components/Heatmap'
import { ChartLegend, Line, chartOptions, groupSeries } from '../components/charts'
import { fmtCost, fmtTime, fmtTokens } from '../lib/format'

const fmtMetric = (metric: string, v: number) => (metric === 'cost' ? fmtCost(v) : fmtTokens(v))

function Badge({ text, tone = 'gray' }: { text: string; tone?: 'gray' | 'indigo' | 'amber' }) {
  const cls = {
    gray: 'bg-gray-500/10 text-gray-600 dark:text-gray-300',
    indigo: 'bg-indigo-500/10 text-indigo-600 dark:text-indigo-400',
    amber: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
  }[tone]
  return <span className={`rounded px-1.5 py-0.5 text-xs font-medium capitalize ${cls}`}>{text}</span>
}

function Severity({ score }: { score: number }) {
  const critical = score >= 5
  return (
    <span
      className={`rounded px-1.5 py-0.5 text-xs font-medium ${
        critical ? 'bg-red-500/10 text-red-600 dark:text-red-400' : 'bg-amber-500/10 text-amber-600 dark:text-amber-400'
      }`}
    >
      {critical ? 'critical' : 'warning'}
    </span>
  )
}

// SpikeLine plots a scored series (cost) as an area with red markers on buckets
// flagged ≥ 3σ — the spend counterpart to the token TokenChart.
function SpikeLine({ points, yFmt, color, label }: { points: ScorePoint[] | null; yFmt: (v: number) => string; color: string; label: string }) {
  const { spanMs, from, to } = useDash()
  if (!points || points.length === 0) return <PanelMessage>No data</PanelMessage>
  const ts: TimeseriesPoint[] = points.map((p) => ({ bucket: p.bucket, value: p.value, count: 0, label }))
  const { buckets, labels, datasets } = groupSeries(ts, spanMs, { [label]: { label, color, fill: true } }, { from, to })
  const flagged = points.filter((p) => p.score >= 3)
  const markers = buckets.map((b) => {
    const hits = flagged.filter((p) => p.bucket === b)
    return hits.length ? Math.max(...hits.map((p) => p.value)) : null
  })
  const ds: any[] = [...datasets]
  if (markers.some((v) => v !== null)) {
    ds.push({
      label: 'Anomaly',
      data: markers,
      showLine: false,
      pointRadius: 6,
      pointHoverRadius: 8,
      pointStyle: 'rectRot',
      borderColor: '#ef4444',
      backgroundColor: '#ef4444',
      borderWidth: 2,
    })
  }
  return (
    <div>
      <ChartLegend items={ds.map((d) => ({ label: d.label, color: d.borderColor }))} />
      <div className="h-72">
        <Line data={{ labels, datasets: ds }} options={chartOptions({ yFmt })} />
      </div>
    </div>
  )
}

// AnomalyLeaderboard ranks entities (apps or users) by their worst z-score in
// the feed — the "who is spiking most" view for one dimension.
function AnomalyLeaderboard({ feed, dimension }: { feed: AnomalyFeedRow[]; dimension: string }) {
  const byEntity = new Map<string, number>()
  const labelOf = new Map<string, string>()
  for (const r of feed) {
    if (r.dimension !== dimension) continue
    byEntity.set(r.group_key, Math.max(byEntity.get(r.group_key) ?? 0, r.score))
    labelOf.set(r.group_key, r.name || r.group_key) // name for users, raw key otherwise
  }
  const rows = [...byEntity.entries()].map(([k, score]) => ({ k, label: labelOf.get(k) || k, score })).sort((a, b) => b.score - a.score).slice(0, 8)
  if (rows.length === 0) return <PanelMessage>No anomalies</PanelMessage>
  const max = rows[0].score || 1 // rows are sorted desc; guard a 0 top score
  return (
    <div className="space-y-2">
      {rows.map((r) => (
        <div key={r.k} className="flex items-center gap-3">
          <span className="w-40 shrink-0 truncate text-xs text-gray-700 dark:text-gray-300" title={r.k}>
            {r.label}
          </span>
          <div className="h-2 flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-gray-800">
            <div
              className="h-full rounded-full"
              style={{ width: `${(r.score / max) * 100}%`, backgroundColor: r.score >= 5 ? '#ef4444' : '#fbbf24' }}
            />
          </div>
          <span className="w-10 shrink-0 text-right text-xs tabular-nums text-gray-500">{r.score.toFixed(1)}σ</span>
        </div>
      ))}
    </div>
  )
}

const consumerColumns: ColumnDef<TopConsumerRow, any>[] = [
  { id: 'user', header: 'User', cell: (c) => <UserCell r={c.row.original} /> },
  { id: 'kind', accessorKey: 'kind', header: 'Kind', cell: (c) => <KindBadge kind={c.getValue()} /> },
  { accessorKey: 'total_requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'total_tokens', header: 'Tokens', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'tpm', header: 'TPM', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) + '/min' },
]

const burstColumns: ColumnDef<BurstRow, any>[] = [
  { id: 'user', header: 'User', cell: (c) => <UserCell r={c.row.original} /> },
  { id: 'kind', accessorKey: 'kind', header: 'Kind', cell: (c) => <KindBadge kind={c.getValue()} /> },
  {
    accessorKey: 'peak_rpm',
    header: 'Peak / min',
    meta: { align: 'right' },
    cell: (c) => {
      const v = c.getValue() as number
      return <span className={v >= 30 ? 'font-medium text-red-500 dark:text-red-400' : ''}>{v}</span>
    },
  },
  { accessorKey: 'total_requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'active_minutes', header: 'Active min', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
]

const DIMS = [
  { value: 'user', label: 'By user' },
  { value: 'app', label: 'By application' },
  { value: 'model', label: 'By model' },
]

export function Anomalies() {
  const [heatDim, setHeatDim] = useState('user')
  const setFilter = useFilterNav()
  const feed = useApi<AnomalyFeedRow[]>('/api/genai/anomaly-feed')
  const costSpikes = useApi<ScorePoint[]>('/api/genai/cost-anomaly-timeseries')
  const topConsumers = useApi<TopConsumerRow[]>('/api/ops/top-consumers')
  const burst = useApi<BurstRow[]>('/api/customers/burst')

  const rows = feed.data ?? []
  const worst = rows.reduce((m, r) => Math.max(m, r.score), 0)
  const usersFlagged = new Set(rows.filter((r) => r.dimension === 'user').map((r) => r.group_key)).size
  const appsFlagged = new Set(rows.filter((r) => r.dimension === 'app').map((r) => r.group_key)).size

  const feedColumns: ColumnDef<AnomalyFeedRow, any>[] = [
    { accessorKey: 'bucket', header: 'When', cell: (c) => fmtTime(c.getValue()) },
    { accessorKey: 'dimension', header: 'Dimension', cell: (c) => <Badge text={c.getValue() === 'app' ? 'application' : c.getValue()} /> },
    {
      accessorKey: 'group_key',
      header: 'Entity',
      // name resolves user GUIDs to a display name; app/model keep the raw key.
      cell: (c) => <span className="text-xs text-gray-900 dark:text-gray-100">{c.row.original.name || c.getValue() || '—'}</span>,
    },
    { accessorKey: 'metric', header: 'Metric', cell: (c) => <Badge text={c.getValue()} tone={c.getValue() === 'cost' ? 'amber' : 'indigo'} /> },
    {
      accessorKey: 'value',
      header: 'Actual',
      meta: { align: 'right' },
      cell: (c) => fmtMetric(c.row.original.metric, c.getValue()),
    },
    {
      accessorKey: 'expected',
      header: 'Expected',
      meta: { align: 'right' },
      cell: (c) => fmtMetric(c.row.original.metric, c.getValue()),
    },
    {
      id: 'deviation',
      header: 'Deviation',
      meta: { align: 'right' },
      accessorFn: (r) => (r.expected > 0 ? r.value / r.expected : 0),
      cell: (c) => <span className="font-medium text-gray-900 dark:text-white">×{(c.getValue() as number).toFixed(1)}</span>,
    },
    { accessorKey: 'score', header: 'Z-Score', meta: { align: 'right' }, cell: (c) => (c.getValue() as number).toFixed(1) },
    { id: 'severity', header: 'Severity', accessorFn: (r) => r.score, cell: (c) => <Severity score={c.getValue()} /> },
  ]

  // Spike heatmap from the feed: entity × bucket, cell tinted by max z-score.
  const dimRows = rows.filter((r) => r.dimension === heatDim)
  const heatBuckets = [...new Set(dimRows.map((r) => r.bucket))].sort()
  const heatEntities = (() => {
    const byEntity = new Map<string, number>()
    for (const r of dimRows) byEntity.set(r.group_key, Math.max(byEntity.get(r.group_key) ?? 0, r.score))
    return [...byEntity.entries()].sort((a, b) => b[1] - a[1]).slice(0, 12).map(([k]) => k)
  })()
  // Cost and token rows share (entity, bucket) keys — keep the max score, not
  // whichever row happens to come last.
  const heatScore = new Map<string, number>()
  for (const r of dimRows) {
    const k = r.group_key + '\0' + r.bucket
    heatScore.set(k, Math.max(heatScore.get(k) ?? 0, r.score))
  }
  const heatLabel = new Map(dimRows.map((r) => [r.group_key, r.name || r.group_key])) // name for users

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <div className="lg:col-span-2">
        <StatStrip
          stats={[
            { label: 'Open Anomalies', value: String(rows.length), sub: '≥ 3σ above baseline' },
            { label: 'Worst Z-Score', value: worst ? worst.toFixed(1) + 'σ' : '—', sub: worst >= 5 ? 'critical' : 'warning' },
            { label: 'Users Flagged', value: String(usersFlagged), sub: 'distinct end users' },
            { label: 'Applications Flagged', value: String(appsFlagged), sub: 'distinct applications' },
          ]}
        />
      </div>

      <TokenChart className="lg:col-span-2" />

      <Panel title="Spend Spikes" sub="Cost per interval; markers flag spend ≥ 3σ above the rolling baseline" className="lg:col-span-2">
        <SpikeLine points={costSpikes.data} yFmt={fmtCost} color="#fbbf24" label="Cost" />
      </Panel>

      <Panel
        title="Anomaly Feed"
        sub="Strongest spend and token spikes ranked across users, applications and models · click a row to filter"
        className="lg:col-span-2"
      >
        {feed.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : rows.length === 0 ? (
          <PanelMessage>
            <AlertTriangle className="h-4 w-4 opacity-50" />
            No anomalies in this time range
          </PanelMessage>
        ) : (
          <DataTable
            data={rows}
            columns={feedColumns}
            initialSort={[{ id: 'score', desc: true }]}
            onRowClick={(r) => {
              if (!r.group_key) return
              if (r.dimension === 'user') setFilter({ user: r.group_key })
              else if (r.dimension === 'app') setFilter({ app: r.group_key })
              else setFilter({ models: [r.group_key] })
            }}
          />
        )}
      </Panel>

      <Panel title="Top Applications by Deviation" sub="Worst z-score per application">
        <AnomalyLeaderboard feed={rows} dimension="app" />
      </Panel>

      <Panel title="Top Users by Deviation" sub="Worst z-score per user">
        <AnomalyLeaderboard feed={rows} dimension="user" />
      </Panel>

      <Panel
        title="Spike Heatmap"
        sub="Where and when consumption spiked"
        className="lg:col-span-2"
        action={
          <select
            value={heatDim}
            onChange={(e) => setHeatDim(e.target.value)}
            className="rounded-lg border border-gray-200 bg-white px-2 py-1 text-xs font-medium text-gray-700 outline-none dark:border-gray-800 dark:bg-gray-900 dark:text-gray-300"
          >
            {DIMS.map((d) => (
              <option key={d.value} value={d.value}>
                {d.label}
              </option>
            ))}
          </select>
        }
      >
        {heatEntities.length === 0 ? (
          <PanelMessage>No anomalies in this time range</PanelMessage>
        ) : (
          <Heatmap
            rows={heatEntities}
            cols={heatBuckets}
            corner={heatDim === 'app' ? 'application' : heatDim}
            rowHeader={(e) => <span>{heatLabel.get(e) || e}</span>}
            colHeader={(b) => fmtTime(b).replace(',', '')}
            cell={(e, b) => {
              const s = heatScore.get(e + '\0' + b)
              if (s === undefined) return null
              const alpha = Math.min(s / 8, 1)
              return {
                bg: `rgba(239, 68, 68, ${0.15 + alpha * 0.7})`,
                text: alpha > 0.5 ? '#fff' : undefined,
                title: `${heatLabel.get(e) || e} · ${fmtTime(b)} · ${s.toFixed(1)}σ`,
                content: s.toFixed(1),
              }
            }}
          />
        )}
      </Panel>

      <Panel title="Top Consumers" sub="Heaviest users by token volume — click a row to filter to them">
        {topConsumers.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (topConsumers.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable
            data={topConsumers.data!}
            columns={consumerColumns}
            initialSort={[{ id: 'total_tokens', desc: true }]}
            onRowClick={(r) => r.id && setFilter({ user: r.id })}
          />
        )}
      </Panel>

      <Panel title="Request Bursts" sub="Peak requests/min per user — a runaway agent loop spikes here · click a row to filter">
        {burst.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (burst.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable
            data={burst.data!}
            columns={burstColumns}
            initialSort={[{ id: 'peak_rpm', desc: true }]}
            onRowClick={(r) => r.id && setFilter({ user: r.id })}
          />
        )}
      </Panel>
    </div>
  )
}
