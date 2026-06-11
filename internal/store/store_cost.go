package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"insights/internal/pricing"
)

// CostRow is token consumption priced via the models.dev catalog. Costs are
// USD. Priced is false when no price is known for (provider, model) — token
// counts are still reported so nothing is silently dropped.
type CostRow struct {
	EndUserID    string `json:"enduser_id,omitempty"`
	EndUserEmail string `json:"enduser_email,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	RequestModel string `json:"request_model,omitempty"`

	InputTokens         float64 `json:"input_tokens"`
	OutputTokens        float64 `json:"output_tokens"`
	CacheReadTokens     float64 `json:"cache_read_tokens"`
	CacheCreationTokens float64 `json:"cache_creation_tokens"`

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

// QueryCostBreakdown returns priced token usage per (user, provider, model).
// It is the base data for the per-user / per-model cost views and the CSV report.
func (s *Store) QueryCostBreakdown(ctx context.Context, from, to time.Time, f Filter) ([]CostRow, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			COALESCE(enduser_id, '') as enduser_id,
			COALESCE(enduser_email, '') as enduser_email,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,
			COALESCE(token_type, '') as token_type,
			COALESCE(SUM(sum), 0) as tokens
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY enduser_id, enduser_email, provider_name, request_model, token_type
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := make(map[string]*CostRow)
	prices := make(map[string]pricing.Price)
	for rows.Next() {
		var user, email, provider, model, tokenType string
		var tokens float64
		if err := rows.Scan(&user, &email, &provider, &model, &tokenType, &tokens); err != nil {
			return nil, err
		}
		key := user + "\x00" + provider + "\x00" + model
		r, ok := byKey[key]
		if !ok {
			price, priced := pricing.Lookup(provider, model)
			r = &CostRow{EndUserID: user, EndUserEmail: email, ProviderName: provider, RequestModel: model, Priced: priced}
			byKey[key] = r
			prices[key] = price
		}
		cost := 0.0
		if r.Priced {
			cost = prices[key].TokenCost(tokenType, tokens)
		}
		switch tokenType {
		case "input":
			r.InputTokens += tokens
			r.InputCost += cost
		case "output":
			r.OutputTokens += tokens
			r.OutputCost += cost
		case "cache_read":
			r.CacheReadTokens += tokens
			r.CacheReadCost += cost
			if r.Priced {
				r.CacheSavings += prices[key].TokenCost("input", tokens) - cost
			}
		case "cache_creation":
			r.CacheCreationTokens += tokens
			r.CacheCreationCost += cost
		}
		r.TotalCost += cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]CostRow, 0, len(byKey))
	for _, r := range byKey {
		result = append(result, *r)
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
// models.dev catalog.
func (s *Store) QueryCostTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]TimeseriesPoint, error) {
	clause, fargs := f.genaiClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,
			COALESCE(token_type, '') as token_type,
			COALESCE(SUM(sum), 0) as tokens
		FROM genai_metrics
		WHERE metric_name = 'gen_ai.client.token.usage'
		  AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, provider_name, request_model, token_type
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
		var provider, model, tokenType string
		var tokens float64
		if err := rows.Scan(&bucket, &provider, &model, &tokenType, &tokens); err != nil {
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
		costs[k] += price.TokenCost(tokenType, tokens)
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

// WhatIfRow compares actual model spend against repricing the same token
// volumes at a target model's rates.
type WhatIfRow struct {
	ProviderName string  `json:"provider_name"`
	RequestModel string  `json:"request_model"`
	InputTokens  float64 `json:"input_tokens"`
	OutputTokens float64 `json:"output_tokens"`
	CurrentCost  float64 `json:"current_cost"`
	TargetCost   float64 `json:"target_cost"`
}

// QueryWhatIf reprices each model's observed token volumes at the target
// (provider, model) rates.
func (s *Store) QueryWhatIf(ctx context.Context, from, to time.Time, f Filter, targetProvider, targetModel string) ([]WhatIfRow, error) {
	target, ok := pricing.Lookup(targetProvider, targetModel)
	if !ok {
		return nil, fmt.Errorf("no pricing for %s/%s", targetProvider, targetModel)
	}
	breakdown, err := s.QueryCostBreakdown(ctx, from, to, f)
	if err != nil {
		return nil, err
	}
	byModel := AggregateCostsByModel(breakdown)

	result := make([]WhatIfRow, 0, len(byModel))
	for _, r := range byModel {
		targetCost := target.TokenCost("input", r.InputTokens) +
			target.TokenCost("output", r.OutputTokens) +
			target.TokenCost("cache_read", r.CacheReadTokens) +
			target.TokenCost("cache_creation", r.CacheCreationTokens)
		result = append(result, WhatIfRow{
			ProviderName: r.ProviderName,
			RequestModel: r.RequestModel,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			CurrentCost:  r.TotalCost,
			TargetCost:   targetCost,
		})
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
