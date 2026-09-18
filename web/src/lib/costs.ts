import type { CostRow } from '../types'

// Client-side twin of the server's AggregateCostsBy* helpers (store_cost.go):
// the FinOps page fetches the full breakdown (group_by=none) once and derives
// every grouping from it, instead of running the same DuckDB scan once per
// grouping. Semantics match the server: sums per key (including
// unpriced_tokens, additive so mixed priced/unpriced rows still yield an
// accurate token share), priced = AND, sorted by cost then token volume.
function aggregate(rows: CostRow[], baseOf: (r: CostRow) => Partial<CostRow> & { key: string }): CostRow[] {
  const byKey = new Map<string, CostRow>()
  for (const r of rows) {
    const { key, ...base } = baseOf(r)
    let agg = byKey.get(key)
    if (!agg) {
      agg = {
        ...base,
        input_tokens: 0,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        reasoning_tokens: 0,
        input_cost: 0,
        output_cost: 0,
        cache_read_cost: 0,
        cache_creation_cost: 0,
        total_cost: 0,
        cache_savings: 0,
        priced: true,
        unpriced_tokens: 0,
      }
      byKey.set(key, agg)
    }
    agg.input_tokens += r.input_tokens
    agg.output_tokens += r.output_tokens
    agg.cache_read_tokens += r.cache_read_tokens
    agg.cache_creation_tokens += r.cache_creation_tokens
    agg.reasoning_tokens += r.reasoning_tokens
    agg.input_cost += r.input_cost
    agg.output_cost += r.output_cost
    agg.cache_read_cost += r.cache_read_cost
    agg.cache_creation_cost += r.cache_creation_cost
    agg.total_cost += r.total_cost
    agg.cache_savings += r.cache_savings
    agg.priced = agg.priced && r.priced
    agg.unpriced_tokens += r.unpriced_tokens
  }
  return [...byKey.values()].sort((a, b) =>
    b.total_cost !== a.total_cost
      ? b.total_cost - a.total_cost
      : b.input_tokens + b.output_tokens - (a.input_tokens + a.output_tokens),
  )
}

export const costsByUser = (rows: CostRow[]) =>
  aggregate(rows, (r) => ({
    key: r.id ?? '',
    id: r.id,
    name: r.name,
    kind: r.kind,
    department: r.department,
    location: r.location,
    username: r.username,
    former: r.former,
  }))

export const costsByApp = (rows: CostRow[]) =>
  aggregate(rows, (r) => ({ key: r.app_id ?? '', app_id: r.app_id, app_name: r.app_name }))

export const costsByDepartment = (rows: CostRow[]) =>
  aggregate(rows, (r) => ({ key: r.department ?? '', department: r.department }))

export const costsByModel = (rows: CostRow[]) =>
  aggregate(rows, (r) => ({
    key: `${r.provider_name ?? ''}\0${r.request_model ?? ''}`,
    provider_name: r.provider_name,
    request_model: r.request_model,
  }))

// PriceTier classifies a model into a spend bracket for the tier badge.
// `dollars` (1–3) drives the filled dollar-sign count; `label` is the name.
export interface PriceTier {
  dollars: number
  label: string
}

// ModelRate is a model's derived $/1M-token rates: the blended rate shown to
// the user, plus the directional input/output rates the tier is decided from.
export interface ModelRate {
  blended: number
  input: number
  output: number
}

// tierRate is the rate the tier badge classifies on. It uses the model's output
// $/1M rate: unlike input, output isn't discounted by caching, so it's stable
// across workloads and is the defining signal of how expensive a model is. Only
// when output is missing does it fall back to input, then the observed blend.
export function tierRate(rate: ModelRate): number {
  if (rate.output > 0) return rate.output
  if (rate.input > 0) return rate.input
  return rate.blended
}

// priceTier maps an output $/1M rate to a tier. The edges sit between the
// common model bands so frontier models (output ≥ $20/1M) read Premium, mid
// models (≥ $7) Standard, and cheap workhorses Budget. Rate 0 → null (unpriced).
export function priceTier(ratePerM: number): PriceTier | null {
  if (ratePerM <= 0) return null
  if (ratePerM < 7) return { dollars: 1, label: 'Budget' }
  if (ratePerM < 20) return { dollars: 2, label: 'Standard' }
  return { dollars: 3, label: 'Premium' }
}

