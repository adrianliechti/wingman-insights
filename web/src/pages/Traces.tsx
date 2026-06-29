import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
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

// A muted dot colored by operation gives the tree visual structure at a glance.
const OP_COLOR: Record<string, string> = {
  chat: '#818cf8',
  generate_content: '#818cf8',
  text_completion: '#818cf8',
  invoke_agent: '#34d399',
  execute_tool: '#fbbf24',
  embeddings: '#f472b6',
  retrieval: '#22d3ee',
}
const opColor = (op?: string) => OP_COLOR[op ?? ''] ?? '#9ca3af'

type BadgeVariant = 'strong' | 'soft' | 'error'

function MetaBadge({ label, value, variant = 'soft' }: { label?: string; value: string; variant?: BadgeVariant }) {
  const cls = {
    strong: 'bg-gray-900 text-gray-100 dark:bg-gray-100 dark:text-gray-900',
    soft: 'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300',
    error: 'bg-red-500/15 text-red-600 dark:text-red-400',
  }[variant]
  return (
    <span className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs tabular-nums ${cls}`}>
      {label && <span className="opacity-60">{label}</span>}
      <span className="font-medium">{value}</span>
    </span>
  )
}

// Opt-in payloads that, when present, render as their own block. Input/output
// messages are pulled out as the headline I/O; the rest stay collapsible.
const INPUT_ATTRS = ['gen_ai.input.messages', 'gen_ai.system_instructions']
const OUTPUT_ATTRS = ['gen_ai.output.messages']
const PAYLOAD_ATTRS = new Set([
  ...INPUT_ATTRS,
  ...OUTPUT_ATTRS,
  'gen_ai.tool.definitions',
  'gen_ai.tool.call.arguments',
  'gen_ai.tool.call.result',
])

function IOBlock({ title, body, tone }: { title: string; body: string; tone: 'input' | 'output' }) {
  return (
    <div>
      <p className="mb-1.5 text-xs font-semibold uppercase tracking-wider text-gray-500">{title}</p>
      <pre
        className={`max-h-80 overflow-auto rounded-lg p-3 text-xs whitespace-pre-wrap text-gray-700 dark:text-gray-200 ${
          tone === 'output'
            ? 'bg-emerald-50 dark:bg-emerald-500/10'
            : 'bg-gray-50 dark:bg-gray-800/50'
        }`}
      >
        {body}
      </pre>
    </div>
  )
}

function SpanDetail({ span }: { span: SpanRow }) {
  const attrs = Object.entries(span.attributes ?? {})
  const get = (key: string) => attrs.find(([k]) => k === key)?.[1]
  const input = INPUT_ATTRS.map(get).find(Boolean)
  const output = OUTPUT_ATTRS.map(get).find(Boolean)
  const meta = attrs.filter(([k]) => !PAYLOAD_ATTRS.has(k)).sort(([a], [b]) => a.localeCompare(b))
  const extraPayloads = attrs.filter(([k]) => PAYLOAD_ATTRS.has(k) && !INPUT_ATTRS.includes(k) && !OUTPUT_ATTRS.includes(k))
  const totalTokens = span.input_tokens + span.output_tokens

  return (
    <div className="space-y-5">
      <div>
        <div className="flex items-center gap-2">
          <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: opColor(span.operation_name) }} />
          <h3 className="font-mono text-sm font-semibold text-gray-900 dark:text-white">{span.name}</h3>
        </div>
        <p className="mt-1 text-xs text-gray-500">{fmtTime(span.time)}</p>

        <div className="mt-3 flex flex-wrap gap-1.5">
          {span.session_id && <MetaBadge label="Session" value={span.session_id} variant="strong" />}
          {(span.user_name || span.user_id) && <MetaBadge label="User" value={span.user_name || span.user_id!} variant="strong" />}
          {span.app_id && <MetaBadge label="App" value={span.app_id} />}
          {span.operation_name && <MetaBadge value={span.operation_name} />}
          <MetaBadge label="Latency" value={fmtDuration(span.duration)} />
          {span.cost > 0 && <MetaBadge label="Cost" value={fmtCost(span.cost)} />}
          {totalTokens > 0 && (
            <MetaBadge value={`${fmtTokens(span.input_tokens)} → ${fmtTokens(span.output_tokens)} (Σ ${fmtTokens(totalTokens)})`} />
          )}
          {(span.cache_read_tokens > 0 || span.cache_creation_tokens > 0) && (
            <MetaBadge label="cache" value={`${fmtTokens(span.cache_read_tokens)}r · ${fmtTokens(span.cache_creation_tokens)}w`} />
          )}
          {span.reasoning_tokens > 0 && <MetaBadge label="reasoning" value={fmtTokens(span.reasoning_tokens)} />}
          {(span.response_model || span.request_model) && <MetaBadge value={span.response_model || span.request_model!} />}
          {span.finish_reasons && <MetaBadge label="finish" value={span.finish_reasons} />}
          {(span.status === 'error' || span.error_type) && <MetaBadge value={span.error_type || 'error'} variant="error" />}
        </div>
      </div>

      {input && <IOBlock title="Input" body={input} tone="input" />}
      {output && <IOBlock title="Output" body={output} tone="output" />}

      {extraPayloads.map(([key, value]) => (
        <details key={key} open={false}>
          <summary className="cursor-pointer text-xs font-semibold uppercase tracking-wider text-gray-500 hover:text-gray-900 dark:hover:text-white">
            {key.replace('gen_ai.', '')}
          </summary>
          <pre className="mt-1.5 max-h-64 overflow-auto rounded-lg bg-gray-50 p-3 text-xs whitespace-pre-wrap text-gray-700 dark:bg-gray-800/50 dark:text-gray-300">
            {value}
          </pre>
        </details>
      ))}

      {meta.length > 0 && (
        <div>
          <p className="mb-2 text-xs font-semibold uppercase tracking-wider text-gray-500">Attributes</p>
          <dl className="grid grid-cols-1 gap-x-8 gap-y-px sm:grid-cols-2">
            {meta.map(([key, value]) => (
              <div key={key} className="flex gap-3 border-b border-gray-100 py-1.5 text-xs dark:border-gray-800/60">
                <dt className="w-40 shrink-0 truncate font-mono text-gray-500" title={key}>
                  {key}
                </dt>
                <dd className="min-w-0 break-all font-mono text-gray-800 dark:text-gray-200">{value}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </div>
  )
}

// TraceView renders the span tree (middle column) and the selected span's detail
// (right column) as two grid cells, sharing the span selection.
function TraceView({ trace }: { trace: TraceSummary }) {
  const [spans, setSpans] = useState<SpanRow[] | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  useEffect(() => {
    setSpans(null)
    setSelectedId(null)
    apiGet<SpanRow[]>(`/api/traces/${trace.trace_id}`, {}).then(setSpans).catch(() => setSpans([]))
  }, [trace.trace_id])

  if (!spans) {
    return (
      <>
        <div className="border-gray-200 lg:border-r dark:border-gray-800">
          <PanelMessage>Loading…</PanelMessage>
        </div>
        <div />
      </>
    )
  }
  if (spans.length === 0) {
    return (
      <>
        <div className="border-gray-200 lg:border-r dark:border-gray-800">
          <PanelMessage>Trace not found</PanelMessage>
        </div>
        <div />
      </>
    )
  }

  const tree = orderTrace(spans)
  const start = Math.min(...spans.map((s) => new Date(s.time).getTime()))
  const end = Math.max(...spans.map((s) => new Date(s.time).getTime() + s.duration * 1000))
  const total = Math.max(end - start, 1)
  const totalCost = spans.reduce((acc, s) => acc + s.cost, 0)
  // Default to the most informative span (one with tokens/cost) rather than a
  // sparse root wrapper, so the detail panel lands on something substantive.
  const fallback = tree.find((s) => s.input_tokens > 0 || s.cost > 0) ?? tree[0]
  const selected = tree.find((s) => s.span_id === selectedId) ?? fallback

  return (
    <>
      <div className="flex min-w-0 flex-col border-gray-200 lg:border-r dark:border-gray-800">
        <div className="border-b border-gray-200 px-3 py-2.5 dark:border-gray-800">
          <p className="truncate font-mono text-xs font-semibold text-gray-900 dark:text-white" title={trace.name}>
            {trace.name}
          </p>
          <p className="mt-0.5 text-[11px] tabular-nums text-gray-500">
            {spans.length} spans · {fmtDuration(total / 1000)}
            {totalCost > 0 && <> · {fmtCost(totalCost)}</>}
          </p>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-1.5">
          {tree.map((s) => {
            const isError = s.status === 'error' || !!s.error_type
            const isSelected = s.span_id === selected?.span_id
            const tokens = s.input_tokens + s.output_tokens
            return (
              <button
                key={s.span_id}
                onClick={() => setSelectedId(s.span_id)}
                style={{ paddingLeft: `${8 + s.depth * 14}px` }}
                className={`flex w-full flex-col rounded-md py-1 pr-2 text-left transition-colors ${
                  isSelected ? 'bg-indigo-50 dark:bg-indigo-500/10' : 'hover:bg-gray-50 dark:hover:bg-gray-800/40'
                }`}
              >
                <span className="flex min-w-0 items-center gap-1.5">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ backgroundColor: isError ? '#ef4444' : opColor(s.operation_name) }} />
                  <span className="truncate font-mono text-xs text-gray-800 dark:text-gray-200" title={s.name}>
                    {s.name}
                  </span>
                </span>
                <span className="mt-0.5 pl-3 text-[11px] tabular-nums text-gray-400 dark:text-gray-500">
                  {fmtDuration(s.duration)}
                  {tokens > 0 && <> · {fmtTokens(tokens)} tok</>}
                  {s.cost > 0 && <> · {fmtCost(s.cost)}</>}
                </span>
              </button>
            )
          })}
        </div>
      </div>

      <div className="min-w-0 overflow-y-auto p-5">
        {selected ? <SpanDetail span={selected} /> : <PanelMessage>Select a span</PanelMessage>}
      </div>
    </>
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

  // Fill the viewport down to the bottom: measure the container's top rather than
  // guess a fixed offset (header/filter heights vary). The negative margin
  // cancels the page's bottom padding so the fill doesn't introduce a scrollbar.
  const containerRef = useRef<HTMLDivElement>(null)
  const [fillH, setFillH] = useState<number>()
  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    const update = () => {
      if (window.innerWidth < 1024) return setFillH(undefined)
      setFillH(Math.max(window.innerHeight - el.getBoundingClientRect().top - 12, 360))
    }
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  return (
    <div
      ref={containerRef}
      style={fillH ? { height: fillH, marginBottom: '-1.5rem' } : undefined}
      className="grid grid-cols-1 overflow-hidden rounded-lg border border-gray-200 lg:grid-cols-[15rem_21rem_1fr] dark:border-gray-800"
    >
      <aside className="flex min-h-0 flex-col border-gray-200 lg:border-r dark:border-gray-800">
        <div className="flex items-center justify-between border-b border-gray-200 px-3 py-2.5 dark:border-gray-800">
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
        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {loading ? (
            <PanelMessage>Loading…</PanelMessage>
          ) : traces.length === 0 ? (
            <PanelMessage>No traces</PanelMessage>
          ) : (
            groups.map(([session, items]) => (
              <div key={session || 'no-session'} className="mb-3">
                <p className="truncate px-1.5 pb-1 font-mono text-[11px] text-gray-400 dark:text-gray-600" title={session}>
                  {session || 'no session'}
                  {(items[0].user_name || items[0].user_id) && (
                    <span className="ml-1.5">· {items[0].user_name || items[0].user_id}</span>
                  )}
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

      {loading ? (
        <div className="lg:col-span-2">
          <PanelMessage>Loading…</PanelMessage>
        </div>
      ) : !selected ? (
        <div className="lg:col-span-2">
          <PanelMessage>No traces — senders must export OTLP traces to /v1/traces</PanelMessage>
        </div>
      ) : (
        <TraceView trace={selected} />
      )}
    </div>
  )
}
