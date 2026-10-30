import { format } from 'date-fns'
import type { TimeseriesPoint } from '../types'

// Personal and Activity share exactly N clock-hour/calendar-day labels,
// including the current partial interval. The end of the window is exclusive.
export function calendarChartRange(to: string, spanMs: number) {
  const hourly = spanMs <= 26 * 3600e3
  const count = Math.max(1, Math.ceil(spanMs / (hourly ? 3600e3 : 24 * 3600e3)))
  const start = new Date(new Date(to).getTime() - 1)
  if (hourly) {
    start.setMinutes(0, 0, 0)
    start.setTime(start.getTime() - (count - 1) * 3600e3)
  } else {
    start.setHours(0, 0, 0, 0)
    start.setDate(start.getDate() - (count - 1))
  }
  const buckets: string[] = []
  const cursor = new Date(start)
  for (let i = 0; i < count; i++) {
    buckets.push(cursor.toISOString())
    if (hourly) cursor.setTime(cursor.getTime() + 3600e3)
    else cursor.setDate(cursor.getDate() + 1)
  }
  return { from: start.toISOString(), to, hourly, buckets }
}

// Aggregate hourly execution counts into local calendar days. Fetching hourly
// keeps events on the correct local date when the UTC offset changes at DST.
// All series use the same grid, including intervals with no recorded activity.
export function calendarActivitySeries(points: TimeseriesPoint[] | null, range: ReturnType<typeof calendarChartRange>): TimeseriesPoint[] | null {
  if (points === null) return null
  const keyFor = (time: string) => range.hourly ? String(new Date(time).getTime()) : format(new Date(time), 'yyyy-MM-dd')
  const series = new Map<string, Map<string, { value: number; count: number }>>()
  for (const point of points) {
    const label = point.label ?? ''
    const buckets = series.get(label) ?? new Map()
    const key = keyFor(point.bucket)
    const previous = buckets.get(key) ?? { value: 0, count: 0 }
    buckets.set(key, { value: previous.value + point.value, count: previous.count + point.count })
    series.set(label, buckets)
  }
  return range.buckets.flatMap((bucket) => [...series].map(([label, values]) => ({
    bucket, label, ...(values.get(keyFor(bucket)) ?? { value: 0, count: 0 }),
  })))
}
