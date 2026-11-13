import { describe, expect, it } from 'vitest'
import { calendarActivitySeries, calendarChartRange, chartBuckets } from './chartBuckets'

const hour = 3600e3
const day = 24 * hour
const to = new Date(2026, 9, 29, 15, 45).toISOString()
const before = (end: string, span: number) => new Date(new Date(end).getTime() - span).toISOString()

describe('rolling chart window', () => {
  it('covers yesterday at 11:00 through today at 11:00 without activity', () => {
    const end = '2026-11-13T11:00:00.000Z'
    const buckets = chartBuckets(before(end, day), end, day)
    expect(buckets).toHaveLength(24)
    expect(buckets[0]).toBe('2026-11-12T11:00:00.000Z')
    expect(buckets.at(-1)).toBe('2026-11-13T10:00:00.000Z')
  })

  it('includes both partial edge hours instead of shortening the window', () => {
    const end = '2026-11-13T11:45:00.000Z'
    const buckets = chartBuckets(before(end, day), end, day)
    expect(buckets).toHaveLength(25)
    expect(buckets[0]).toBe('2026-11-12T11:00:00.000Z')
    expect(buckets.at(-1)).toBe('2026-11-13T11:00:00.000Z')
  })

  it.each([[3 * hour, 12], [3 * day, 12], [7 * day, 28], [14 * day, 56], [30 * day, 30]])(
    'fills the full window at the API interval for %i milliseconds', (span, count) => {
      const end = '2026-11-13T00:00:00.000Z'
      expect(chartBuckets(before(end, span), end, span)).toHaveLength(count)
    },
  )
})

describe('shared Personal and Activity chart buckets', () => {
  it.each([[day, 25, true], [3 * day, 4, false], [7 * day, 8, false], [14 * day, 15, false], [30 * day, 31, false]])(
    'covers the entire rolling window for %i milliseconds', (span, count, hourly) => {
      const from = before(to, span)
      const range = calendarChartRange(from, to)
      expect(range.buckets).toHaveLength(count)
      expect(range.hourly).toBe(hourly)
      expect(new Set(range.buckets).size).toBe(count)
      expect(range.from).toBe(from)
      expect(range.to).toBe(to)
      expect(new Date(range.buckets[0]).getMinutes()).toBe(0)
      if (!hourly) expect(new Date(range.buckets[0]).getHours()).toBe(0)
    },
  )

  it('ends an exact-midnight custom range in the previous calendar day', () => {
    const end = new Date(2026, 9, 29, 0, 0).toISOString()
    const range = calendarChartRange(before(end, 3 * day), end)
    expect(range.buckets.map((b) => new Date(b).getDate())).toEqual([26, 27, 28])
  })

  it('preserves stack totals and fills quiet days for every series', () => {
    const range = calendarChartRange(before(to, 7 * day), to)
    const points = [
      { bucket: new Date(2026, 9, 29, 10).toISOString(), label: 'first_observed', value: 1, count: 1 },
      { bucket: new Date(2026, 9, 29, 11).toISOString(), label: 'other_runs', value: 3, count: 3 },
      { bucket: new Date(2026, 9, 29, 12).toISOString(), label: 'other_runs', value: 2, count: 2 },
    ]
    const result = calendarActivitySeries(points, range)!
    expect(result).toHaveLength(16)
    expect(new Set(result.map((p) => p.bucket)).size).toBe(8)
    expect(result.reduce((sum, p) => sum + p.value, 0)).toBe(6)
    expect(result.filter((p) => p.value > 0).map((p) => p.value)).toEqual([1, 5])
    expect(calendarActivitySeries(null, range)).toBeNull()
    expect(calendarActivitySeries([], range)).toEqual([])
  })

  it('keeps both repeated DST hours in the same local calendar day', () => {
    const range = calendarChartRange(before(to, 7 * day), to)
    const points = ['2026-10-25T00:00:00Z', '2026-10-25T01:00:00Z'].map((bucket) => ({ bucket, label: 'Web Search', value: 1, count: 1 }))
    const result = calendarActivitySeries(points, range)!
    expect(result).toHaveLength(8)
    expect(result.reduce((sum, p) => sum + p.value, 0)).toBe(2)
    expect(result.find((p) => p.value === 2)).toBeDefined()
    const end = new Date(2026, 9, 25, 9).toISOString()
    const hourly = calendarChartRange(before(end, day), end)
    expect(hourly.buckets).toHaveLength(24)
    expect(new Set(hourly.buckets).size).toBe(24)
  })
})
