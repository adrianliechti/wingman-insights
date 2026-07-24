package store

import "insights/internal/pricing"

// Token analytics need the five disjoint token partitions — uncached input,
// cache read, cache write, response output and reasoning output — derived from
// the released-spec gen_ai.usage.* span attributes: input_tokens / output_tokens
// are the inclusive totals, with cache and reasoning as subsets (OTel GenAI
// semconv). The cost, cache, reasoning and composition queries all SELECT these
// columns from genai_spans.
const spansPartCols = `
	COALESCE(SUM(GREATEST(input_tokens - cache_read_tokens - cache_creation_tokens, 0)), 0) AS uncached,
	COALESCE(SUM(cache_read_tokens), 0) AS cache_read,
	COALESCE(SUM(cache_creation_tokens), 0) AS cache_write,
	COALESCE(SUM(GREATEST(output_tokens - reasoning_tokens, 0)), 0) AS response,
	COALESCE(SUM(reasoning_tokens), 0) AS reasoning`

// tokenParts holds the five disjoint partitions for one group (bucket, model, …).
type tokenParts struct {
	Uncached   float64
	CacheRead  float64
	CacheWrite float64
	Response   float64
	Reasoning  float64
}

// cost prices the partitions: uncached at the input rate, cache at their own
// rates, and all output (response + reasoning) at the output rate. Aggregates
// have lost the per-request input sizes, so long-context tiers (Price.Tiers)
// cannot apply here — everything bills at the base card. Real spend always
// comes from the per-span materialized cost columns, which are tier-aware.
func (p tokenParts) cost(pr pricing.Price) float64 {
	return pr.TokenCost("input", p.Uncached) +
		pr.TokenCost("cache_read", p.CacheRead) +
		pr.TokenCost("cache_creation", p.CacheWrite) +
		pr.TokenCost("output", p.Response+p.Reasoning)
}
