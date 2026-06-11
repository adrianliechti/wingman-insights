import type { ReactNode } from 'react'

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
