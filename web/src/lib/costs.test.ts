import { describe, expect, it } from 'vitest'
import { priceTier, tierRate, type ModelRate } from './costs'

// rate builds a ModelRate from catalog list $/1M input/output rates. blended is
// a plausible usage blend but the tier must be decided from the directional
// rates, so its exact value is irrelevant to these cases.
function rate(input: number, output: number): ModelRate {
  return { input, output, blended: (input * 3 + output) / 4 }
}

describe('priceTier via tierRate', () => {
  const tierOf = (r: ModelRate) => priceTier(tierRate(r))?.label ?? null

  it('classifies the reference models', () => {
    // List $/1M input/output from internal/pricing/models.json.
    expect(tierOf(rate(1, 5))).toBe('Budget') // claude-haiku-4-5
    expect(tierOf(rate(3, 15))).toBe('Standard') // claude-sonnet-4-5
    expect(tierOf(rate(5, 25))).toBe('Premium') // claude-opus-5
    expect(tierOf(rate(5, 30))).toBe('Premium') // gpt-5.5
    expect(tierOf(rate(2.5, 10))).toBe('Standard') // gpt-4o
  })

  it('stays stable when input is discounted by caching', () => {
    // Opus 5 with a cache-heavy workload: observed input drops below list, but
    // the model is still Premium because output rate is unaffected by caching.
    expect(tierOf(rate(4.81, 25))).toBe('Premium')
    expect(tierOf(rate(2, 25))).toBe('Premium')
  })

  it('flags cheap models as Budget', () => {
    expect(tierOf(rate(0.2, 1.2))).toBe('Budget') // gpt-5.6-luna
  })

  it('returns null for unpriced models', () => {
    expect(priceTier(tierRate(rate(0, 0)))).toBeNull()
  })

  it('falls back to blended when directional rates are missing', () => {
    expect(tierRate({ input: 0, output: 0, blended: 5 })).toBe(5)
  })
})
