import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { DashProvider } from '../dash'
import { groupSeries, TimeseriesPanel } from './charts'

const hour = 3600e3
const range = { from: '2026-11-12T11:45:00Z', to: '2026-11-13T11:45:00Z' }
const specs = { input: { label: 'Input', color: '#283c83' } }

describe('timeseries chart window', () => {
  it('preserves quiet hours before, between, and after recorded activity', () => {
    const result = groupSeries([
      { bucket: '2026-11-12T15:00:00Z', label: 'input', value: 10, count: 1 },
      { bucket: '2026-11-13T09:00:00.000Z', label: 'input', value: 20, count: 1 },
    ], 24 * hour, specs, range)
    expect(result.buckets).toHaveLength(25)
    expect(result.datasets[0].data).toEqual([
      0, 0, 0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 20, 0, 0,
    ])
    expect(result.partialCount).toBe(2)
  })

  it('keeps the same time axis for an entirely inactive user', () => {
    const empty = groupSeries([], 24 * hour, specs, range)
    expect(empty.buckets).toHaveLength(25)
    expect(empty.datasets[0].data).toEqual(Array(25).fill(0))
    expect(groupSeries([], 24 * hour, undefined, range).buckets).toEqual(empty.buckets)
    expect(groupSeries(null, 24 * hour, specs, range).datasets[0].data).toEqual(empty.datasets[0].data)
  })

  it('uses supplied calendar buckets rather than deriving them from activity', () => {
    const buckets = ['2026-11-11T23:00:00.000Z', '2026-11-12T23:00:00.000Z']
    const result = groupSeries([], 3 * 24 * hour, specs, { ...range, buckets })
    expect(result.buckets).toEqual(buckets)
    expect(result.datasets[0].data).toEqual([0, 0])
  })

  it.each([null, []])('renders the chart for an inactive API response: %j', (points) => {
    const html = renderToStaticMarkup(createElement(DashProvider, {
      search: { range: '24h' },
      children: createElement(TimeseriesPanel, { points, spanMs: 24 * hour, specs, loading: false }),
    }))
    expect(html).toContain('<canvas')
    expect(html).not.toContain('No data')
  })
})
