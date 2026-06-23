import { CircleCheck } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import type { HTTPErrorsByCodeRow, HTTPSummaryRow, TimeseriesPoint, ToolStatRow } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { DataTable } from '../components/DataTable'
import { Bar, TimeseriesPanel, chartOptions } from '../components/charts'
import { fmtDuration, fmtTokens } from '../lib/format'

// avgPeak collapses a timeseries into the mean and max bucket total over the
// window. Series split across labels (e.g. server/client) are summed within
// each bucket first, so Peak is the busiest interval's combined total.
function avgPeak(points?: TimeseriesPoint[] | null): { avg: number; peak: number } | null {
  if (!points || points.length === 0) return null
  const byBucket = new Map<string, number>()
  for (const p of points) byBucket.set(p.bucket, (byBucket.get(p.bucket) ?? 0) + p.value)
  const totals = [...byBucket.values()]
  return {
    avg: totals.reduce((a, b) => a + b, 0) / totals.length,
    peak: Math.max(...totals),
  }
}

function AvgPeak({ stats, fmt }: { stats: { avg: number; peak: number } | null; fmt: (v: number) => string }) {
  if (!stats) return null
  return (
    <div className="whitespace-nowrap text-right text-xs text-gray-500">
      <span className="tabular-nums">Avg {fmt(stats.avg)}</span>
      <span className="mx-1.5 text-gray-300 dark:text-gray-700">·</span>
      <span className="tabular-nums">Peak {fmt(stats.peak)}</span>
    </div>
  )
}

const DIRECTION_SPECS = {
  server: { label: 'Server (inbound)', color: '#818cf8', fill: true },
  client: { label: 'Client (outbound)', color: '#34d399', fill: true },
}

const PERCENTILE_SPECS = {
  p50: { label: 'p50', color: '#34d399' },
  p95: { label: 'p95', color: '#fbbf24' },
  p99: { label: 'p99', color: '#ef4444' },
}

const httpSummaryColumns: ColumnDef<HTTPSummaryRow, any>[] = [
  { accessorKey: 'direction', header: 'Direction' },
  { accessorKey: 'method', header: 'Method' },
  {
    accessorKey: 'route',
    header: 'Route',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue() || '—'}</span>,
  },
  { accessorKey: 'total_requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'avg_duration', header: 'Avg Latency', meta: { align: 'right' }, cell: (c) => fmtDuration(c.getValue()) },
  {
    id: 'error_rate',
    header: 'Errors',
    meta: { align: 'right' },
    accessorFn: (r) => (r.total_requests > 0 ? r.error_count / r.total_requests : 0),
    cell: (c) => {
      const rate = c.getValue() as number
      return (
        <span className={rate > 0.05 ? 'text-red-500 dark:text-red-400' : rate > 0 ? 'text-amber-500' : ''}>
          {(rate * 100).toFixed(1)}%
        </span>
      )
    },
  },
]

const toolColumns: ColumnDef<ToolStatRow, any>[] = [
  {
    accessorKey: 'tool_name',
    header: 'Tool',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue()}</span>,
  },
  { accessorKey: 'count', header: 'Calls', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
  { accessorKey: 'avg_duration', header: 'Avg Duration', meta: { align: 'right' }, cell: (c) => fmtDuration(c.getValue()) },
  {
    id: 'error_rate',
    header: 'Errors',
    meta: { align: 'right' },
    accessorFn: (r) => (r.count > 0 ? r.error_count / r.count : 0),
    cell: (c) => {
      const rate = c.getValue() as number
      return (
        <span className={rate > 0.05 ? 'text-red-500 dark:text-red-400' : rate > 0 ? 'text-amber-500' : ''}>
          {(rate * 100).toFixed(1)}%
        </span>
      )
    },
  },
]

export function Operations() {
  const { spanMs } = useDash()
  const percentiles = useApi<TimeseriesPoint[]>('/api/ops/latency-percentiles')
  const ttfc = useApi<TimeseriesPoint[]>('/api/ops/ttfc-timeseries')
  const throughput = useApi<TimeseriesPoint[]>('/api/ops/throughput')
  const errorRate = useApi<TimeseriesPoint[]>('/api/ops/error-rate')
  const tools = useApi<ToolStatRow[]>('/api/ops/tools')
  const httpRequests = useApi<TimeseriesPoint[]>('/api/http/requests-timeseries')
  const httpLatency = useApi<TimeseriesPoint[]>('/api/http/timeseries')
  const httpErrorsByCode = useApi<HTTPErrorsByCodeRow[]>('/api/http/errors-by-code')
  const httpSummary = useApi<HTTPSummaryRow[]>('/api/http/summary')

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <Panel title="Latency Percentiles" sub="Per-request durations from spans">
        <TimeseriesPanel
          points={percentiles.data}
          spanMs={spanMs}
          specs={PERCENTILE_SPECS}
          yFmt={(v) => fmtDuration(v)}
          loading={percentiles.loading}
        />
      </Panel>

      <Panel title="Error Rate" sub="Share of failed GenAI operations">
        <TimeseriesPanel
          points={errorRate.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Errors', color: '#ef4444', fill: true } }}
          yFmt={(v) => v.toFixed(1) + '%'}
          loading={errorRate.loading}
        />
      </Panel>

      <Panel title="Time to First Chunk" sub="Streaming responsiveness per model">
        <TimeseriesPanel points={ttfc.data} spanMs={spanMs} yFmt={(v) => fmtDuration(v)} loading={ttfc.loading} />
      </Panel>

      <Panel
        title="Throughput"
        sub="Output tokens per second of model time"
        action={<AvgPeak stats={avgPeak(throughput.data)} fmt={(v) => fmtTokens(v) + '/s'} />}
      >
        <TimeseriesPanel
          points={throughput.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Tokens/sec', color: '#22d3ee', fill: true } }}
          yFmt={(v) => fmtTokens(v) + '/s'}
          loading={throughput.loading}
        />
      </Panel>

      <Panel
        title="HTTP Requests"
        sub="Inbound and outbound requests per interval"
        className="lg:col-span-2"
        action={<AvgPeak stats={avgPeak(httpRequests.data)} fmt={fmtTokens} />}
      >
        <TimeseriesPanel
          points={httpRequests.data}
          spanMs={spanMs}
          specs={DIRECTION_SPECS}
          yFmt={fmtTokens}
          loading={httpRequests.loading}
        />
      </Panel>

      <Panel title="HTTP Latency" sub="Average request duration">
        <TimeseriesPanel
          points={httpLatency.data}
          spanMs={spanMs}
          specs={DIRECTION_SPECS}
          yFmt={(v) => fmtDuration(v)}
          loading={httpLatency.loading}
        />
      </Panel>

      <Panel title="HTTP Errors by Status Code">
        {httpErrorsByCode.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (httpErrorsByCode.data ?? []).length === 0 ? (
          <PanelMessage>
            <CircleCheck className="h-4 w-4 text-emerald-500" />
            No errors
          </PanelMessage>
        ) : (
          <div className="h-56">
            <Bar
              data={{
                labels: httpErrorsByCode.data!.map((d) => String(d.status_code)),
                datasets: [{ data: httpErrorsByCode.data!.map((d) => d.count), backgroundColor: '#ef4444', borderRadius: 4 }],
              }}
              options={chartOptions()}
            />
          </div>
        )}
      </Panel>

      <Panel title="Tools" sub="execute_tool spans per tool">
        {tools.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (tools.data ?? []).length === 0 ? (
          <PanelMessage>
            <CircleCheck className="h-4 w-4 text-emerald-500" />
            No tool calls in range
          </PanelMessage>
        ) : (
          <DataTable data={tools.data!} columns={toolColumns} initialSort={[{ id: 'count', desc: true }]} />
        )}
      </Panel>

      <Panel title="Routes" sub="Latency and error rate per route" className="lg:col-span-2">
        {httpSummary.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (httpSummary.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={httpSummary.data!} columns={httpSummaryColumns} initialSort={[{ id: 'total_requests', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
