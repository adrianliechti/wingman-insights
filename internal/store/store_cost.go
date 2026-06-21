package store

import (
	"context"
	"sort"
	"time"

	"insights/internal/pricing"
)

// CostRow is token consumption priced via the models.dev catalog. Costs are
// USD. Priced is false when no price is known for (provider, model) — token
// counts are still reported so nothing is silently dropped.
//
// Cost is sourced from spans, the only signal that carries the per-request cache
// breakdown (gen_ai.usage.cache_*.input_tokens). InputTokens here is the billed,
// non-cached remainder (inclusive input minus cache), so the four token columns
// form a disjoint ledger that lines up with the four cost columns.
type CostRow struct {
	EndUserID    string `json:"enduser_id,omitempty"`
	EndUserEmail string `json:"enduser_email,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	RequestModel string `json:"request_model,omitempty"`

	InputTokens         float64 `json:"input_tokens"`
	OutputTokens        float64 `json:"output_tokens"`
	CacheReadTokens     float64 `json:"cache_read_tokens"`
	CacheCreationTokens float64 `json:"cache_creation_tokens"`
	// ReasoningTokens is a subset of OutputTokens (billed within output, so no
	// separate cost) — reported for visibility into "thinking" overhead.
	ReasoningTokens float64 `json:"reasoning_tokens"`

	InputCost         float64 `json:"input_cost"`
	OutputCost        float64 `json:"output_cost"`
	CacheReadCost     float64 `json:"cache_read_cost"`
	CacheCreationCost float64 `json:"cache_creation_cost"`
	TotalCost         float64 `json:"total_cost"`

	// CacheSavings is what the cache-read tokens would have cost at the
	// full input rate, minus what they actually cost.
	CacheSavings float64 `json:"cache_savings"`

	Priced bool `json:"priced"`
}

// QueryCostBreakdown returns priced token usage per (user, provider, model),
// sourced from the partition counters (or spans) so cached tokens are priced at
// their own rate. Base data for the per-user / per-model cost views and the CSV.
func (s *Store) QueryCostBreakdown(ctx context.Context, from, to time.Time, f Filter) ([]CostRow, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(user_id, '') as enduser_id,
			COALESCE(NULLIF(MAX(user_email), ''), '') as enduser_email,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY user_id, provider_name, request_model
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CostRow
	for rows.Next() {
		var user, email, provider, model string
		var p tokenParts
		if err := rows.Scan(&user, &email, &provider, &model,
			&p.Uncached, &p.CacheRead, &p.CacheWrite, &p.Response, &p.Reasoning); err != nil {
			return nil, err
		}
		price, priced := pricing.Lookup(provider, model)
		r := CostRow{
			EndUserID:           user,
			EndUserEmail:        email,
			ProviderName:        provider,
			RequestModel:        model,
			InputTokens:         p.Uncached, // billed (non-cached) input
			OutputTokens:        p.Response + p.Reasoning,
			CacheReadTokens:     p.CacheRead,
			CacheCreationTokens: p.CacheWrite,
			ReasoningTokens:     p.Reasoning,
			Priced:              priced,
		}
		if priced {
			r.InputCost = price.TokenCost("input", p.Uncached)
			r.OutputCost = price.TokenCost("output", p.Response+p.Reasoning)
			r.CacheReadCost = price.TokenCost("cache_read", p.CacheRead)
			r.CacheCreationCost = price.TokenCost("cache_creation", p.CacheWrite)
			r.TotalCost = r.InputCost + r.OutputCost + r.CacheReadCost + r.CacheCreationCost
			// Savings = what the cache-read tokens would have cost at the full
			// input rate, minus what they actually cost.
			r.CacheSavings = price.TokenCost("input", p.CacheRead) - r.CacheReadCost
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sortCostRows(result)
	return result, nil
}

// AggregateCostsByUser collapses a cost breakdown to one row per user.
func AggregateCostsByUser(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.EndUserID, CostRow{EndUserID: r.EndUserID, EndUserEmail: r.EndUserEmail}
	})
}

// AggregateCostsByModel collapses a cost breakdown to one row per provider/model.
func AggregateCostsByModel(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.ProviderName + "\x00" + r.RequestModel, CostRow{ProviderName: r.ProviderName, RequestModel: r.RequestModel}
	})
}

func aggregateCosts(rows []CostRow, keyFn func(CostRow) (string, CostRow)) []CostRow {
	byKey := make(map[string]*CostRow)
	for _, r := range rows {
		key, base := keyFn(r)
		agg, ok := byKey[key]
		if !ok {
			base.Priced = true
			agg = &base
			byKey[key] = agg
		}
		agg.InputTokens += r.InputTokens
		agg.OutputTokens += r.OutputTokens
		agg.CacheReadTokens += r.CacheReadTokens
		agg.CacheCreationTokens += r.CacheCreationTokens
		agg.ReasoningTokens += r.ReasoningTokens
		agg.InputCost += r.InputCost
		agg.OutputCost += r.OutputCost
		agg.CacheReadCost += r.CacheReadCost
		agg.CacheCreationCost += r.CacheCreationCost
		agg.TotalCost += r.TotalCost
		agg.CacheSavings += r.CacheSavings
		agg.Priced = agg.Priced && r.Priced
	}
	result := make([]CostRow, 0, len(byKey))
	for _, r := range byKey {
		result = append(result, *r)
	}
	sortCostRows(result)
	return result
}

// QueryCostTimeseries returns spend per time bucket and model, priced via the
// models.dev catalog. Sourced from the partition counters (or spans) so cached
// tokens are priced correctly.
func (s *Store) QueryCostTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, provider_name, request_model
		ORDER BY bucket
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		bucket time.Time
		model  string
	}
	costs := make(map[key]float64)
	var order []key
	for rows.Next() {
		var bucket time.Time
		var provider, model string
		var p tokenParts
		if err := rows.Scan(&bucket, &provider, &model,
			&p.Uncached, &p.CacheRead, &p.CacheWrite, &p.Response, &p.Reasoning); err != nil {
			return nil, err
		}
		price, priced := pricing.Lookup(provider, model)
		if !priced {
			continue
		}
		k := key{bucket, model}
		if _, seen := costs[k]; !seen {
			order = append(order, k)
		}
		costs[k] += p.cost(price)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]TimeseriesPoint, 0, len(order))
	for _, k := range order {
		result = append(result, TimeseriesPoint{Bucket: k.bucket, Label: k.model, Value: costs[k]})
	}
	return result, nil
}

func sortCostRows(rows []CostRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalCost != rows[j].TotalCost {
			return rows[i].TotalCost > rows[j].TotalCost
		}
		ti := rows[i].InputTokens + rows[i].OutputTokens
		tj := rows[j].InputTokens + rows[j].OutputTokens
		return ti > tj
	})
}
