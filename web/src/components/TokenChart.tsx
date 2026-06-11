import { useApi, useDash } from '../dash'
import type { AnomalyPoint, TimeseriesPoint } from '../types'
import { ChartLegend, Line, chartOptions, groupSeries } from './charts'
import { Panel, PanelMessage } from './Panel'
import { fmtTokens } from '../lib/format'

const TOKEN_SPECS = {
  input: { label: 'Input', color: '#818cf8', fill: true },
  output: { label: 'Output', color: '#34d399', fill: true },
  cache_creation: { label: 'Cache Creation', color: '#fbbf24', fill: true },
  cache_read: { label: 'Cache Read', color: '#22d3ee', fill: true },
}

// TokenChart plots token usage per type and overlays red markers on buckets
// where consumption spikes above the rolling baseline (z-score ≥ 3).
export function TokenChart({ className }: { className?: string }) {
  const { spanMs } = useDash()
  const tokens = useApi<TimeseriesPoint[]>('/api/genai/token-timeseries')
  const anomalies = useApi<AnomalyPoint[]>('/api/genai/anomaly-timeseries')

  const loading = tokens.loading || anomalies.loading
  const points = tokens.data ?? []

  let content
  if (loading) {
    content = <PanelMessage>Loading…</PanelMessage>
  } else if (points.length === 0) {
    content = <PanelMessage>No data</PanelMessage>
  } else {
    const { buckets, labels, datasets } = groupSeries(points, spanMs, TOKEN_SPECS)
    const flagged = (anomalies.data ?? []).filter((a) => a.score >= 3)
    const markerData = buckets.map((b) => {
      const hits = flagged.filter((a) => a.bucket === b)
      return hits.length ? Math.max(...hits.map((a) => a.tokens)) : null
    })
    const allDatasets: any[] = [...datasets]
    if (markerData.some((v) => v !== null)) {
      allDatasets.push({
        label: 'Anomaly',
        data: markerData,
        showLine: false,
        pointRadius: 6,
        pointHoverRadius: 8,
        pointStyle: 'rectRot',
        borderColor: '#ef4444',
        backgroundColor: '#ef4444',
        borderWidth: 2,
      })
    }
    content = (
      <div>
        <ChartLegend items={allDatasets.map((d) => ({ label: d.label, color: d.borderColor }))} />
        <div className="h-72">
          <Line data={{ labels, datasets: allDatasets }} options={chartOptions({ yFmt: fmtTokens })} />
        </div>
      </div>
    )
  }

  return (
    <Panel
      title="Token Usage"
      sub="Tokens per interval; markers flag consumption spikes above the rolling baseline"
      className={className}
    >
      {content}
    </Panel>
  )
}
