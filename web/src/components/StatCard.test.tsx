import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { StatStrip } from './StatCard'

describe('independent headline states', () => {
  it('shows ready users while sessions still have no data', () => {
    const html = renderToStaticMarkup(createElement(StatStrip, { stats: [
      { label: 'Users', value: '4200', firstLoad: false, loading: false },
      { label: 'Sessions', value: '0', firstLoad: true, loading: true },
    ] }))
    expect(html).toContain('>4200</span>')
    expect(html).not.toContain('>0</span>')
    expect(html.match(/animate-pulse/g)).toHaveLength(1)
    expect(html).not.toContain('opacity-40')
  })

  it('keeps existing values visible but dims only the refreshing headline', () => {
    const html = renderToStaticMarkup(createElement(StatStrip, { stats: [
      { label: 'Users', value: '4200', firstLoad: false, loading: false },
      { label: 'Sessions', value: '53810', firstLoad: false, loading: true },
    ] }))
    expect(html).toContain('>4200</span>')
    expect(html).toContain('>53810</span>')
    expect(html.match(/opacity-40/g)).toHaveLength(1)
  })

  it('preserves shared loading states for existing callers and allows ready overrides', () => {
    const html = renderToStaticMarkup(createElement(StatStrip, { firstLoad: true, loading: true, stats: [
      { label: 'Ready', value: '123', firstLoad: false, loading: false },
      { label: 'Pending', value: '456' },
    ] }))
    expect(html).toContain('>123</span>')
    expect(html).not.toContain('>456</span>')
    expect(html.match(/animate-pulse/g)).toHaveLength(1)
  })
})
