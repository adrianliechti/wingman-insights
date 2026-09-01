import { createContext, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { apiGet } from './api'
import type { Params } from './api'

export type RangeKey = 'today' | '24h' | '3d' | '7d' | '30d'

// DashSearch lives in the URL so views (range, filters) are shareable.
export interface DashSearch {
  range?: RangeKey
  from?: string
  to?: string
  app?: string[]
  user?: string[]
  department?: string[]
  location?: string[]
  provider?: string[]
  models?: string[]
}

const RANGE_KEYS: RangeKey[] = ['today', '24h', '3d', '7d', '30d']

export function validateSearch(search: Record<string, unknown>): DashSearch {
  const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined)
  // parseSearch (router.tsx) always hands us a comma-joined string; tolerate an
  // array too in case validateSearch is ever called with pre-parsed input.
  const strList = (v: unknown) => {
    const raw = Array.isArray(v) ? v : typeof v === 'string' ? v.split(',') : []
    const result = [...new Set(raw.map((s) => String(s).trim()).filter(Boolean))]
    return result.length > 0 ? result : undefined
  }
  const range = str(search.range)
  return {
    range: RANGE_KEYS.includes(range as RangeKey) ? (range as RangeKey) : undefined,
    from: str(search.from),
    to: str(search.to),
    app: strList(search.app),
    user: strList(search.user),
    department: strList(search.department),
    location: strList(search.location),
    provider: strList(search.provider),
    models: strList(search.models),
  }
}

const RANGE_MS: Record<string, number> = {
  '24h': 24 * 3600e3,
  '3d': 3 * 24 * 3600e3,
  '7d': 7 * 24 * 3600e3,
  '30d': 30 * 24 * 3600e3,
}

// parseDate returns a valid Date for an ISO string, or null.
function parseDate(s?: string): Date | null {
  if (!s) return null
  const d = new Date(s)
  return isNaN(d.getTime()) ? null : d
}

export function resolveRange(search: DashSearch) {
  const now = new Date()
  const range = search.range ?? '24h'
  let to = now
  let from: Date
  const customFrom = parseDate(search.from)
  const customTo = parseDate(search.to)
  if (customFrom) {
    // Explicit from/to in the URL (shareable custom window) win over the preset;
    // the Header clears them whenever a preset is picked.
    from = customFrom
    to = customTo ?? now
    if (from > to) [from, to] = [to, from]
  } else if (range === 'today') {
    from = new Date(now)
    from.setHours(0, 0, 0, 0)
  } else {
    from = new Date(now.getTime() - (RANGE_MS[range] ?? RANGE_MS['24h']))
  }
  const spanMs = Math.max(to.getTime() - from.getTime(), 60e3)
  const interval =
    spanMs <= 6 * 3600e3 ? '15 minute'
    : spanMs <= 48 * 3600e3 ? '1 hour'
    : spanMs <= 14 * 24 * 3600e3 ? '6 hour'
    : '1 day'
  return { from: from.toISOString(), to: to.toISOString(), interval, spanMs }
}

export interface DashState {
  from: string
  to: string
  interval: string
  spanMs: number
  search: DashSearch
  refreshKey: number
  refresh: () => void
  autoRefresh: boolean
  setAutoRefresh: (v: boolean) => void
}

const DashContext = createContext<DashState | null>(null)

export function DashProvider({ search, children }: { search: DashSearch; children: ReactNode }) {
  const [refreshKey, setRefreshKey] = useState(0)
  const [autoRefresh, setAutoRefresh] = useState(false)

  useEffect(() => {
    if (!autoRefresh) return
    const id = setInterval(() => setRefreshKey((k) => k + 1), 15000)
    return () => clearInterval(id)
  }, [autoRefresh])

  const range = useMemo(
    () => resolveRange(search),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [search.range, search.from, search.to, refreshKey],
  )

  const value: DashState = {
    ...range,
    search,
    refreshKey,
    refresh: () => setRefreshKey((k) => k + 1),
    autoRefresh,
    setAutoRefresh,
  }
  return <DashContext.Provider value={value}>{children}</DashContext.Provider>
}

export function useDash(): DashState {
  const ctx = useContext(DashContext)
  if (!ctx) throw new Error('useDash outside DashProvider')
  return ctx
}

// useFilterNav returns a setter that patches the global filters in the URL
// search — used by the filter bar and by click-to-filter table rows, so
// clicking an entity anywhere narrows every page to it.
export function useFilterNav() {
  const navigate = useNavigate()
  return (patch: Partial<DashSearch>) =>
    navigate({ to: '.', search: (prev: DashSearch) => ({ ...prev, ...patch }) })
}

// usePrevRange returns the equal-length window immediately before the active
// range — pass it to useApi as `extra` for period-over-period comparison.
export function usePrevRange(): { from: string; to: string } {
  const { from, to } = useDash()
  const f = new Date(from).getTime()
  const span = new Date(to).getTime() - f
  return { from: new Date(f - span).toISOString(), to: from }
}

// useApi fetches an endpoint with the global time range and filters applied;
// `extra` adds or overrides query params per call.
export function useApi<T>(path: string, extra?: Params) {
  const { from, to, interval, search, refreshKey } = useDash()
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const params: Params = {
    from,
    to,
    interval,
    app: search.app,
    user: search.user,
    department: search.department,
    location: search.location,
    provider: search.provider,
    models: search.models,
    ...extra,
  }
  const key = path + JSON.stringify(params) + refreshKey

  useEffect(() => {
    // Abort the in-flight request when the key changes (filter/range change) or
    // the component unmounts, so superseded fetches don't run to completion or
    // land their results out of order.
    const ctrl = new AbortController()
    setLoading(true)
    apiGet<T>(path, params, ctrl.signal)
      .then((d) => {
        if (!ctrl.signal.aborted) {
          setData(d)
          setError(null)
        }
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setError(String(e))
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    return () => ctrl.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  return { data, loading, error, params }
}
