import { describe, expect, it } from 'vitest'
import { calendarActivitySeries, calendarChartRange } from './chartBuckets'

const hour = 3600e3
const day = 24 * hour
const to = new Date(2026, 9, 29, 15, 45).toISOString()

describe('shared Personal and Activity chart buckets', () => {
  it.each([[day, 24, true], [3 * day, 3, false], [7 * day, 7, false], [14 * day, 14, false], [30 * day, 30, false]])(
    'uses the preset number of bars for %i milliseconds', (span, count, hourly) => {
      const range = calendarChartRange(to, span)
      expect(range.buckets).toHaveLength(count)
      expect(range.hourly).toBe(hourly)
      expect(new Set(range.buckets).size).toBe(count)
      expect(new Date(range.from).getMinutes()).toBe(0)
      if (!hourly) expect(new Date(range.from).getHours()).toBe(0)
    },
  )

  it('ends an exact-midnight custom range in the previous calendar day', () => {
    const end = new Date(2026, 9, 29, 0, 0).toISOString()
    const range = calendarChartRange(end, 3 * day)
    expect(range.buckets.map((b) => new Date(b).getDate())).toEqual([26, 27, 28])
  })

  it('preserves stack totals and fills quiet days for every series', () => {
    const range = calendarChartRange(to, 7 * day)
    const points = [
      { bucket: new Date(2026, 9, 29, 10).toISOString(), label: 'first_observed', value: 1, count: 1 },
      { bucket: new Date(2026, 9, 29, 11).toISOString(), label: 'other_runs', value: 3, count: 3 },
      { bucket: new Date(2026, 9, 29, 12).toISOString(), label: 'other_runs', value: 2, count: 2 },
    ]
    const result = calendarActivitySeries(points, range)!
    expect(result).toHaveLength(14)
    expect(new Set(result.map((p) => p.bucket)).size).toBe(7)
    expect(result.reduce((sum, p) => sum + p.value, 0)).toBe(6)
    expect(result.filter((p) => p.value > 0).map((p) => p.value)).toEqual([1, 5])
    expect(calendarActivitySeries(null, range)).toBeNull()
    expect(calendarActivitySeries([], range)).toEqual([])
  })

  it('keeps both repeated DST hours in the same local calendar day', () => {
    const range = calendarChartRange(to, 7 * day)
    const points = ['2026-10-25T00:00:00Z', '2026-10-25T01:00:00Z'].map((bucket) => ({ bucket, label: 'Web Search', value: 1, count: 1 }))
    const result = calendarActivitySeries(points, range)!
    expect(result).toHaveLength(7)
    expect(result.reduce((sum, p) => sum + p.value, 0)).toBe(2)
    expect(result.find((p) => p.value === 2)).toBeDefined()
    const hourly = calendarChartRange(new Date(2026, 9, 25, 9).toISOString(), day)
    expect(hourly.buckets).toHaveLength(24)
    expect(new Set(hourly.buckets).size).toBe(24)
  })
})
