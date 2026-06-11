import { CircleCheck } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi, useDash } from '../dash'
import type {
  HTTPErrorsByCodeRow,
  HTTPSummaryRow,
  MethodDistributionRow,
  TimeseriesPoint,
  TopRouteRow,
} from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { DataTable } from '../components/DataTable'
import { Bar, TimeseriesPanel, chartOptions } from '../components/charts'
import { fmtDuration, fmtTokens } from '../lib/format'

const DIRECTION_SPECS = {
  server: { label: 'Server (inbound)', color: '#818cf8', fill: true },
  client: { label: 'Client (outbound)', color: '#34d399', fill: true },
}

const summaryColumns: ColumnDef<HTTPSummaryRow, any>[] = [
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

function BarChart({ labels, values, color }: { labels: string[]; values: number[]; color: string }) {
  return (
    <div className="h-56">
      <Bar
        data={{
          labels,
          datasets: [{ data: values, backgroundColor: color, borderRadius: 4 }],
        }}
        options={chartOptions()}
      />
    </div>
  )
}

export function Http() {
  const { spanMs } = useDash()
  const requests = useApi<TimeseriesPoint[]>('/api/http/requests-timeseries')
  const latency = useApi<TimeseriesPoint[]>('/api/http/timeseries')
  const errorRate = useApi<TimeseriesPoint[]>('/api/http/error-rate')
  const errorsByCode = useApi<HTTPErrorsByCodeRow[]>('/api/http/errors-by-code')
  const topRoutes = useApi<TopRouteRow[]>('/api/http/top-routes')
  const methods = useApi<MethodDistributionRow[]>('/api/http/method-distribution')
  const summary = useApi<HTTPSummaryRow[]>('/api/http/summary')

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
      <Panel title="Requests" sub="Inbound and outbound requests per interval" className="lg:col-span-2">
        <TimeseriesPanel
          points={requests.data}
          spanMs={spanMs}
          specs={DIRECTION_SPECS}
          yFmt={fmtTokens}
          loading={requests.loading}
        />
      </Panel>

      <Panel title="Latency" sub="Average request duration">
        <TimeseriesPanel
          points={latency.data}
          spanMs={spanMs}
          specs={DIRECTION_SPECS}
          yFmt={(v) => fmtDuration(v)}
          loading={latency.loading}
        />
      </Panel>

      <Panel title="Error Rate" sub="Share of responses with status ≥ 400">
        <TimeseriesPanel
          points={errorRate.data}
          spanMs={spanMs}
          specs={{ '': { label: 'Errors', color: '#ef4444', fill: true } }}
          yFmt={(v) => v.toFixed(1) + '%'}
          loading={errorRate.loading}
        />
      </Panel>

      <Panel title="Errors by Status Code">
        {errorsByCode.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (errorsByCode.data ?? []).length === 0 ? (
          <PanelMessage>
            <CircleCheck className="h-4 w-4 text-emerald-500" />
            No errors
          </PanelMessage>
        ) : (
          <BarChart
            labels={errorsByCode.data!.map((d) => String(d.status_code))}
            values={errorsByCode.data!.map((d) => d.count)}
            color="#ef4444"
          />
        )}
      </Panel>

      <Panel title="Methods">
        {methods.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (methods.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <BarChart
            labels={methods.data!.map((d) => d.method)}
            values={methods.data!.map((d) => d.total_requests)}
            color="#818cf8"
          />
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

      <Panel title="All Routes" sub="Latency and error rate per route" className="lg:col-span-2">
        {summary.loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (summary.data ?? []).length === 0 ? (
          <PanelMessage>No data</PanelMessage>
        ) : (
          <DataTable data={summary.data!} columns={summaryColumns} initialSort={[{ id: 'total_requests', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
