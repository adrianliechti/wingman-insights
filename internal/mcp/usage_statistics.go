package mcpserver

import (
	"math"

	"insights/internal/store"
)

// TokenUsage is the shared token and cost ledger. Input includes cache reads and
// cache creation; output includes reasoning, so total = input + output.
type TokenUsage struct {
	CostUSD             float64  `json:"cost_usd"`
	CacheSavingsUSD     float64  `json:"cache_savings_usd"`
	TotalTokens         int64    `json:"total_tokens"`
	InputTokens         int64    `json:"input_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	ReasoningTokens     int64    `json:"reasoning_tokens"`
	UnpricedTokens      int64    `json:"unpriced_tokens"`
	CacheReadPercent    *float64 `json:"cache_read_percent,omitempty"`
	ReasoningPercent    *float64 `json:"reasoning_percent,omitempty"`
}

// Usage adds provider-call counts and per-call averages to the ledger.
// Undefined ratios (zero denominator) are omitted rather than reported as zero.
type Usage struct {
	Requests int64 `json:"requests"`
	TokenUsage
	CostPerRequestUSD      *float64 `json:"cost_per_request_usd,omitempty"`
	InputTokensPerRequest  *float64 `json:"input_tokens_per_request,omitempty"`
	OutputTokensPerRequest *float64 `json:"output_tokens_per_request,omitempty"`
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func usd(v float64) float64 { return round(v, 6) }

// ratio rounds to two decimals and is nil when undefined.
func ratio(n, d float64) *float64 {
	if d == 0 {
		return nil
	}
	v := round(n/d, 2)
	return &v
}

func percent(n, d float64) *float64 { return ratio(100*n, d) }

// tokenUsage takes uncached input, matching the store's disjoint ledger.
func tokenUsage(cost, savings float64, uncached, cacheRead, cacheCreation, output, reasoning, unpriced int64) TokenUsage {
	input := uncached + cacheRead + cacheCreation
	return TokenUsage{
		CostUSD: usd(cost), CacheSavingsUSD: usd(savings),
		TotalTokens: input + output, InputTokens: input, CacheReadTokens: cacheRead, CacheCreationTokens: cacheCreation,
		OutputTokens: output, ReasoningTokens: reasoning, UnpricedTokens: unpriced,
		CacheReadPercent: percent(float64(cacheRead), float64(input)), ReasoningPercent: percent(float64(reasoning), float64(output)),
	}
}

func newUsage(u store.UsageTotals) Usage {
	t := tokenUsage(u.Cost, u.Tokens.CacheSavings, u.Tokens.Input, u.CacheReadTokens, u.CacheCreationTokens, u.Tokens.Output, u.Tokens.Reasoning, u.UnpricedTokens)
	n := float64(u.Requests)
	return Usage{Requests: u.Requests, TokenUsage: t,
		CostPerRequestUSD:      ratioUSD(u.Cost, n),
		InputTokensPerRequest:  ratio(float64(t.InputTokens), n),
		OutputTokensPerRequest: ratio(float64(t.OutputTokens), n)}
}

func ratioUSD(cost, d float64) *float64 {
	if d == 0 {
		return nil
	}
	v := usd(cost / d)
	return &v
}
