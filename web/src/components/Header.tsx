import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from '@tanstack/react-router'
import { Boxes, CalendarDays, ChevronDown, ChevronLeft, ChevronRight, Menu, Monitor, Moon, Receipt, RefreshCw, Sun, User, X } from 'lucide-react'
import { useDash, useApi, useFilterNav, useMe } from '../dash'
import type { DashSearch, RangeKey } from '../dash'
import type { UsageByAppRow } from '../types'
import { MultiFilterSelect } from './FilterBar'

const PRESETS: { label: string; value: RangeKey }[] = [
  { label: '24h', value: '24h' },
  { label: '3d', value: '3d' },
  { label: '7d', value: '7d' },
  { label: '30d', value: '30d' },
]

// The personal dashboard uses its own preset set, tuned for individual usage.
const PERSONAL_PRESETS: { label: string; value: RangeKey }[] = [
  { label: '24h', value: '24h' },
  { label: '7d', value: '7d' },
  { label: '14d', value: '14d' },
  { label: '30d', value: '30d' },
]

// fmtDay renders one bound of a custom window, adding the year only when it
// isn't the current one.
function fmtDay(d: Date): string {
  const opts: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric' }
  if (d.getFullYear() !== new Date().getFullYear()) opts.year = 'numeric'
  return d.toLocaleDateString('en', opts)
}

// monthGrid returns the 6×7 Monday-first day matrix covering `view`'s month,
// padded with the adjacent months' days.
function monthGrid(view: Date): Date[] {
  const first = new Date(view.getFullYear(), view.getMonth(), 1)
  const offset = (first.getDay() + 6) % 7
  return Array.from({ length: 42 }, (_, i) => new Date(view.getFullYear(), view.getMonth(), 1 - offset + i))
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

const SEG_ON = 'cursor-pointer rounded-md bg-indigo-600 px-2.5 py-1.5 text-xs font-medium text-white shadow-sm sm:px-3'
const SEG_OFF =
  'cursor-pointer rounded-md px-2.5 py-1.5 text-xs font-medium text-gray-500 transition-all hover:text-gray-900 dark:text-gray-400 dark:hover:text-white sm:px-3'

// Theme mode is the user's explicit choice; 'system' follows the OS preference
// and is the default when nothing is stored. The persisted value drives the
// pre-paint script in index.html.
type ThemeMode = 'light' | 'dark' | 'system'

function readThemeMode(): ThemeMode {
  const v = localStorage.getItem('theme')
  return v === 'light' || v === 'dark' ? v : 'system'
}

// applyThemeMode flips the `dark` class on <html> — the source of truth charts
// observe — resolving 'system' against the OS preference.
function applyThemeMode(mode: ThemeMode) {
  const dark = mode === 'dark' || (mode === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
}

const NAV = [
  { to: '/', label: 'Personal' },
  { to: '/overview', label: 'Overview' },
  { to: '/anomalies', label: 'Anomalies' },
  { to: '/customers', label: 'Customers' },
  { to: '/finops', label: 'FinOps' },
  { to: '/operations', label: 'Operations' },
  { to: '/traces', label: 'Traces' },
]

// personalOnly renders the header for the personal-usage view: the page nav is
// hidden (a non-admin has nowhere else to go), but the time-range controls stay
// so users can rescope their own numbers. personalRange selects the personal
// preset set without hiding the nav, so an admin viewing /personal gets the
// same range options while keeping their navigation.
export function Header({
  personalOnly = false,
  personalRange = false,
}: {
  personalOnly?: boolean
  personalRange?: boolean
}) {
  const dash = useDash()
  const navigate = useNavigate()
  const setFilter = useFilterNav()
  const { me } = useMe()
  const isPersonalView = personalOnly || personalRange
  const { pathname } = useLocation()
  const currentPage = NAV.find((item) => (item.to === '/' ? pathname === '/' : pathname.startsWith(item.to)))
  // The app filter only makes sense once the caller has used more than one
  // application; fetched with app unset so the option list doesn't collapse
  // to 1 the moment a filter is applied. Skipped entirely outside the
  // personal view, which is the only place /api/personal/usage-by-app applies.
  const appRows = useApi<UsageByAppRow[]>(isPersonalView ? '/api/personal/usage-by-app' : null, { app: undefined })
  const appOptions = (appRows.data ?? []).map((r) => ({ value: r.app, label: r.app || 'unattributed' }))
  // An explicit from/to in the URL (custom window) overrides any preset, so no
  // preset may render as selected while one is active.
  const customActive = !!dash.search.from
  const range = customActive ? undefined : (dash.search.range ?? '7d')
  const [themeMode, setThemeMode] = useState<ThemeMode>(readThemeMode)
  const [pickerOpen, setPickerOpen] = useState(false)
  const pickerRef = useRef<HTMLDivElement>(null)
  const [navOpen, setNavOpen] = useState(false)
  const navRef = useRef<HTMLDivElement>(null)
  const [profileOpen, setProfileOpen] = useState(false)
  const profileRef = useRef<HTMLDivElement>(null)
  const [viewMonth, setViewMonth] = useState(() => {
    const now = new Date()
    return new Date(now.getFullYear(), now.getMonth(), 1)
  })
  const [selStart, setSelStart] = useState<Date | null>(null)
  const [selEnd, setSelEnd] = useState<Date | null>(null)
  // dash.to is exclusive-ish (custom windows end at the next local midnight),
  // so display the day just before it.
  const customLabel = customActive
    ? `${fmtDay(new Date(dash.from))} – ${fmtDay(new Date(new Date(dash.to).getTime() - 1))}`
    : ''

  function togglePicker() {
    if (!pickerOpen) {
      // Seed the calendar with the currently active window — custom or preset —
      // so what's shown always mirrors what the charts display.
      const s = new Date(dash.from)
      const e = new Date(new Date(dash.to).getTime() - 1)
      setSelStart(new Date(s.getFullYear(), s.getMonth(), s.getDate()))
      setSelEnd(new Date(e.getFullYear(), e.getMonth(), e.getDate()))
      setViewMonth(new Date(e.getFullYear(), e.getMonth(), 1))
    }
    setPickerOpen((o) => !o)
  }

  // First click sets the start, second click completes the range and applies
  // it (inclusive end: the window runs through the picked day's local
  // midnight). A third click starts a fresh selection.
  function pickDay(d: Date) {
    if (!selStart || selEnd) {
      setSelStart(d)
      setSelEnd(null)
      return
    }
    let s = selStart
    let e = d
    if (e < s) [s, e] = [e, s]
    setSelStart(s)
    setSelEnd(e)
    setSearch({
      from: s.toISOString(),
      to: new Date(e.getFullYear(), e.getMonth(), e.getDate() + 1).toISOString(),
      range: undefined,
    })
    setPickerOpen(false)
  }

  useEffect(() => {
    if (!pickerOpen) return
    function onDown(e: MouseEvent) {
      if (pickerRef.current && !pickerRef.current.contains(e.target as Node)) setPickerOpen(false)
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setPickerOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [pickerOpen])

  useEffect(() => {
    if (!navOpen) return
    function onDown(e: MouseEvent) {
      if (navRef.current && !navRef.current.contains(e.target as Node)) setNavOpen(false)
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setNavOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [navOpen])

  useEffect(() => {
    if (!profileOpen) return
    function onDown(e: MouseEvent) {
      if (profileRef.current && !profileRef.current.contains(e.target as Node)) setProfileOpen(false)
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setProfileOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [profileOpen])

  useEffect(() => {
    if (themeMode !== 'system') return
    const mq = matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => applyThemeMode('system')
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [themeMode])

  function setSearch(patch: Partial<DashSearch>) {
    navigate({
      to: '.',
      search: (prev: DashSearch) => ({ ...prev, ...patch }),
    })
  }

  function chooseTheme(mode: ThemeMode) {
    setThemeMode(mode)
    if (mode === 'system') localStorage.removeItem('theme')
    else localStorage.setItem('theme', mode)
    applyThemeMode(mode)
  }

  const btn =
    'inline-flex h-9 cursor-pointer items-center rounded-lg border px-2 text-xs font-medium transition-colors bg-white border-gray-200 text-gray-500 hover:text-gray-900 hover:border-gray-300 dark:bg-gray-900 dark:border-gray-800 dark:text-gray-400 dark:hover:text-white dark:hover:border-gray-700 sm:px-3'

  const navLinks = (opts?: { stacked?: boolean; onNavigate?: () => void }) =>
    NAV.map((item) => (
      <div key={item.to} className={opts?.stacked ? 'flex flex-col' : 'flex items-center'}>
        <Link
          to={item.to}
          search={(prev: DashSearch) => prev}
          onClick={opts?.onNavigate}
          className={
            opts?.stacked
              ? 'rounded-md px-3 py-1.5 text-xs font-medium text-gray-500 transition-all hover:text-gray-900 dark:text-gray-400 dark:hover:text-white'
              : 'whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium text-gray-500 transition-all hover:text-gray-900 dark:text-gray-400 dark:hover:text-white'
          }
          activeProps={{
            className: opts?.stacked
              ? 'rounded-md px-3 py-1.5 text-xs font-medium bg-indigo-600 !text-white shadow-sm hover:!text-white'
              : 'whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium bg-indigo-600 !text-white shadow-sm hover:!text-white',
          }}
          activeOptions={{ exact: item.to === '/' }}
        >
          {item.label}
        </Link>
        {/* Set personal usage apart from the org-wide dashboard pages. */}
        {item.to === '/' &&
          (opts?.stacked ? (
            <span className="my-1 h-px w-full bg-gray-200 dark:bg-gray-700" aria-hidden="true" />
          ) : (
            <span className="mx-1 h-4 w-px bg-gray-200 dark:bg-gray-700" aria-hidden="true" />
          ))}
      </div>
    ))

  return (
    <header className="pb-5">
      <div className="flex flex-row flex-nowrap items-center justify-between gap-2 sm:gap-4">
        <div className="flex min-w-0 items-center gap-3 lg:gap-6">
          {!personalOnly && (
            <div className="lg:hidden" ref={navRef}>
              <button onClick={() => setNavOpen((o) => !o)} className={btn} title="Menu">
                <Menu className="h-3.5 w-3.5" />
              </button>
              <div
                className={`fixed inset-0 z-40 bg-black/40 transition-opacity ${
                  navOpen ? 'opacity-100' : 'pointer-events-none opacity-0'
                }`}
                onClick={() => setNavOpen(false)}
                aria-hidden="true"
              />
              <div
                className={`fixed inset-y-0 left-0 z-50 flex w-64 flex-col gap-0.5 bg-white p-3 shadow-xl transition-transform dark:bg-gray-900 ${
                  navOpen ? 'translate-x-0' : '-translate-x-full'
                }`}
              >
                <div className="flex items-center justify-between px-1 pb-3">
                  <div className="flex items-center gap-2">
                    <Receipt className="h-6 w-6 text-indigo-600" />
                    <span className="text-sm font-semibold text-gray-900 dark:text-white">AI Insights</span>
                  </div>
                  <button
                    onClick={() => setNavOpen(false)}
                    className="rounded-md p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-gray-800 dark:hover:text-gray-200"
                    title="Close menu"
                  >
                    <X className="h-4 w-4" />
                  </button>
                </div>
                {navLinks({ stacked: true, onNavigate: () => setNavOpen(false) })}
              </div>
            </div>
          )}
          <div className="flex min-w-0 shrink items-center gap-2.5">
            <Receipt className="h-7 w-7 shrink-0 text-indigo-600" />
            <h1
              className={`hidden shrink-0 whitespace-nowrap text-lg font-semibold tracking-tight text-gray-900 lg:block dark:text-white ${
                personalOnly || !currentPage ? 'sm:block' : ''
              }`}
            >
              AI Insights
            </h1>
            {!personalOnly && currentPage && (
              <>
                <span className="hidden h-5 w-px shrink-0 bg-gray-200 sm:block lg:hidden dark:bg-gray-700" aria-hidden="true" />
                <span className="hidden min-w-0 truncate text-lg font-medium text-gray-400 sm:block lg:hidden dark:text-gray-500">
                  {currentPage.label}
                </span>
              </>
            )}
          </div>
          {!personalOnly && (
            <nav className="hidden h-9 min-w-0 items-center overflow-x-auto rounded-lg border border-gray-200 bg-white p-0.5 lg:flex dark:border-gray-800 dark:bg-gray-900">
              {navLinks()}
            </nav>
          )}
        </div>

        <div className="flex shrink-0 flex-nowrap items-center gap-1 sm:gap-2">
          {isPersonalView && appOptions.length > 1 && (
            <MultiFilterSelect
              icon={Boxes}
              placeholder="All applications"
              noun="apps"
              value={dash.search.app}
              options={appOptions}
              onChange={(v) => setFilter({ app: v })}
              compact
            />
          )}
          <div className="flex h-9 items-center rounded-lg border border-gray-200 bg-white p-0.5 dark:border-gray-800 dark:bg-gray-900">
            {(personalOnly || personalRange ? PERSONAL_PRESETS : PRESETS).map((p) => (
              <button
                key={p.value}
                onClick={() => setSearch({ range: p.value, from: undefined, to: undefined })}
                className={range === p.value ? SEG_ON : SEG_OFF}
              >
                {p.label}
              </button>
            ))}
            {/* Custom range picker is hidden on the personal view, which uses
                fixed presets only. */}
            {!(personalOnly || personalRange) && (
              <div className="relative" ref={pickerRef}>
              <button onClick={togglePicker} className={customActive ? SEG_ON : SEG_OFF} title="Custom range">
                <CalendarDays className="-mt-0.5 inline-block h-3.5 w-3.5" />
                {customActive && <span className="ml-1.5">{customLabel}</span>}
                <ChevronDown className="-mr-0.5 ml-1 inline-block h-3 w-3" />
              </button>
              {pickerOpen && (
                <div className="absolute right-0 top-full z-20 mt-1.5 w-64 rounded-lg border border-gray-200 bg-white p-3 shadow-lg dark:border-gray-800 dark:bg-gray-900">
                  <div className="flex items-center justify-between pb-1">
                    <button
                      onClick={() => setViewMonth((m) => new Date(m.getFullYear(), m.getMonth() - 1, 1))}
                      className="rounded-md p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-gray-800 dark:hover:text-gray-200"
                      title="Previous month"
                    >
                      <ChevronLeft className="h-3.5 w-3.5" />
                    </button>
                    <span className="text-xs font-semibold text-gray-900 dark:text-white">
                      {viewMonth.toLocaleString('en', { month: 'long', year: 'numeric' })}
                    </span>
                    <button
                      onClick={() => setViewMonth((m) => new Date(m.getFullYear(), m.getMonth() + 1, 1))}
                      className="rounded-md p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-gray-800 dark:hover:text-gray-200"
                      title="Next month"
                    >
                      <ChevronRight className="h-3.5 w-3.5" />
                    </button>
                  </div>
                  <div className="grid grid-cols-7 text-center text-[10px] leading-5 text-gray-400 dark:text-gray-500">
                    {['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'].map((d) => (
                      <span key={d}>{d}</span>
                    ))}
                  </div>
                  <div className="grid grid-cols-7">
                    {monthGrid(viewMonth).map((d) => {
                      const outside = d.getMonth() !== viewMonth.getMonth()
                      const today = sameDay(d, new Date())
                      const isStart = !!selStart && sameDay(d, selStart)
                      const isEnd = !!selEnd && sameDay(d, selEnd)
                      const inRange = !!selStart && !!selEnd && d > selStart && d < selEnd
                      let cls = 'h-8 cursor-pointer text-xs transition-colors '
                      if (isStart || isEnd) {
                        const single = isStart && (isEnd ? sameDay(selStart!, selEnd!) : true)
                        cls +=
                          'bg-indigo-600 font-medium text-white ' +
                          (single ? 'rounded-md' : isStart ? 'rounded-l-md' : 'rounded-r-md')
                      } else if (inRange) {
                        cls += 'bg-indigo-50 text-indigo-700 dark:bg-indigo-500/15 dark:text-indigo-300'
                      } else {
                        cls += 'rounded-md hover:bg-gray-100 dark:hover:bg-gray-800 '
                        cls += today
                          ? 'font-semibold text-indigo-600 dark:text-indigo-400'
                          : outside
                            ? 'text-gray-300 dark:text-gray-700'
                            : 'text-gray-700 dark:text-gray-300'
                      }
                      return (
                        <button key={d.getTime()} onClick={() => pickDay(d)} className={cls}>
                          {d.getDate()}
                        </button>
                      )
                    })}
                  </div>
                  <p className="pt-1.5 text-center text-[10px] text-gray-400 dark:text-gray-500">
                    {selStart && !selEnd ? `${fmtDay(selStart)} → pick the end day` : 'Click a start day to select a new range'}
                  </p>
                </div>
              )}
            </div>
            )}
          </div>
          <button onClick={() => dash.refresh()} className={btn} title="Refresh">
            <RefreshCw className="h-3.5 w-3.5" />
          </button>
          <div className="relative" ref={profileRef}>
            <button onClick={() => setProfileOpen((o) => !o)} className={btn} title="Account">
              <User className="h-3.5 w-3.5" />
            </button>
            {profileOpen && (
              <div className="absolute right-0 top-full z-20 mt-1.5 w-56 rounded-lg border border-gray-200 bg-white p-1 shadow-lg dark:border-gray-800 dark:bg-gray-900">
                <div className="px-3 py-2">
                  <div className="truncate text-sm font-medium text-gray-900 dark:text-white">
                    {me?.name || me?.user || 'Unknown user'}
                  </div>
                  {me?.user && me.user !== me.name && (
                    <div className="truncate text-xs text-gray-500 dark:text-gray-400">{me.user.toLowerCase()}</div>
                  )}
                </div>
                <div className="my-1 h-px w-full bg-gray-200 dark:bg-gray-700" aria-hidden="true" />
                <div className="flex items-center justify-between px-3 py-1.5">
                  <span className="text-[11px] font-medium text-gray-400 dark:text-gray-500">Theme</span>
                  <div className="flex items-center rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-gray-800 dark:bg-gray-800/50">
                    {(
                      [
                        { mode: 'light', label: 'Light', icon: Sun },
                        { mode: 'system', label: 'System', icon: Monitor },
                        { mode: 'dark', label: 'Dark', icon: Moon },
                      ] as { mode: ThemeMode; label: string; icon: typeof Sun }[]
                    ).map(({ mode, label, icon: Icon }) => (
                      <button
                        key={mode}
                        onClick={() => chooseTheme(mode)}
                        title={label}
                        className={`flex cursor-pointer items-center justify-center rounded-md p-1.5 transition-colors ${
                          themeMode === mode
                            ? 'bg-indigo-600 text-white shadow-sm'
                            : 'text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-white'
                        }`}
                      >
                        <Icon className="h-3.5 w-3.5" />
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
    </header>
  )
}
