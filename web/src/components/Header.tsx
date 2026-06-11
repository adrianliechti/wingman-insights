import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Moon, RefreshCw, Sun } from 'lucide-react'
import { useDash } from '../dash'
import type { DashSearch, RangeKey } from '../dash'

const PRESETS: { label: string; value: RangeKey }[] = [
  { label: 'Today', value: 'today' },
  { label: '24h', value: '24h' },
  { label: '3d', value: '3d' },
  { label: '7d', value: '7d' },
  { label: '30d', value: '30d' },
  { label: 'Custom', value: 'custom' },
]

const NAV = [
  { to: '/', label: 'Overview' },
  { to: '/traces', label: 'Traces' },
  { to: '/product', label: 'Product' },
  { to: '/operations', label: 'Operations' },
  { to: '/finops', label: 'FinOps' },
]

function toLocalInput(iso: string): string {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function Header() {
  const dash = useDash()
  const navigate = useNavigate()
  const range = dash.search.range ?? '24h'
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const [customFrom, setCustomFrom] = useState(() => toLocalInput(dash.from))
  const [customTo, setCustomTo] = useState(() => toLocalInput(dash.to))

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

  function applyCustom() {
    const from = new Date(customFrom)
    const to = new Date(customTo)
    if (isNaN(from.getTime()) || isNaN(to.getTime()) || from >= to) return
    setSearch({ range: 'custom', from: from.toISOString(), to: to.toISOString() })
  }

  const btn =
    'rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors bg-white border-gray-200 text-gray-500 hover:text-gray-900 hover:border-gray-300 dark:bg-gray-900 dark:border-gray-800 dark:text-gray-400 dark:hover:text-white dark:hover:border-gray-700'

  return (
    <header className="border-b border-gray-200 pb-5 dark:border-gray-800">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex items-center gap-6">
          <div className="flex items-center gap-2.5">
            <img src="/logo_light.svg" alt="" className="h-7 w-7 dark:hidden" />
            <img src="/logo_dark.svg" alt="" className="hidden h-7 w-7 dark:block" />
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
                onClick={() =>
                  p.value === 'custom'
                    ? setSearch({ range: 'custom', from: new Date(customFrom).toISOString(), to: new Date(customTo).toISOString() })
                    : setSearch({ range: p.value, from: undefined, to: undefined })
                }
                className={
                  range === p.value
                    ? 'rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white shadow-sm'
                    : 'rounded-md px-3 py-1.5 text-xs font-medium text-gray-500 transition-all hover:text-gray-900 dark:text-gray-400 dark:hover:text-white'
                }
              >
                {p.label}
              </button>
            ))}
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

      {range === 'custom' && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <input
            type="datetime-local"
            value={customFrom}
            onChange={(e) => setCustomFrom(e.target.value)}
            className="rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-xs text-gray-700 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-300 dark:[color-scheme:dark]"
          />
          <span className="text-xs text-gray-500">→</span>
          <input
            type="datetime-local"
            value={customTo}
            onChange={(e) => setCustomTo(e.target.value)}
            className="rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-xs text-gray-700 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-300 dark:[color-scheme:dark]"
          />
          <button onClick={applyCustom} className="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500">
            Apply
          </button>
        </div>
      )}
    </header>
  )
}
