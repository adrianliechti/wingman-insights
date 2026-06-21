import { useEffect, useMemo, useState } from 'react'
import { CircleAlert } from 'lucide-react'
import { useApi } from '../dash'
import { apiGet } from '../api'
import type { SpanRow, TraceSummary } from '../types'
import { PanelMessage } from '../components/Panel'
import { fmtCost, fmtDuration, fmtTime, fmtTokens } from '../lib/format'
import { format } from 'date-fns'

interface TreeSpan extends SpanRow {
  depth: number
}

// orderTrace arranges spans as a depth-first tree using parent_span_id.
function orderTrace(spans: SpanRow[]): TreeSpan[] {
  const byParent = new Map<string, SpanRow[]>()
  const ids = new Set(spans.map((s) => s.span_id))
  for (const s of spans) {
    const parent = s.parent_span_id && ids.has(s.parent_span_id) ? s.parent_span_id : ''
    const list = byParent.get(parent) ?? []
    list.push(s)
    byParent.set(parent, list)
  }
  const result: TreeSpan[] = []
  const walk = (parent: string, depth: number) => {
    for (const s of byParent.get(parent) ?? []) {
      result.push({ ...s, depth })
      walk(s.span_id, depth + 1)
    }
  }
  walk('', 0)
  return result
}

function Chip({ label, value, tone = 'default' }: { label?: string; value: string; tone?: 'default' | 'error' }) {
  return (
    <span
      className={`rounded-md border px-2 py-1 text-xs tabular-nums ${
        tone === 'error'
          ? 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400'
          : 'border-gray-200 bg-gray-50 text-gray-700 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-300'
      }`}
    >
      {label && <span className="mr-1 text-gray-400 dark:text-gray-500">{label}</span>}
      {value}
    </span>
  )
}

// Bulky opt-in payloads get a collapsible block instead of a table row.
const PAYLOAD_ATTRS = new Set([
  'gen_ai.input.messages',
  'gen_ai.output.messages',
  'gen_ai.system_instructions',
  'gen_ai.tool.definitions',
  'gen_ai.tool.call.arguments',
  'gen_ai.tool.call.result',
])

function SpanDetail({ span }: { span: SpanRow }) {
  const attrs = Object.entries(span.attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const meta = attrs.filter(([k]) => !PAYLOAD_ATTRS.has(k))
  const payloads = attrs.filter(([k]) => PAYLOAD_ATTRS.has(k))
  const totalTokens = span.input_tokens + span.output_tokens

  return (
    <div>
      <h3 className="font-mono text-sm font-semibold text-gray-900 dark:text-white">{span.name}</h3>
      <p className="mt-0.5 text-xs text-gray-500">{fmtTime(span.time)}</p>

      <div className="mt-3 flex flex-wrap gap-1.5">
        <Chip label="Latency" value={fmtDuration(span.duration)} />
        {span.cost > 0 && <Chip value={fmtCost(span.cost)} />}
        {totalTokens > 0 && (
          <Chip value={`${fmtTokens(span.input_tokens)} → ${fmtTokens(span.output_tokens)} (Σ ${fmtTokens(totalTokens)})`} />
        )}
        {(span.cache_read_tokens > 0 || span.cache_creation_tokens > 0) && (
          <Chip label="cache" value={`${fmtTokens(span.cache_read_tokens)} read · ${fmtTokens(span.cache_creation_tokens)} write`} />
        )}
        {span.reasoning_tokens > 0 && <Chip label="reasoning" value={fmtTokens(span.reasoning_tokens)} />}
        {span.response_model && <Chip value={span.response_model} />}
        {span.finish_reasons && <Chip label="finish" value={span.finish_reasons} />}
        {(span.status === 'error' || span.error_type) && <Chip tone="error" value={span.error_type || 'error'} />}
      </div>

      {payloads.map(([key, value]) => (
        <details key={key} className="mt-3">
          <summary className="cursor-pointer text-xs font-medium text-gray-500 hover:text-gray-900 dark:hover:text-white">
            {key}
          </summary>
          <pre className="mt-1 max-h-64 overflow-auto rounded-md border border-gray-200 bg-gray-50 p-2 text-xs whitespace-pre-wrap text-gray-700 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-300">
            {value}
          </pre>
        </details>
      ))}

      {meta.length > 0 && (
        <div className="mt-4">
          <p className="mb-1.5 text-xs font-medium uppercase tracking-wider text-gray-500">Attributes</p>
          <div className="divide-y divide-gray-100 rounded-md border border-gray-200 dark:divide-gray-800/60 dark:border-gray-800">
            {meta.map(([key, value]) => (
              <div key={key} className="flex gap-3 px-2.5 py-1.5 text-xs">
                <span className="w-44 shrink-0 truncate font-mono text-gray-500" title={key}>
                  {key}
                </span>
                <span className="break-all font-mono text-gray-800 dark:text-gray-200">{value}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function TraceDetail({ trace }: { trace: TraceSummary }) {
  const [spans, setSpans] = useState<SpanRow[] | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  useEffect(() => {
    setSpans(null)
    setSelectedId(null)
    apiGet<SpanRow[]>(`/api/traces/${trace.trace_id}`, {}).then(setSpans).catch(() => setSpans([]))
  }, [trace.trace_id])

  if (!spans) return <PanelMessage>Loading…</PanelMessage>
  if (spans.length === 0) return <PanelMessage>Trace not found</PanelMessage>

  const tree = orderTrace(spans)
  const start = Math.min(...spans.map((s) => new Date(s.time).getTime()))
  const end = Math.max(...spans.map((s) => new Date(s.time).getTime() + s.duration * 1000))
  const total = Math.max(end - start, 1)
  const totalCost = spans.reduce((acc, s) => acc + s.cost, 0)
  const selected = tree.find((s) => s.span_id === selectedId) ?? tree[0]

  return (
    <div>
      <div className="mb-3">
        <h2 className="font-mono text-sm font-semibold text-gray-900 dark:text-white">{trace.name}</h2>
        <p className="mt-0.5 font-mono text-xs text-gray-500">
          {trace.trace_id} · {spans.length} spans · {fmtDuration(total / 1000)}
          {totalCost > 0 && <> · {fmtCost(totalCost)}</>}
          {trace.session_id && <> · {trace.session_id}</>}
        </p>
      </div>

      <div className="space-y-0.5">
        {tree.map((s) => {
          const offset = ((new Date(s.time).getTime() - start) / total) * 100
          const width = Math.max(((s.duration * 1000) / total) * 100, 0.5)
          const isError = s.status === 'error' || !!s.error_type
          const isSelected = s.span_id === selected?.span_id
          return (
            <button
              key={s.span_id}
              onClick={() => setSelectedId(s.span_id)}
              className={`flex w-full items-center gap-3 rounded-md px-1.5 py-1 text-left text-xs transition-colors ${
                isSelected ? 'bg-indigo-50 dark:bg-indigo-500/10' : 'hover:bg-gray-50 dark:hover:bg-gray-800/40'
              }`}
            >
              <div
                className="w-64 shrink-0 truncate font-mono text-gray-700 dark:text-gray-300"
                style={{ paddingLeft: `${s.depth * 14}px` }}
                title={s.name}
              >
                {s.name}
              </div>
              <div className="relative h-4 flex-1 rounded bg-gray-100 dark:bg-gray-800/50">
                <div
                  className={`absolute top-0.5 h-3 rounded ${isError ? 'bg-red-500/80' : 'bg-indigo-500/80'}`}
                  style={{ left: `${offset}%`, width: `${width}%` }}
                />
              </div>
              <div className="w-16 shrink-0 text-right tabular-nums text-gray-500">{fmtDuration(s.duration)}</div>
              <div className="w-16 shrink-0 text-right tabular-nums text-gray-500">
                {s.cost > 0 ? fmtCost(s.cost) : ''}
              </div>
            </button>
          )
        })}
      </div>

      <div className="mt-4 rounded-lg border border-gray-200 p-4 dark:border-gray-800">
        {selected ? <SpanDetail span={selected} /> : <PanelMessage>Select a span</PanelMessage>}
      </div>
    </div>
  )
}

// Sessions group their traces; traces without a session form a tail group.
function groupBySession(traces: TraceSummary[]) {
  const groups = new Map<string, TraceSummary[]>()
  for (const t of traces) {
    const key = t.session_id || ''
    const list = groups.get(key) ?? []
    list.push(t)
    groups.set(key, list)
  }
  return [...groups.entries()].sort(
    ([, a], [, b]) => new Date(b[0].time).getTime() - new Date(a[0].time).getTime(),
  )
}

export function Traces() {
  const [onlyErrors, setOnlyErrors] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const { data, loading } = useApi<TraceSummary[]>('/api/traces', {
    status: onlyErrors ? 'error' : undefined,
    limit: '50',
  })

  const traces = data ?? []
  const groups = useMemo(() => groupBySession(traces), [traces])
  const selected = traces.find((t) => t.trace_id === selectedId) ?? traces[0]

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-[20rem_1fr]">
      <aside className="rounded-lg border border-gray-200 dark:border-gray-800">
        <div className="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-gray-800">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Sessions</h2>
          <label className="flex cursor-pointer items-center gap-1.5 text-xs font-medium text-gray-500 dark:text-gray-400">
            <input
              type="checkbox"
              checked={onlyErrors}
              onChange={(e) => setOnlyErrors(e.target.checked)}
              className="accent-indigo-600"
            />
            Errors
          </label>
        </div>
        <div className="max-h-[70vh] overflow-y-auto p-2">
          {loading ? (
            <PanelMessage>Loading…</PanelMessage>
          ) : traces.length === 0 ? (
            <PanelMessage>No traces</PanelMessage>
          ) : (
            groups.map(([session, items]) => (
              <div key={session || 'no-session'} className="mb-3">
                <p className="truncate px-1.5 pb-1 font-mono text-[11px] text-gray-400 dark:text-gray-600" title={session}>
                  {session || 'no session'}
                  {items[0].user_email && <span className="ml-1.5">· {items[0].user_email}</span>}
                </p>
                <div className="space-y-0.5">
                  {items.map((t) => {
                    const isSelected = t.trace_id === selected?.trace_id
                    return (
                      <button
                        key={t.trace_id}
                        onClick={() => setSelectedId(t.trace_id)}
                        className={`w-full rounded-md px-2 py-1.5 text-left transition-colors ${
                          isSelected ? 'bg-indigo-50 dark:bg-indigo-500/10' : 'hover:bg-gray-50 dark:hover:bg-gray-800/40'
                        }`}
                      >
                        <div className="flex items-center justify-between gap-2">
                          <span className="truncate font-mono text-xs text-gray-900 dark:text-gray-100">{t.name}</span>
                          {t.has_error && <CircleAlert className="h-3.5 w-3.5 shrink-0 text-red-500" />}
                        </div>
                        <div className="mt-0.5 flex items-center justify-between text-[11px] text-gray-500">
                          <span>{format(new Date(t.time), 'MMM d, HH:mm:ss')}</span>
                          <span className="tabular-nums">
                            {fmtDuration(t.duration)}
                            {t.cost > 0 && <> · {fmtCost(t.cost)}</>}
                          </span>
                        </div>
                      </button>
                    )
                  })}
                </div>
              </div>
            ))
          )}
        </div>
      </aside>

      <main className="min-w-0 rounded-lg border border-gray-200 p-5 dark:border-gray-800">
        {loading ? (
          <PanelMessage>Loading…</PanelMessage>
        ) : !selected ? (
          <PanelMessage>No traces — senders must export OTLP traces to /v1/traces</PanelMessage>
        ) : (
          <TraceDetail trace={selected} />
        )}
      </main>
    </div>
  )
}
