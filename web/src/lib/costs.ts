import type { CostRow } from '../types'

// Client-side twin of the server's AggregateCostsBy* helpers (store_cost.go):
// the FinOps page fetches the full breakdown (group_by=none) once and derives
// every grouping from it, instead of running the same DuckDB scan once per
// grouping. Semantics match the server: sums per key, priced = AND, sorted by
// cost then token volume.
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
