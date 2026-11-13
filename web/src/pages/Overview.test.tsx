import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Overview } from './Overview'

const { useApi } = vi.hoisted(() => ({ useApi: vi.fn() }))
vi.mock('../dash', () => ({ useApi, useDash: () => ({ spanMs: 30 * 86400e3 }) }))
vi.mock('../components/TokenChart', () => ({ TokenChart: () => null }))
vi.mock('../components/DataTable', () => ({ DataTable: () => null }))
vi.mock('../components/charts', () => ({
  ChartLegend: () => null, Doughnut: () => null, TimeseriesPanel: () => null,
  CHART: { blue: 'blue', positive: 'green', warning: 'orange', negative: 'red' }, PALETTE: [],
}))

function section(markup: string, label: string) {
  const afterLabel = markup.slice(markup.indexOf(`${label}</p>`) + label.length + 4)
  return afterLabel.slice(0, afterLabel.indexOf('</div>'))
}

describe('Overview headline loading', () => {
  beforeEach(() => useApi.mockReset())

  // Every combination covers both initial loading and a refresh with old data
  // still present. A ready number must never wait for a different endpoint.
  it.each(Array.from({ length: 8 }, (_, mask) => [[mask, false], [mask, true]] as const).flat())('loads each headline independently, pending mask %i, cached %s', (mask, cached) => {
    const costLoading = Boolean(mask & 1)
    const summaryLoading = Boolean(mask & 2)
    const activeLoading = Boolean(mask & 4)
    useApi.mockImplementation((path: string) => {
      const replies: Record<string, { data: unknown; loading: boolean }> = {
        '/api/genai/costs': { data: [{ total_cost: 3.5, cache_savings: 0 }], loading: costLoading },
        '/api/genai/token-summary': { data: [
          { token_type: 'input', total_tokens: 10, total_requests: 31 },
          { token_type: 'output', total_tokens: 12, total_requests: 0 },
        ], loading: summaryLoading },
        '/api/genai/active-users': { data: { dau: 77, wau: 88, mau: 99 }, loading: activeLoading },
      }
      const reply = replies[path] ?? { data: [], loading: false }
      return { ...reply, data: reply.loading && !cached ? null : reply.data }
    })
    const markup = renderToStaticMarkup(createElement(Overview))
    for (const [label, value, loading] of [
      ['Estimated Cost', '$3.50', costLoading],
      ['Tokens', '22', summaryLoading],
      ['Requests', '31', summaryLoading],
      ['Active Users', '77', activeLoading],
    ] as const) {
      const output = section(markup, label)
      expect(output.includes('shimmer'), label).toBe(loading)
      if (!loading) expect(output, label).toContain(value)
    }
    if (!activeLoading) expect(section(markup, 'Active Users')).toContain('88 wk · 99 mo')
  })
})
