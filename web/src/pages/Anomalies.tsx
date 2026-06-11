import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useApi } from '../dash'
import type { AnomalyPoint } from '../types'
import { Panel, PanelMessage } from '../components/Panel'
import { DataTable } from '../components/DataTable'
import { TokenChart } from '../components/TokenChart'
import { fmtTime, fmtTokens } from '../lib/format'

const GROUPS = [
  { value: 'user', label: 'By user' },
  { value: 'service', label: 'By app' },
  { value: 'model', label: 'By model' },
]

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

export function Anomalies() {
  const [groupBy, setGroupBy] = useState('user')
  const { data, loading } = useApi<AnomalyPoint[]>('/api/genai/anomalies', { group_by: groupBy })

  const columns: ColumnDef<AnomalyPoint, any>[] = [
    { accessorKey: 'bucket', header: 'When', cell: (c) => fmtTime(c.getValue()) },
    {
      accessorKey: 'group_key',
      header: groupBy === 'user' ? 'User' : groupBy === 'service' ? 'App' : 'Model',
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
    <div className="grid grid-cols-1 gap-5">
      <TokenChart />

      <Panel
        title="Detected Anomalies"
        sub="Intervals where token consumption is ≥ 3σ above the rolling baseline"
        action={
          <div className="flex items-center gap-2">
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
          </div>
        }
      >
        {loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : (data ?? []).length === 0 ? (
          <PanelMessage>
            <AlertTriangle className="mx-auto mb-2 h-5 w-5 opacity-50" />
            No anomalies in this time range
          </PanelMessage>
        ) : (
          <DataTable data={data!} columns={columns} initialSort={[{ id: 'score', desc: true }]} />
        )}
      </Panel>
    </div>
  )
}
