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
	ServiceName  string `json:"service_name,omitempty"`
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
			COALESCE(service_name, '') as service_name,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY user_id, service_name, provider_name, request_model
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CostRow
	for rows.Next() {
		var user, email, service, provider, model string
		var p tokenParts
		if err := rows.Scan(&user, &email, &service, &provider, &model,
			&p.Uncached, &p.CacheRead, &p.CacheWrite, &p.Response, &p.Reasoning); err != nil {
			return nil, err
		}
		price, priced := pricing.Lookup(provider, model)
		r := CostRow{
			EndUserID:           user,
			EndUserEmail:        email,
			ServiceName:         service,
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

// AggregateCostsByApp collapses a cost breakdown to one row per app (service).
func AggregateCostsByApp(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.ServiceName, CostRow{ServiceName: r.ServiceName}
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

// QueryCostTimeseries returns spend per time bucket, stacked by request_model
// (groupBy "model", default) or service_name (groupBy "app"). Priced from spans
// so cached tokens are billed at their own rate.
func (s *Store) QueryCostTimeseries(ctx context.Context, from, to time.Time, interval, groupBy string, f Filter) ([]TimeseriesPoint, error) {
	labelCol := "request_model"
	if groupBy == "app" {
		labelCol = "service_name"
	}
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(`+labelCol+`, '') as label,
			COALESCE(provider_name, '') as provider_name,
			COALESCE(request_model, '') as request_model,`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, label, provider_name, request_model
		ORDER BY bucket
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		bucket time.Time
		label  string
	}
	costs := make(map[key]float64)
	var order []key
	for rows.Next() {
		var bucket time.Time
		var label, provider, model string
		var p tokenParts
		if err := rows.Scan(&bucket, &label, &provider, &model,
			&p.Uncached, &p.CacheRead, &p.CacheWrite, &p.Response, &p.Reasoning); err != nil {
			return nil, err
		}
		price, priced := pricing.Lookup(provider, model)
		if !priced {
			// No price → no cost line. Unpriced consumption is not lost: it is
			// surfaced by QueryTokenVolumeTimeseries (the FinOps "Tokens" view).
			continue
		}
		k := key{bucket, label}
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
		result = append(result, TimeseriesPoint{Bucket: k.bucket, Label: k.label, Value: costs[k]})
	}
	return result, nil
}

// QueryTokenVolumeTimeseries returns total token volume per bucket, stacked by
// request_model (groupBy "model", default) or service_name (groupBy "app").
// Unlike QueryCostTimeseries this is consumption, not spend: it includes models
// with no models.dev price, so unpriced usage stays visible. Volume is the
// inclusive input + output total (cache and reasoning are subsets, not added
// again).
func (s *Store) QueryTokenVolumeTimeseries(ctx context.Context, from, to time.Time, interval, groupBy string, f Filter) ([]TimeseriesPoint, error) {
	labelCol := "request_model"
	if groupBy == "app" {
		labelCol = "service_name"
	}
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(`+labelCol+`, '') as label,
			COALESCE(SUM(input_tokens + output_tokens), 0) as value,
			COUNT(*) as count
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, label
		ORDER BY bucket
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TimeseriesPoint
	for rows.Next() {
		var r TimeseriesPoint
		if err := rows.Scan(&r.Bucket, &r.Label, &r.Value, &r.Count); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// backfillSpanCost fills the cost / cache_savings columns for rows inserted
// before those columns existed (NULL on every pre-existing row). It prices once
// per distinct (provider, model) — a handful of UPDATEs, not one per row, using
// the same arithmetic as pricing.Price.Cost — then zeroes anything still NULL
// (unpriced models) so the columns are never NULL afterward and this is a no-op
// on subsequent starts.
func (s *Store) backfillSpanCost(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(provider_name, ''), COALESCE(request_model, '')
		FROM genai_spans WHERE cost IS NULL`)
	if err != nil {
		return err
	}
	type pair struct{ provider, model string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.provider, &p.model); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range pairs {
		price, ok := pricing.Lookup(p.provider, p.model)
		if !ok {
			continue // swept to 0 below
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE genai_spans SET
			cost = GREATEST(COALESCE(input_tokens,0) - COALESCE(cache_read_tokens,0) - COALESCE(cache_creation_tokens,0), 0)/1000000.0*?
				+ COALESCE(cache_read_tokens,0)/1000000.0*?
				+ COALESCE(cache_creation_tokens,0)/1000000.0*?
				+ COALESCE(output_tokens,0)/1000000.0*?,
			cache_savings = COALESCE(cache_read_tokens,0)/1000000.0*?
			WHERE cost IS NULL AND COALESCE(provider_name,'') = ? AND COALESCE(request_model,'') = ?`,
			price.Input, price.CacheRead, price.CacheWrite, price.Output,
			price.Input-price.CacheRead,
			p.provider, p.model); err != nil {
			return err
		}
	}
	// Unpriced models: make the columns non-NULL so they are not retried.
	_, err = s.db.ExecContext(ctx, `UPDATE genai_spans SET cost = 0, cache_savings = 0 WHERE cost IS NULL`)
	return err
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
