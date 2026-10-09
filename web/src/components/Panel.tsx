import type { ReactNode } from 'react'
import { Loader2 } from 'lucide-react'

export function Panel({
  title,
  sub,
  action,
  children,
  className = '',
}: {
  title: string
  sub?: string
  action?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`rounded-lg border border-gray-200 p-5 dark:border-gray-800 ${className}`}>
      <div className="mb-4 flex items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">{title}</h2>
          {sub && <p className="mt-0.5 text-xs text-gray-500">{sub}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  )
}

export function PanelMessage({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center justify-center gap-1.5 py-10 text-sm text-gray-400 dark:text-gray-600">
      {children}
    </div>
  )
}

// Loading states come in two flavours, because "no data yet" and "data on
// screen is out of date" are different problems for the reader:
//
//   PanelLoading — first load, when there is nothing to show yet. A shimmering
//     stand-in keeps the panel's footprint so the page doesn't reflow when the
//     real content lands.
//   Stale — a refetch (filter or range change) where the previous result is
//     still on screen. The content stays visible for context but is dimmed and
//     marked, so a stale chart can never be mistaken for the current one.

// Skeleton is one shimmering placeholder block.
export function Skeleton({ className = '' }: { className?: string }) {
  return <div className={`animate-pulse rounded bg-gray-200 dark:bg-gray-800 ${className}`} />
}

// barHeights gives the skeleton an uneven, chart-like silhouette; the staggered
// delays make the shimmer travel across it rather than pulsing as one block.
const barHeights = ['45%', '70%', '55%', '85%', '40%', '65%', '95%', '50%', '75%', '60%', '88%', '42%']

// PanelLoading stands in for a panel's content during the first load.
export function PanelLoading({ height = 'h-64' }: { height?: string }) {
  return (
    <div className={`flex ${height} items-end gap-1.5`} role="status" aria-label="Loading">
      {barHeights.map((h, i) => (
        <div
          key={i}
          className="flex-1 animate-pulse rounded-t bg-gray-200 dark:bg-gray-800"
          style={{ height: h, animationDelay: `${i * 90}ms` }}
        />
      ))}
    </div>
  )
}

// PanelLoadingRows is the PanelLoading equivalent for tabular content.
export function PanelLoadingRows({ rows = 6 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-2 py-1" role="status" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div
          key={i}
          className="h-7 animate-pulse rounded bg-gray-200 dark:bg-gray-800"
          style={{ animationDelay: `${i * 90}ms`, opacity: 1 - i * 0.1 }}
        />
      ))}
    </div>
  )
}

// Stale wraps content that is still showing a previous result while a new one
// is in flight. `when` false renders children untouched, so this is safe to
// leave in place permanently.
export function Stale({ when, children }: { when?: boolean; children: ReactNode }) {
  if (!when) return <>{children}</>
  return (
    <div className="relative" aria-busy="true">
      <div className="pointer-events-none select-none opacity-30 transition-opacity duration-200">
        {children}
      </div>
      <div className="absolute inset-0 flex items-center justify-center">
        <span className="flex items-center gap-2 rounded-full border border-gray-200 bg-white/90 px-3 py-1.5 text-xs font-medium text-gray-500 shadow-sm backdrop-blur-sm dark:border-gray-700 dark:bg-gray-900/90 dark:text-gray-400">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Updating…
        </span>
      </div>
    </div>
  )
}
