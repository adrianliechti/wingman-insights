package mcpserver

import (
	"context"
	"strings"
	"time"

	"insights/internal/store"
)

type AdoptionPoint struct {
	Time           string `json:"time"`
	ActiveUsers    int64  `json:"active_users"`
	NewUsers       int64  `json:"new_users"`
	ReturningUsers int64  `json:"returning_users"`
	Requests       int64  `json:"requests"`
}

type AdoptionData struct {
	Totals    store.AdoptionSummary     `json:"totals"`
	Points    []AdoptionPoint           `json:"points"`
	Frequency []store.AdoptionFrequency `json:"frequency"`
}

type PromptSizeTotals struct {
	Requests         int64   `json:"requests"`
	CostUSD          float64 `json:"cost_usd"`
	UnpricedRequests int64   `json:"unpriced_requests"`
	UnpricedTokens   int64   `json:"unpriced_tokens"`
}

type PromptSizeBin struct {
	Label             string   `json:"label"`
	MinInputTokens    int64    `json:"min_input_tokens"`
	MaxInputTokens    *int64   `json:"max_input_tokens,omitempty" jsonschema:"Omitted for the open-ended last bin."`
	Requests          int64    `json:"requests"`
	CostUSD           float64  `json:"cost_usd"`
	UnpricedRequests  int64    `json:"unpriced_requests"`
	RequestPercent    *float64 `json:"request_percent,omitempty"`
	CostPercent       *float64 `json:"cost_percent,omitempty"`
	CostPerRequestUSD *float64 `json:"cost_per_request_usd,omitempty"`
}

type PromptSizeData struct {
	Totals PromptSizeTotals `json:"totals"`
	Bins   []PromptSizeBin  `json:"bins"`
}

func (t tools) adoption(ctx context.Context, in UsageInput) (Response[UsageData], error) {
	q, from, to, f, err := t.query(in.RangeFilters, in.ModelFilters)
	if err != nil {
		return Response[UsageData]{}, err
	}
	iv, err := resolveInterval(in.Interval, false, from, to)
	if err != nil {
		return Response[UsageData]{}, err
	}
	report, err := t.store.QueryAdoption(ctx, from, to, iv, f)
	if err != nil {
		return Response[UsageData]{}, err
	}
	if len(report.Buckets) > maxPoints {
		return Response[UsageData]{}, inputError("adoption exceeds 2000 buckets; use auto or day, or narrow the range")
	}
	data := AdoptionData{Totals: report.Summary, Points: []AdoptionPoint{}, Frequency: report.Frequency}
	for _, b := range report.Buckets {
		data.Points = append(data.Points, AdoptionPoint{Time: b.Bucket.UTC().Format(time.RFC3339), ActiveUsers: b.ActiveUsers, NewUsers: b.NewUsers, ReturningUsers: b.ReturningUsers, Requests: b.Requests})
	}
	q.View, q.Interval = "adoption", strings.TrimPrefix(iv, "1 ")
	out := Response[UsageData]{Query: q, Warnings: []string{}, Data: UsageData{Adoption: &data}}
	if report.Summary.ActiveUsers == 0 {
		out.Warnings = append(out.Warnings, "No matching users.")
	} else {
		out.Warnings = append(out.Warnings, bucketWarnings(from, to, iv)...)
	}
	return out, nil
}

func (t tools) promptSizes(ctx context.Context, in UsageInput) (Response[UsageData], error) {
	q, from, to, f, err := t.query(in.RangeFilters, in.ModelFilters)
	if err != nil {
		return Response[UsageData]{}, err
	}
	rows, err := t.store.QueryContextHistogram(ctx, from, to, f)
	if err != nil {
		return Response[UsageData]{}, err
	}
	var totals PromptSizeTotals
	var cost float64
	for _, r := range rows {
		totals.Requests += r.Requests
		totals.UnpricedRequests += r.UnpricedRequests
		totals.UnpricedTokens += r.UnpricedTokens
		cost += r.Cost
	}
	totals.CostUSD = usd(cost)
	data := PromptSizeData{Totals: totals, Bins: []PromptSizeBin{}}
	for _, r := range rows {
		data.Bins = append(data.Bins, PromptSizeBin{Label: r.Bucket, MinInputTokens: r.MinInputTokens, MaxInputTokens: r.MaxInputTokens,
			Requests: r.Requests, CostUSD: usd(r.Cost), UnpricedRequests: r.UnpricedRequests,
			RequestPercent: percent(float64(r.Requests), float64(totals.Requests)), CostPercent: percent(r.Cost, cost), CostPerRequestUSD: ratioUSD(r.Cost, float64(r.Requests))})
	}
	q.GroupBy = "prompt_size"
	out := Response[UsageData]{Query: q, Warnings: []string{}, Data: UsageData{PromptSizes: &data}}
	if totals.UnpricedRequests > 0 {
		out.Warnings = append(out.Warnings, "Some calls are unpriced; costs and cost_percent are incomplete.")
	}
	if totals.Requests == 0 {
		out.Warnings = append(out.Warnings, "No matching provider calls with input tokens.")
	}
	return out, nil
}
