package mcpserver

import (
	"context"
	"strconv"
	"strings"
	"time"

	"insights/internal/store"
)

type AdoptionInput struct {
	RangeFilters
	ModelFilters
	Interval    string `json:"interval,omitempty" jsonschema:"auto (default), hour, or day. New/returning users use first observed token-bearing provider activity in retained history under the same filters, not signup date."`
	ChartMetric string `json:"chart_metric,omitempty" jsonschema:"active_users (default), new_vs_returning_users, new_users, returning_users, requests, or active_days. active_days charts the number of users active on 1, 2, ... distinct UTC days in the selected window. Admin only; bucket distinct-user counts are not additive."`
}

type PromptSizeInput struct {
	RangeFilters
	ModelFilters
	ChartMetric string `json:"chart_metric,omitempty" jsonschema:"requests (default), cost_usd, request_share_percent, or cost_share_percent. Shares are 0 to 100 across all prompt-size bins. Costs are stored estimates; unpriced calls remain counted. Prompt size is inclusive input tokens, including cache reads and creation."`
}

type PromptSizeRow struct {
	store.ContextBucketRow
	RequestSharePercent *float64 `json:"request_share_percent"`
	CostSharePercent    *float64 `json:"cost_share_percent"`
	CostPerRequest      *float64 `json:"cost_per_request_usd"`
}

type PromptSizeData struct {
	Requests         int64           `json:"requests"`
	Cost             float64         `json:"cost_usd"`
	UnpricedRequests int64           `json:"unpriced_requests"`
	UnpricedTokens   int64           `json:"unpriced_tokens"`
	Rows             []PromptSizeRow `json:"rows"`
}

// Ordered distributions keep their natural numeric order rather than ranking
// bins by popularity or collapsing them into Other.
func distributionChart(title, xLabel, metric string, notes []string) Chart {
	return Chart{Title: title, Type: "bar", XAxis: ChartAxis{Label: xLabel, Type: "nominal", Unit: "none"}, YAxis: measure(metric), Rows: []ChartRow{}, MissingData: "not_applicable", Notes: append([]string{}, notes...)}
}

func (t tools) adoption(ctx context.Context, in AdoptionInput) (Response[store.AdoptionReport], error) {
	var out Response[store.AdoptionReport]
	q, from, to, f, err := t.query(ctx, in.RangeFilters, in.ModelFilters)
	if err != nil {
		return out, err
	}
	metric, err := chartMetric(in.ChartMetric, "active_users", "active_users", "new_vs_returning_users", "new_users", "returning_users", "requests", "active_days")
	if err != nil {
		return out, err
	}
	iv, err := chartInterval(in.Interval, false, from, to, 2)
	if err != nil {
		return out, err
	}
	data, err := t.store.QueryAdoption(ctx, from, to, iv, f)
	if err != nil {
		return out, err
	}
	if len(data.Buckets) > maxPoints {
		return out, inputError("adoption exceeds 2000 buckets; use auto or day, or narrow the range")
	}
	q.Interval = strings.TrimPrefix(iv, "1 ")
	q.Warnings = append(q.Warnings, "New means first observed activity in retained history under the selected filters, not registration. One active day in this window does not prove a user never returns.", "Counts cover identified token-bearing provider calls; anonymous calls and directory identities known to be applications are excluded. Unknown identity kinds may remain. Window active-user totals are distinct; do not sum bucket active-user counts.")
	if data.Summary.ActiveUsers == 0 {
		q.Warnings = append(q.Warnings, "No matching observed users.")
	}
	out.Data, out.Query = data, q
	if metric == "active_days" {
		out.Chart = distributionChart("Frequency of observed AI use", "Active UTC days", "active_users", q.Warnings)
		for _, r := range data.Frequency {
			v := float64(r.Users)
			x := strconv.FormatInt(r.ActiveDays, 10)
			out.Chart.Rows = append(out.Chart.Rows, ChartRow{X: x, XLabel: x, Series: "users", SeriesLabel: "Observed users", Value: &v})
		}
		out.TotalRows = len(data.Frequency)
		return out, nil
	}
	values := []chartValue{}
	for _, b := range data.Buckets {
		x := b.Bucket.UTC().Format(time.RFC3339)
		add := func(id, label string, n int64) {
			values = append(values, chartValue{x: x, xLabel: x, series: id, seriesLabel: label, value: float64(n)})
		}
		switch metric {
		case "requests":
			add("requests", "Identified provider calls", b.Requests)
		case "new_users":
			add("new_users", "New observed users", b.NewUsers)
		case "returning_users":
			add("returning_users", "Returning observed users", b.ReturningUsers)
		case "new_vs_returning_users":
			add("new_users", "New observed users", b.NewUsers)
			add("returning_users", "Returning observed users", b.ReturningUsers)
		default:
			add("active_users", "Active observed users", b.ActiveUsers)
		}
	}
	out.TotalRows = len(data.Buckets)
	out.Chart, err = makeChart("AI adoption over time", "line", "Time (UTC)", metric, values, 2, true, from, to, iv, false, q.Warnings)
	return out, err
}

func (t tools) promptSizes(ctx context.Context, in PromptSizeInput) (Response[PromptSizeData], error) {
	var out Response[PromptSizeData]
	q, from, to, f, err := t.query(ctx, in.RangeFilters, in.ModelFilters)
	if err != nil {
		return out, err
	}
	metric, err := chartMetric(in.ChartMetric, "requests", "requests", "cost_usd", "request_share_percent", "cost_share_percent")
	if err != nil {
		return out, err
	}
	rows, err := t.store.QueryContextHistogram(ctx, from, to, f)
	if err != nil {
		return out, err
	}
	out.Data.Rows = []PromptSizeRow{}
	for _, r := range rows {
		out.Data.Requests += r.Requests
		out.Data.Cost += r.Cost
		out.Data.UnpricedRequests += r.UnpricedRequests
		out.Data.UnpricedTokens += r.UnpricedTokens
	}
	q.Warnings = append(q.Warnings, "Prompt size includes cached input. Only provider calls with positive input tokens are included; output-only calls are excluded. Calls are not conversations. Cost share uses known stored cost, not a complete provider invoice.")
	if out.Data.UnpricedRequests > 0 {
		q.Warnings = append(q.Warnings, "Some calls are unpriced; cost estimates and cost shares may be incomplete.")
	}
	if out.Data.Requests == 0 {
		q.Warnings = append(q.Warnings, "No matching recorded provider calls with input tokens.")
	}
	out.Chart = distributionChart("Provider usage by prompt size", "Inclusive input tokens", metric, q.Warnings)
	for i, r := range rows {
		p := PromptSizeRow{ContextBucketRow: r, RequestSharePercent: ratio(100*float64(r.Requests), float64(out.Data.Requests)), CostSharePercent: ratio(100*r.Cost, out.Data.Cost), CostPerRequest: ratio(r.Cost, float64(r.Requests))}
		out.Data.Rows = append(out.Data.Rows, p)
		if out.Data.Requests == 0 {
			continue
		}
		v := float64(r.Requests)
		var value *float64
		switch metric {
		case "cost_usd":
			v = r.Cost
			value = &v
		case "request_share_percent":
			value = p.RequestSharePercent
		case "cost_share_percent":
			value = p.CostSharePercent
		default:
			value = &v
		}
		out.Chart.Rows = append(out.Chart.Rows, ChartRow{X: strconv.Itoa(i), XLabel: r.Bucket, Series: metric, SeriesLabel: measure(metric).Label, Value: value})
	}
	out.Query = q
	if out.Data.Requests > 0 {
		out.TotalRows = len(rows)
	}
	return out, nil
}
