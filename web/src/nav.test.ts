import { describe, expect, it } from 'vitest'
import { NAV, customerViews, opsViews, resolveView, viewSearch } from './nav'

describe('resolveView', () => {
  it('picks the named view', () => {
    expect(resolveView(opsViews, 'traces')).toBe('traces')
    expect(resolveView(customerViews, 'anomalies')).toBe('anomalies')
  })

  // A hand-edited or stale URL must still render a page, and — since the
  // submenu highlight is derived from this — must resolve to the same view the
  // page renders, so the highlight can't disagree with the content.
  it('falls back to the default for a missing or unknown view', () => {
    expect(resolveView(opsViews, undefined)).toBe('metrics')
    expect(resolveView(opsViews, '')).toBe('metrics')
    expect(resolveView(opsViews, 'bogus')).toBe('metrics')
    // A view of another section is just as unknown here.
    expect(resolveView(opsViews, 'anomalies')).toBe('metrics')
  })
})

describe('viewSearch', () => {
  it('clears the param for the default view and names the others', () => {
    expect(viewSearch(opsViews, 'metrics')).toBeUndefined()
    expect(viewSearch(opsViews, 'traces')).toBe('traces')
    expect(viewSearch(customerViews, 'engagement')).toBeUndefined()
    expect(viewSearch(customerViews, 'classification')).toBe('classification')
  })

  // The default view's link must round-trip to the default view, otherwise the
  // canonical URL (/operations) and the submenu would disagree.
  it('round-trips through resolveView', () => {
    for (const v of opsViews) {
      expect(resolveView(opsViews, viewSearch(opsViews, v.id))).toBe(v.id)
    }
    for (const v of customerViews) {
      expect(resolveView(customerViews, viewSearch(customerViews, v.id))).toBe(v.id)
    }
  })
})

describe('NAV', () => {
  it('has unique paths and non-empty view lists', () => {
    const paths = NAV.map((n) => n.to)
    expect(new Set(paths).size).toBe(paths.length)
    for (const item of NAV) {
      if (!item.views) continue
      expect(item.views.length).toBeGreaterThan(1)
      const ids = item.views.map((v) => v.id)
      expect(new Set(ids).size).toBe(ids.length)
    }
  })

  // Every section's views must be distinguishable across the whole nav: the
  // `view` param is global, so two sections sharing a view id would make a
  // cross-section link ambiguous.
  it('uses view ids that are unique across sections', () => {
    const ids = NAV.flatMap((n) => (n.views ?? []).map((v) => v.id))
    expect(new Set(ids).size).toBe(ids.length)
  })
})
