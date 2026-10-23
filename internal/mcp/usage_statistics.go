package mcpserver

import (
	"time"

	"insights/internal/store"
)

var usageMetrics = []string{"cost_usd", "total_tokens", "requests", "cost_per_request_usd", "input_tokens_per_request", "output_tokens_per_request", "tokens_per_request", "cache_read_share_percent", "reasoning_share_percent", "cache_savings_usd"}

type UsageStatistics struct {
	Requests               int64    `json:"requests"`
	CacheReadTokens        int64    `json:"cache_read_tokens"`
	CacheCreationTokens    int64    `json:"cache_creation_tokens"`
	UnpricedTokens         int64    `json:"unpriced_tokens"`
	CostPerRequest         *float64 `json:"cost_per_request_usd"`
	InputTokensPerRequest  *float64 `json:"input_tokens_per_request"`
	OutputTokensPerRequest *float64 `json:"output_tokens_per_request"`
	TokensPerRequest       *float64 `json:"tokens_per_request"`
	CacheReadSharePercent  *float64 `json:"cache_read_share_percent"`
	ReasoningSharePercent  *float64 `json:"reasoning_share_percent"`
}

func ratio(n, d float64) *float64 {
	if d == 0 {
		return nil
	}
	v := n / d
	return &v
}

func usageStatistics(u store.UsageTotals) UsageStatistics {
	input := u.Tokens.Input + u.Tokens.Cached
	return UsageStatistics{Requests: u.Requests, CacheReadTokens: u.CacheReadTokens, CacheCreationTokens: u.CacheCreationTokens, UnpricedTokens: u.UnpricedTokens,
		CostPerRequest: ratio(u.Cost, float64(u.Requests)), InputTokensPerRequest: ratio(float64(input), float64(u.Requests)),
		OutputTokensPerRequest: ratio(float64(u.Tokens.Output), float64(u.Requests)), TokensPerRequest: ratio(float64(input+u.Tokens.Output), float64(u.Requests)),
		CacheReadSharePercent: ratio(100*float64(u.CacheReadTokens), float64(input)), ReasoningSharePercent: ratio(100*float64(u.Tokens.Reasoning), float64(u.Tokens.Output))}
}

type MetricChange struct {
	Current        *float64 `json:"current"`
	Previous       *float64 `json:"previous"`
	AbsoluteChange *float64 `json:"absolute_change"`
	PercentChange  *float64 `json:"percent_change"`
}

type UsageComparison struct {
	From       string                  `json:"from"`
	To         string                  `json:"to"`
	Cost       float64                 `json:"cost_usd"`
	Tokens     store.TokenTotals       `json:"tokens"`
	Statistics UsageStatistics         `json:"statistics"`
	Changes    map[string]MetricChange `json:"changes"`
	Chart      Chart                   `json:"chart"`
}

func comparisonRange(in UsageInput, from, to time.Time) (time.Time, time.Time, bool, error) {
	if in.ComparePrevious {
		if in.CompareFrom != "" || in.CompareTo != "" {
			return from, to, false, inputError("compare_previous cannot be combined with compare_from/compare_to")
		}
		return from.Add(-to.Sub(from)), from.Add(-time.Nanosecond), true, nil
	}
	if in.CompareFrom == "" && in.CompareTo == "" {
		return from, to, false, nil
	}
	a, err := time.Parse(time.RFC3339, in.CompareFrom)
	if err != nil {
		return from, to, false, inputError("compare_from and compare_to must both be RFC3339")
	}
	b, err := time.Parse(time.RFC3339, in.CompareTo)
	if err != nil {
		return from, to, false, inputError("compare_from and compare_to must both be RFC3339")
	}
	if !a.Before(b) || b.Sub(a) > 90*24*time.Hour {
		return a, b, false, inputError("comparison start must precede end and the window must not exceed 90 days")
	}
	return a, b, true, nil
}

func usageMetric(u store.UsageTotals, metric string) *float64 {
	s := usageStatistics(u)
	switch metric {
	case "requests":
		v := float64(u.Requests)
		return &v
	case "total_tokens":
		v := float64(u.Tokens.Input + u.Tokens.Output + u.Tokens.Cached)
		return &v
	case "cost_per_request_usd":
		return s.CostPerRequest
	case "input_tokens_per_request":
		return s.InputTokensPerRequest
	case "output_tokens_per_request":
		return s.OutputTokensPerRequest
	case "tokens_per_request":
		return s.TokensPerRequest
	case "cache_read_share_percent":
		return s.CacheReadSharePercent
	case "reasoning_share_percent":
		return s.ReasoningSharePercent
	case "cache_savings_usd":
		v := u.Tokens.CacheSavings
		return &v
	default:
		v := u.Cost
		return &v
	}
}

func compareUsage(current, previous store.UsageTotals, from, to time.Time, metric string) *UsageComparison {
	c := &UsageComparison{From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano), Cost: previous.Cost, Tokens: previous.Tokens, Statistics: usageStatistics(previous), Changes: map[string]MetricChange{}}
	for _, m := range usageMetrics {
		a, b := usageMetric(current, m), usageMetric(previous, m)
		delta := MetricChange{Current: a, Previous: b}
		if a != nil && b != nil {
			d := *a - *b
			delta.AbsoluteChange = &d
			delta.PercentChange = ratio(100*d, *b)
		}
		c.Changes[m] = delta
	}
	c.Chart = Chart{Title: "Usage period comparison", Type: "bar", XAxis: ChartAxis{Label: "Period", Type: "nominal", Unit: "none"}, YAxis: measure(metric), Rows: []ChartRow{}, MissingData: "null", Notes: []string{"Undefined averages and changes from a zero baseline are null. Changes describe observed activity, not proven causes. Percentage-metric absolute changes are percentage points."}}
	if !current.Tokens.Priced || !previous.Tokens.Priced {
		c.Chart.Notes = append(c.Chart.Notes, "Some usage is unpriced; cost estimates may be incomplete in either period.")
	}
	for _, p := range []struct {
		id, label string
		u         store.UsageTotals
	}{{"previous", "Previous", previous}, {"current", "Current", current}} {
		c.Chart.Rows = append(c.Chart.Rows, ChartRow{X: p.id, XLabel: p.label, Series: metric, SeriesLabel: measure(metric).Label, Value: usageMetric(p.u, metric)})
	}
	return c
}
