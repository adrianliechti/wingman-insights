import { format } from 'date-fns'
import type { TimeseriesPoint } from '../types'

export function chartInterval(spanMs: number) {
  if (spanMs <= 6 * 3600e3) return { interval: '15 minute', widthMs: 15 * 60e3 }
  if (spanMs <= 48 * 3600e3) return { interval: '1 hour', widthMs: 3600e3 }
  if (spanMs <= 14 * 24 * 3600e3) return { interval: '6 hour', widthMs: 6 * 3600e3 }
  return { interval: '1 day', widthMs: 24 * 3600e3 }
}

// Match the API's UTC-aligned buckets, including quiet and partial intervals.
export function chartBuckets(from: string, to: string, spanMs: number): string[] {
  const { widthMs } = chartInterval(spanMs)
  const start = Math.floor(new Date(from).getTime() / widthMs) * widthMs
  const end = new Date(to).getTime()
  const buckets: string[] = []
  for (let time = start; time < end; time += widthMs) buckets.push(new Date(time).toISOString())
  return buckets
}

// Personal and Activity share clock-hour/calendar-day labels covering the
// exact selected window. Partial intervals at either edge are included.
export function calendarChartRange(from: string, to: string) {
  const end = new Date(to).getTime()
  const spanMs = end - new Date(from).getTime()
  const hourly = spanMs <= 26 * 3600e3
  const start = new Date(from)
  if (hourly) {
    start.setMinutes(0, 0, 0)
  } else {
    start.setHours(0, 0, 0, 0)
  }
  const buckets: string[] = []
  const cursor = new Date(start)
  while (cursor.getTime() < end) {
    buckets.push(cursor.toISOString())
    if (hourly) cursor.setTime(cursor.getTime() + 3600e3)
    else cursor.setDate(cursor.getDate() + 1)
  }
  return { from, to, hourly, buckets }
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
