import { createContext, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { apiGet } from './api'
import type { Params } from './api'

export type RangeKey = 'today' | '24h' | '3d' | '7d' | '30d' | 'custom'

// DashSearch lives in the URL so views (range, filters) are shareable.
export interface DashSearch {
  range?: RangeKey
  from?: string
  to?: string
  service?: string
  user?: string
  provider?: string
  model?: string
}

const RANGE_KEYS: RangeKey[] = ['today', '24h', '3d', '7d', '30d', 'custom']

export function validateSearch(search: Record<string, unknown>): DashSearch {
  const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined)
  const range = str(search.range)
  return {
    range: RANGE_KEYS.includes(range as RangeKey) ? (range as RangeKey) : undefined,
    from: str(search.from),
    to: str(search.to),
    service: str(search.service),
    user: str(search.user),
    provider: str(search.provider),
    model: str(search.model),
  }
}

const RANGE_MS: Record<string, number> = {
  '24h': 24 * 3600e3,
  '3d': 3 * 24 * 3600e3,
  '7d': 7 * 24 * 3600e3,
  '30d': 30 * 24 * 3600e3,
}

export function resolveRange(search: DashSearch) {
  const now = new Date()
  const range = search.range ?? '24h'
  let from: Date
  let to = now
  if (range === 'custom' && search.from) {
    from = new Date(search.from)
    to = search.to ? new Date(search.to) : now
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
    service: search.service,
    user: search.user,
    provider: search.provider,
    model: search.model,
    ...extra,
  }
  const key = path + JSON.stringify(params) + refreshKey

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    apiGet<T>(path, params)
      .then((d) => {
        if (!cancelled) {
          setData(d)
          setError(null)
        }
      })
      .catch((e) => {
        if (!cancelled) setError(String(e))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  return { data, loading, error, params }
}
