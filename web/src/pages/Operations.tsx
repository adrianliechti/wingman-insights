import { useState } from 'react'
import { AlertTriangle, CircleCheck } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import type {
  AnomalyPoint,
  HTTPErrorsByCodeRow,
  HTTPSummaryRow,
  TimeseriesPoint,
  ToolStatRow,
  TopRouteRow,
} from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { DataTable } from '../components/DataTable'
import { TokenChart } from '../components/TokenChart'
import { Bar, TimeseriesPanel, chartOptions } from '../components/charts'
import { fmtDuration, fmtTime, fmtTokens } from '../lib/format'

const DIRECTION_SPECS = {
  server: { label: 'Server (inbound)', color: '#818cf8', fill: true },
  client: { label: 'Client (outbound)', color: '#34d399', fill: true },
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

const routeColumns: ColumnDef<TopRouteRow, any>[] = [
  { accessorKey: 'method', header: 'Method' },
  {
    accessorKey: 'route',
    header: 'Route',
    cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue()}</span>,
  },
  { accessorKey: 'total_requests', header: 'Requests', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
]

const GROUPS = [
  { value: 'user', label: 'By user' },
  { value: 'service', label: 'By service' },
  { value: 'model', label: 'By model' },
]

const PERCENTILE_SPECS = {
  p50: { label: 'p50', color: '#34d399' },
  p95: { label: 'p95', color: '#fbbf24' },
  p99: { label: 'p99', color: '#ef4444' },
}

function Severity({ score }: { score: number }) {
  const critical = score >= 5
  return (
    <span
      className={`rounded px-1.5 py-0.5 text-xs font-medium ${
        critical
          ? 'bg-red-500/10 text-red-600 dark:text-red-400'
          : 'bg-amber-500/10 text-amber-600 dark:text-amber-400'
      }`}
    >
      {critical ? 'critical' : 'warning'}
    </span>
  )
}

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
  const [groupBy, setGroupBy] = useState('user')
  const anomalies = useApi<AnomalyPoint[]>('/api/genai/anomalies', { group_by: groupBy })
  const percentiles = useApi<TimeseriesPoint[]>('/api/ops/latency-percentiles')
  const ttfc = useApi<TimeseriesPoint[]>('/api/ops/ttfc-timeseries')
  const throughput = useApi<TimeseriesPoint[]>('/api/ops/throughput')
  const errorRate = useApi<TimeseriesPoint[]>('/api/ops/error-rate')
  const tools = useApi<ToolStatRow[]>('/api/ops/tools')
  const httpRequests = useApi<TimeseriesPoint[]>('/api/http/requests-timeseries')
  const httpLatency = useApi<TimeseriesPoint[]>('/api/http/timeseries')
  const httpErrorsByCode = useApi<HTTPErrorsByCodeRow[]>('/api/http/errors-by-code')
  const topRoutes = useApi<TopRouteRow[]>('/api/http/top-routes')
  const httpSummary = useApi<HTTPSummaryRow[]>('/api/http/summary')

  const anomalyColumns: ColumnDef<AnomalyPoint, any>[] = [
    { accessorKey: 'bucket', header: 'When', cell: (c) => fmtTime(c.getValue()) },
    {
      accessorKey: 'group_key',
      header: groupBy === 'user' ? 'User' : groupBy === 'service' ? 'Service' : 'Model',
      cell: (c) => <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{c.getValue() || '—'}</span>,
    },
    { accessorKey: 'token_type', header: 'Token Type' },
    { accessorKey: 'tokens', header: 'Tokens', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
    { accessorKey: 'expected', header: 'Expected', meta: { align: 'right' }, cell: (c) => fmtTokens(c.getValue()) },
    {
      id: 'deviation',
      header: 'Deviation',
      meta: { align: 'right' },
      accessorFn: (r) => (r.expected > 0 ? r.tokens / r.expected : 0),
      cell: (c) => <span className="font-medium text-gray-900 dark:text-white">×{(c.getValue() as number).toFixed(1)}</span>,
    },
    { accessorKey: 'score', header: 'Z-Score', meta: { align: 'right' }, cell: (c) => (c.getValue() as number).toFixed(1) },
    { id: 'severity', header: 'Severity', accessorFn: (r) => r.score, cell: (c) => <Severity score={c.getValue()} /> },
  ]

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <TokenChart className="lg:col-span-2" />

      <Panel
        title="Detected Anomalies"
        sub="Intervals where token consumption is ≥ 3σ above the rolling baseline"
        className="lg:col-span-2"
        action={
          <select
            value={groupBy}
            onChange={(e) => setGroupBy(e.target.value)}
            className="rounded-lg border border-gray-200 bg-white px-2 py-1 text-xs font-medium text-gray-700 outline-none dark:border-gray-800 dark:bg-gray-900 dark:text-gray-300"
          >
            {GROUPS.map((g) => (
              <option key={g.value} value={g.value}>
                {g.label}
              </option>
            ))}
          </select>
        }
      >
        {anomalies.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (anomalies.data ?? []).length === 0 ? (
          <PanelMessage>
            <AlertTriangle className="h-4 w-4 opacity-50" />
            No anomalies in this time range
          </PanelMessage>
        ) : (
          <DataTable data={anomalies.data!} columns={anomalyColumns} initialSort={[{ id: 'score', desc: true }]} />
        )}
      </Panel>

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
        <TimeseriesPanel
          points={ttfc.data}
          spanMs={spanMs}
          yFmt={(v) => fmtDuration(v)}
          loading={ttfc.loading}
        />
      </Panel>

      <Panel title="Throughput" sub="Output tokens per second of model time">
        <TimeseriesPanel
          points={throughput.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Tokens/sec', color: '#22d3ee', fill: true } }}
          yFmt={(v) => fmtTokens(v) + '/s'}
          loading={throughput.loading}
        />
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

      <Panel title="HTTP Requests" sub="Inbound and outbound requests per interval" className="lg:col-span-2">
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
                datasets: [
                  { data: httpErrorsByCode.data!.map((d) => d.count), backgroundColor: '#ef4444', borderRadius: 4 },
                ],
              }}
              options={chartOptions()}
            />
          </div>
        )}
      </Panel>

      <Panel title="Top Routes" sub="Most requested routes in range">
        {topRoutes.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (topRoutes.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={topRoutes.data!} columns={routeColumns} initialSort={[{ id: 'total_requests', desc: true }]} />
        )}
      </Panel>

      <Panel title="All Routes" sub="Latency and error rate per route">
        {httpSummary.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (httpSummary.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable
            data={httpSummary.data!}
            columns={httpSummaryColumns}
            initialSort={[{ id: 'total_requests', desc: true }]}
          />
        )}
      </Panel>
    </div>
  )
}
