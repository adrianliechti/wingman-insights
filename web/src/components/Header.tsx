import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { CalendarDays, ChevronDown, ChevronLeft, ChevronRight, Moon, RefreshCw, Sun } from 'lucide-react'
import { useDash } from '../dash'
import type { DashSearch, RangeKey } from '../dash'

const PRESETS: { label: string; value: RangeKey }[] = [
  { label: 'Today', value: 'today' },
  { label: '24h', value: '24h' },
  { label: '3d', value: '3d' },
  { label: '7d', value: '7d' },
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

const SEG_ON = 'rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white shadow-sm'
const SEG_OFF =
  'rounded-md px-3 py-1.5 text-xs font-medium text-gray-500 transition-all hover:text-gray-900 dark:text-gray-400 dark:hover:text-white'

const NAV = [
  { to: '/', label: 'Overview' },
  { to: '/anomalies', label: 'Anomalies' },
  { to: '/customers', label: 'Customers' },
  { to: '/finops', label: 'FinOps' },
  { to: '/operations', label: 'Operations' },
  { to: '/traces', label: 'Traces' },
]

export function Header() {
  const dash = useDash()
  const navigate = useNavigate()
  // An explicit from/to in the URL (custom window) overrides any preset, so no
  // preset may render as selected while one is active.
  const customActive = !!dash.search.from
  const range = customActive ? undefined : (dash.search.range ?? '24h')
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const [pickerOpen, setPickerOpen] = useState(false)
  const pickerRef = useRef<HTMLDivElement>(null)
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

  function setSearch(patch: Partial<DashSearch>) {
    navigate({
      to: '.',
      search: (prev: DashSearch) => ({ ...prev, ...patch }),
    })
  }

  function toggleTheme() {
    const next = !dark
    setDark(next)
    document.documentElement.classList.toggle('dark', next)
    localStorage.setItem('theme', next ? 'dark' : 'light')
  }

  const btn =
    'rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors bg-white border-gray-200 text-gray-500 hover:text-gray-900 hover:border-gray-300 dark:bg-gray-900 dark:border-gray-800 dark:text-gray-400 dark:hover:text-white dark:hover:border-gray-700'

  return (
    <header className="border-b border-gray-200 pb-5 dark:border-gray-800">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex items-center gap-6">
          <div className="flex items-center gap-2.5">
            <img src={`${import.meta.env.BASE_URL}logo_light.svg`} alt="" className="h-7 w-7 dark:hidden" />
            <img src={`${import.meta.env.BASE_URL}logo_dark.svg`} alt="" className="hidden h-7 w-7 dark:block" />
            <h1 className="text-lg font-semibold tracking-tight text-gray-900 dark:text-white">Insights</h1>
          </div>
          <nav className="flex rounded-lg border border-gray-200 bg-white p-0.5 dark:border-gray-800 dark:bg-gray-900">
            {NAV.map((item) => (
              <Link
                key={item.to}
                to={item.to}
                search={(prev: DashSearch) => prev}
                className="rounded-md px-3 py-1.5 text-xs font-medium text-gray-500 transition-all hover:text-gray-900 dark:text-gray-400 dark:hover:text-white"
                activeProps={{
                  className: 'rounded-md px-3 py-1.5 text-xs font-medium bg-indigo-600 text-white shadow-sm',
                }}
                activeOptions={{ exact: item.to === '/' }}
              >
                {item.label}
              </Link>
            ))}
          </nav>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <div className="flex rounded-lg border border-gray-200 bg-white p-0.5 dark:border-gray-800 dark:bg-gray-900">
            {PRESETS.map((p) => (
              <button
                key={p.value}
                onClick={() => setSearch({ range: p.value, from: undefined, to: undefined })}
                className={range === p.value ? SEG_ON : SEG_OFF}
              >
                {p.label}
              </button>
            ))}
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
                      let cls = 'h-8 text-xs transition-colors '
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
          </div>
          <button onClick={() => dash.refresh()} className={btn} title="Refresh">
            <RefreshCw className="mr-1 -mt-0.5 inline-block h-3.5 w-3.5" />
            Refresh
          </button>
          <button
            onClick={() => dash.setAutoRefresh(!dash.autoRefresh)}
            className={
              dash.autoRefresh
                ? 'rounded-lg border border-emerald-500/30 bg-emerald-600/10 px-3 py-1.5 text-xs font-medium text-emerald-600 dark:text-emerald-400'
                : btn
            }
          >
            <span
              className={`mr-1.5 inline-block h-1.5 w-1.5 rounded-full ${
                dash.autoRefresh ? 'animate-pulse bg-emerald-400' : 'bg-gray-400 dark:bg-gray-600'
              }`}
            />
            Auto
          </button>
          <button onClick={toggleTheme} className={btn} title="Toggle theme">
            {dark ? <Sun className="h-3.5 w-3.5" /> : <Moon className="h-3.5 w-3.5" />}
          </button>
        </div>
      </div>
    </header>
  )
}
