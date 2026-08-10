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
	ID           string `json:"id,omitempty"`         // Entra object id when resolved, else raw OTel id
	Name         string `json:"name,omitempty"`       // resolved display name; empty if unresolved
	Kind         string `json:"kind,omitempty"`       // user | application; empty if unresolved
	Department   string `json:"department,omitempty"` // resolved department; empty if unresolved/unset
	Location     string `json:"location,omitempty"`   // resolved office location; empty if unresolved/unset
	Username     string `json:"username,omitempty"`   // resolved preferred username; empty if unresolved/unset
	Former       bool   `json:"former,omitempty"`     // matched a directory row no longer active (departed/deleted)
	AppID        string `json:"app_id,omitempty"`
	AppName      string `json:"app_name,omitempty"` // resolved app display name; empty if unresolved
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

	// Priced is BOOL_AND'd over every span folded into this row: false as soon
	// as one contributing span has no catalog price, even if the rest do. It
	// flags "some unpriced spend may be hiding here", not "these tokens are
	// unpriced" — use UnpricedTokens for the latter, since it stays additive
	// (a plain SUM) across any grouping instead of collapsing to all-or-nothing.
	Priced bool `json:"priced"`
	// UnpricedTokens is the inclusive input+output token count from only the
	// spans that had no models.dev price, summed regardless of grouping. Unlike
	// Priced, this composes correctly when a row mixes priced and unpriced
	// spans (e.g. a model priced only after some usage was already ingested),
	// so a token-share percentage computed from it reflects the true fraction
	// instead of charging a whole bucket's volume for one unpriced straggler.
	UnpricedTokens float64 `json:"unpriced_tokens"`
}

// QueryCostBreakdown returns priced token usage per (user, provider, model).
// Token counts are summed from the partition counters (or spans); costs are
// summed from the per-span materialized cost columns (priced once, at insert
// time), not re-priced live — the same source every other cost query reads, so
// a pricing catalog refresh can't make this page disagree with them. Base data
// for the per-user / per-model cost views and the CSV.
func (s *Store) QueryCostBreakdown(ctx context.Context, from, to time.Time, f Filter) ([]CostRow, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	r := dirResolve("genai_spans", "user_id", "user_email")
	a := dirResolveApp("genai_spans", "app_id")
	// Resolve user_id/user_email and app_id to canonical identities via the
	// directory table (matched identity first, else raw email, else raw id — see
	// dirResolve), then group by the resolved principal directly in SQL — no
	// Go-side fold. An unresolved principal passes through as itself with empty
	// name/kind/department/location.
	rows, err := s.db.QueryContext(ctx, `
		WITH resolved AS (
			SELECT
				`+r.ID+` as principal,
				`+r.Name+` as name,
				`+r.Kind+` as kind,
				`+r.Dept+` as department,
				`+r.Loc+` as location,
				`+r.User+` as username,
				`+r.Former+` as former,
				`+a.ID+` as app_id,
				`+a.Name+` as app_name,
				COALESCE(provider_name, '') as provider_name,
				COALESCE(request_model, '') as request_model,
				input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
				input_cost, output_cost, cache_read_cost, cache_creation_cost, cost, cache_savings, priced
			FROM genai_spans`+r.Join+a.Join+`
			WHERE (input_tokens > 0 OR output_tokens > 0) AND genai_spans.time >= ? AND genai_spans.time <= ?`+clause+`
		)
		SELECT principal, name, kind, department, location, username, BOOL_OR(former), app_id, app_name, provider_name, request_model,`+spansPartCols+`,
			COALESCE(SUM(input_cost), 0), COALESCE(SUM(output_cost), 0),
			COALESCE(SUM(cache_read_cost), 0), COALESCE(SUM(cache_creation_cost), 0),
			COALESCE(SUM(cost), 0), COALESCE(SUM(cache_savings), 0), BOOL_AND(COALESCE(priced, false)),
			COALESCE(SUM(CASE WHEN COALESCE(priced, false) THEN 0 ELSE input_tokens + output_tokens END), 0)
		FROM resolved
		GROUP BY principal, name, kind, department, location, username, app_id, app_name, provider_name, request_model
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CostRow
	for rows.Next() {
		var id, name, kind, department, location, username, appID, appName, provider, model string
		var p tokenParts
		var r CostRow
		if err := rows.Scan(&id, &name, &kind, &department, &location, &username, &r.Former, &appID, &appName, &provider, &model,
			&p.Uncached, &p.CacheRead, &p.CacheWrite, &p.Response, &p.Reasoning,
			&r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheCreationCost, &r.TotalCost, &r.CacheSavings, &r.Priced,
			&r.UnpricedTokens); err != nil {
			return nil, err
		}
		r.ID, r.Name, r.Kind, r.Department, r.Location, r.Username = id, name, kind, department, location, username
		r.AppID, r.AppName, r.ProviderName, r.RequestModel = appID, appName, provider, model
		r.InputTokens = p.Uncached // billed (non-cached) input
		r.OutputTokens = p.Response + p.Reasoning
		r.CacheReadTokens = p.CacheRead
		r.CacheCreationTokens = p.CacheWrite
		r.ReasoningTokens = p.Reasoning
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortCostRows(result)
	return result, nil
}

// AggregateCostsByUser collapses a cost breakdown to one row per user, keyed by
// the resolved identity id, carrying the name/kind/department/location/username/former through.
func AggregateCostsByUser(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.ID, CostRow{ID: r.ID, Name: r.Name, Kind: r.Kind, Department: r.Department, Location: r.Location, Username: r.Username, Former: r.Former}
	})
}

// AggregateCostsByModel collapses a cost breakdown to one row per provider/model.
func AggregateCostsByModel(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.ProviderName + "\x00" + r.RequestModel, CostRow{ProviderName: r.ProviderName, RequestModel: r.RequestModel}
	})
}

// AggregateCostsByApp collapses a cost breakdown to one row per app, keyed by
// the resolved app identity (service.peer.name) and carrying its display name.
func AggregateCostsByApp(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.AppID, CostRow{AppID: r.AppID, AppName: r.AppName}
	})
}

// AggregateCostsByDepartment collapses a cost breakdown to one row per
// department. Rows whose user did not resolve (or has no department) fold into a
// single empty-department bucket the UI can label "Unknown".
func AggregateCostsByDepartment(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.Department, CostRow{Department: r.Department}
	})
}

// AggregateCostsByLocation collapses a cost breakdown to one row per office
// location, with unresolved/unset locations folding into one bucket.
func AggregateCostsByLocation(rows []CostRow) []CostRow {
	return aggregateCosts(rows, func(r CostRow) (string, CostRow) {
		return r.Location, CostRow{Location: r.Location}
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
		agg.UnpricedTokens += r.UnpricedTokens
	}
	result := make([]CostRow, 0, len(byKey))
	for _, r := range byKey {
		result = append(result, *r)
	}
	sortCostRows(result)
	return result
}

// QueryCostTimeseries returns spend per time bucket, stacked by request_model
// (groupBy "model", default) or app_id (groupBy "app"). Cost is summed from the
// per-span materialized cost column (priced at insert time), the same source
// every other cost query reads — not re-priced live, so a pricing catalog
// refresh can't make this chart disagree with them. An unpriced model
// contributes 0 (its consumption stays visible via
// QueryTokenVolumeTimeseries, the FinOps "Tokens" view).
func (s *Store) QueryCostTimeseries(ctx context.Context, from, to time.Time, interval, groupBy string, f Filter) ([]TimeseriesPoint, error) {
	labelCol := "request_model"
	if groupBy == "app" {
		labelCol = "app_id"
	}
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(`+labelCol+`, '') as label,
			COALESCE(SUM(cost), 0) as value,
			COUNT(*) as count
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, label
		ORDER BY bucket
	`, args...)
}

// UsageBucketPoint is one point in the usage timeseries: cost plus the token
// breakdown for a model over one interval.
type UsageBucketPoint struct {
	Bucket time.Time
	Model  string
	Cost   float64
	Tokens TokenTotals
}

// QueryUsageTimeseries returns cost and token consumption per time bucket,
// stacked by request_model. Cost and cache savings are summed from the per-span
// materialized columns (the same source as QueryCostTimeseries); tokens come
// from the five disjoint partitions, folded by usageTokenTotals.
func (s *Store) QueryUsageTimeseries(ctx context.Context, from, to time.Time, interval string, f Filter) ([]UsageBucketPoint, error) {
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			time_bucket(CAST(? AS INTERVAL), time) as bucket,
			COALESCE(request_model, '') as model,
			COALESCE(SUM(cost), 0) as cost,
			COALESCE(SUM(cache_savings), 0) as cache_savings,
			COALESCE(BOOL_AND(COALESCE(priced, false)), true) as priced,`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause+`
		GROUP BY bucket, model
		ORDER BY bucket
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []UsageBucketPoint
	for rows.Next() {
		var p UsageBucketPoint
		var parts tokenParts
		var cacheSavings float64
		var priced bool
		if err := rows.Scan(&p.Bucket, &p.Model, &p.Cost, &cacheSavings, &priced,
			&parts.Uncached, &parts.CacheRead, &parts.CacheWrite, &parts.Response, &parts.Reasoning); err != nil {
			return nil, err
		}
		p.Tokens = usageTokenTotals(parts, cacheSavings, priced)
		result = append(result, p)
	}
	return result, rows.Err()
}

// TokenTotals is the aggregate token consumption for a window and filter, using
// the same disjoint ledger as CostRow: Input is the billed, non-cached prompt
// remainder, Cached is cache read + creation (not part of Input), Output covers
// the whole completion and Reasoning is a subset of it reported for visibility.
// Input + Output + Cached is therefore the billed total, with no double count.
type TokenTotals struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Cached    int64 `json:"cached"`
	Reasoning int64 `json:"reasoning"`

	// CacheSavings is what the cached tokens would have cost at the full input
	// rate, minus what they actually cost.
	CacheSavings float64 `json:"cache_savings"`
	// Priced is false when any contributing model has no models.dev price, so a
	// zero cost can be told apart from genuinely free usage.
	Priced bool `json:"priced"`
}

// usageTokenTotals folds the five disjoint partitions into the reported ledger:
// uncached input, all output (response + reasoning, one rate), and the two cache
// classes merged — matching the Input / Output / Cached / Reasoning columns of
// the FinOps cost tables so both surfaces agree.
func usageTokenTotals(p tokenParts, cacheSavings float64, priced bool) TokenTotals {
	return TokenTotals{
		Input:        int64(p.Uncached),
		Output:       int64(p.Response + p.Reasoning),
		Cached:       int64(p.CacheRead + p.CacheWrite),
		Reasoning:    int64(p.Reasoning),
		CacheSavings: cacheSavings,
		Priced:       priced,
	}
}

// UsageTotals is cost and token consumption for a window and filter, read in a
// single scan of genai_spans.
type UsageTotals struct {
	Cost   float64
	Tokens TokenTotals
}

// QueryUsageTotals returns the total cost and token counts for the given window
// and filter in one pass over genai_spans — the source carrying both the
// materialized cost column and the per-request cache breakdown. Returns zeroes
// (and Priced true, nothing being unpriced) when no matching spans exist.
func (s *Store) QueryUsageTotals(ctx context.Context, from, to time.Time, f Filter) (UsageTotals, error) {
	clause, fargs := f.spansClause()
	args := append([]any{from, to}, fargs...)
	var u UsageTotals
	var parts tokenParts
	var cacheSavings float64
	var priced bool
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(cost), 0),
			COALESCE(SUM(cache_savings), 0),
			COALESCE(BOOL_AND(COALESCE(priced, false)), true),`+spansPartCols+`
		FROM genai_spans
		WHERE (input_tokens > 0 OR output_tokens > 0) AND time >= ? AND time <= ?`+clause,
		args...).Scan(&u.Cost, &cacheSavings, &priced,
		&parts.Uncached, &parts.CacheRead, &parts.CacheWrite, &parts.Response, &parts.Reasoning)
	if err != nil {
		return UsageTotals{}, err
	}
	u.Tokens = usageTokenTotals(parts, cacheSavings, priced)
	return u, nil
}

// QueryTokenVolumeTimeseries returns total token volume per bucket, stacked by
// request_model (groupBy "model", default) or app_id (groupBy "app").
// Unlike QueryCostTimeseries this is consumption, not spend: it includes models
// with no models.dev price, so unpriced usage stays visible. Volume is the
// inclusive input + output total (cache and reasoning are subsets, not added
// again).
func (s *Store) QueryTokenVolumeTimeseries(ctx context.Context, from, to time.Time, interval, groupBy string, f Filter) ([]TimeseriesPoint, error) {
	labelCol := "request_model"
	if groupBy == "app" {
		labelCol = "app_id"
	}
	clause, fargs := f.spansClause()
	args := append([]any{interval, from, to}, fargs...)
	return s.queryTimeseries(ctx, `
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
}

// backfillSpanCost fills the per-category cost columns (and their sum, cost /
// cache_savings) for rows inserted before those columns existed (NULL on every
// pre-existing row) and re-prices rows still marked unpriced, so a model added
// to the catalog after ingest gets priced retroactively on the next start
// instead of being frozen at "unpriced" forever. Priced once per distinct
// (provider, model) — a handful of UPDATEs, not one per row — using the same
// arithmetic as SpanRow.costBreakdown. Rows still not found in the catalog are
// left untouched (already 0/false from a prior run, or swept below on first
// sight), so only the genuinely unpriced slice is rescanned on later starts,
// never the whole table.
func (s *Store) backfillSpanCost(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(provider_name, ''), COALESCE(request_model, '')
		FROM genai_spans WHERE input_cost IS NULL OR priced = false`)
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
			continue // still unpriced; retried again on a later start
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE genai_spans SET
			input_cost = GREATEST(COALESCE(input_tokens,0) - COALESCE(cache_read_tokens,0) - COALESCE(cache_creation_tokens,0), 0)/1000000.0*?,
			output_cost = COALESCE(output_tokens,0)/1000000.0*?,
			cache_read_cost = COALESCE(cache_read_tokens,0)/1000000.0*?,
			cache_creation_cost = COALESCE(cache_creation_tokens,0)/1000000.0*?,
			cost = GREATEST(COALESCE(input_tokens,0) - COALESCE(cache_read_tokens,0) - COALESCE(cache_creation_tokens,0), 0)/1000000.0*?
				+ COALESCE(cache_read_tokens,0)/1000000.0*?
				+ COALESCE(cache_creation_tokens,0)/1000000.0*?
				+ COALESCE(output_tokens,0)/1000000.0*?,
			cache_savings = COALESCE(cache_read_tokens,0)/1000000.0*?,
			priced = true
			WHERE (input_cost IS NULL OR priced = false) AND COALESCE(provider_name,'') = ? AND COALESCE(request_model,'') = ?`,
			price.Input, price.Output, price.CacheRead, price.CacheWrite,
			price.Input, price.CacheRead, price.CacheWrite, price.Output,
			price.Input-price.CacheRead,
			p.provider, p.model); err != nil {
			return err
		}
	}
	// First sight of an unpriced model: make every column non-NULL so it reads
	// as "priced = false" (not retried by the IS NULL half above) rather than
	// being rescanned by that clause on every later start too.
	_, err = s.db.ExecContext(ctx, `UPDATE genai_spans SET
		cost = 0, cache_savings = 0, input_cost = 0, output_cost = 0, cache_read_cost = 0, cache_creation_cost = 0, priced = false
		WHERE input_cost IS NULL`)
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
