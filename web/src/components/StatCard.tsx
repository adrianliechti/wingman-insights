export interface Stat {
  label: string
  value: string
  sub?: string
}

// StatStrip renders headline numbers as one flat divided row instead of
// individual cards.
export function StatStrip({ stats }: { stats: Stat[] }) {
  return (
    <div className="flex flex-col divide-y divide-gray-200 rounded-lg border border-gray-200 sm:flex-row sm:divide-x sm:divide-y-0 dark:divide-gray-800 dark:border-gray-800">
      {stats.map((s) => (
        <div key={s.label} className="flex-1 px-5 py-4">
          <p className="text-xs font-medium uppercase tracking-wider text-gray-500">{s.label}</p>
          <p className="mt-1.5 text-2xl font-semibold tabular-nums tracking-tight text-gray-900 dark:text-white">
            {s.value}
          </p>
          {s.sub && <p className="mt-0.5 text-xs text-gray-500">{s.sub}</p>}
        </div>
      ))}
    </div>
  )
}
