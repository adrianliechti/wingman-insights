export interface Stat {
  label: string
  value: string
  sub?: string
  // delta shows a period-over-period change. positiveIsGood flips the coloring
  // (e.g. cost rising is bad, active users rising is good).
  delta?: { pct: number | null; positiveIsGood?: boolean }
}

function DeltaChip({ pct, positiveIsGood = true }: { pct: number; positiveIsGood?: boolean }) {
  const up = pct >= 0
  const color =
    Math.round(pct) === 0
      ? 'text-gray-400 dark:text-gray-500'
      : up === positiveIsGood
        ? 'text-emerald-600 dark:text-emerald-400'
        : 'text-red-500 dark:text-red-400'
  return (
    <span className={`text-xs font-semibold tabular-nums ${color}`} title="vs previous period">
      {up ? '↑' : '↓'} {Math.abs(pct).toFixed(0)}%
    </span>
  )
}

// StatStrip renders headline numbers as one flat divided row instead of
// individual cards.
export function StatStrip({ stats }: { stats: Stat[] }) {
  return (
    <div className="flex flex-col divide-y divide-gray-200 rounded-lg border border-gray-200 sm:flex-row sm:divide-x sm:divide-y-0 dark:divide-gray-800 dark:border-gray-800">
      {stats.map((s) => (
        <div key={s.label} className="flex-1 px-5 py-4">
          <p className="text-xs font-medium uppercase tracking-wider text-gray-500">{s.label}</p>
          <p className="mt-1.5 flex items-baseline gap-2">
            <span className="text-2xl font-semibold tabular-nums tracking-tight text-gray-900 dark:text-white">
              {s.value}
            </span>
            {s.delta && s.delta.pct != null && (
              <DeltaChip pct={s.delta.pct} positiveIsGood={s.delta.positiveIsGood} />
            )}
          </p>
          {s.sub && <p className="mt-0.5 text-xs text-gray-500">{s.sub}</p>}
        </div>
      ))}
    </div>
  )
}
