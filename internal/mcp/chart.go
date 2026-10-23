package mcpserver

import (
	"sort"
	"strings"
	"time"

	"insights/internal/store"
)

// Charts are renderer-independent tables: the client chooses its plotting
// library and styling. Stable IDs distinguish entities with identical labels.
type ChartOptions struct {
	TopN int `json:"top_n,omitempty" jsonschema:"Chart readability limit: 1 to 20, default 8. Keep the largest groups/series across the whole range and combine the remainder as Other, preserving totals. Independent of the raw data's limit."`
}

type ChartAxis struct {
	Label string `json:"label"`
	Type  string `json:"type"` // temporal, nominal, quantitative
	Unit  string `json:"unit"` // UTC, USD, tokens, prompts, percent, or none
}

type ChartRow struct {
	X           string   `json:"x"`
	XLabel      string   `json:"x_label"`
	Series      string   `json:"series"`
	SeriesLabel string   `json:"series_label"`
	Value       *float64 `json:"value"` // null is missing telemetry, not zero
}

type Chart struct {
	Title          string     `json:"title"`
	Type           string     `json:"type"` // bar, line, stacked_bar
	XAxis          ChartAxis  `json:"x_axis"`
	YAxis          ChartAxis  `json:"y_axis"`
	Rows           []ChartRow `json:"rows"`
	IncludesOther  bool       `json:"includes_other"`
	MissingData    string     `json:"missing_data"`
	PartialBuckets bool       `json:"partial_buckets"`
	Notes          []string   `json:"notes"`
}

type chartValue struct {
	x, xLabel, series, seriesLabel string
	value                          float64
}

func chartOptions(in ChartOptions) (int, error) {
	if in.TopN == 0 {
		return 8, nil
	}
	if in.TopN < 1 || in.TopN > 20 {
		return 0, inputError("top_n must be between 1 and 20")
	}
	return in.TopN, nil
}

func chartMetric(value, fallback string, allowed ...string) (string, error) {
	if value == "" {
		return fallback, nil
	}
	for _, a := range allowed {
		if a == value {
			return value, nil
		}
	}
	return "", inputError("chart_metric must be one of: " + strings.Join(allowed, ", "))
}

func chartInterval(value string, optional bool, from, to time.Time, topN int) (string, error) {
	if value == "auto" || (value == "" && !optional) {
		// Account for the full UTC grid and an extra Other series. Daily
		// buckets fit even a 90-day range with the maximum 20-series limit.
		buckets := int(to.UTC().Truncate(time.Hour).Sub(from.UTC().Truncate(time.Hour))/time.Hour) + 1
		if buckets*(topN+1) <= maxPoints {
			return "1 hour", nil
		}
		return "1 day", nil
	}
	return interval(value, optional)
}

func measure(metric string) ChartAxis {
	switch metric {
	case "total_tokens":
		return ChartAxis{Label: "Tokens", Type: "quantitative", Unit: "tokens"}
	case "cache_savings_usd":
		return ChartAxis{Label: "Estimated cache savings (USD)", Type: "quantitative", Unit: "USD"}
	case "prompts":
		return ChartAxis{Label: "Classified prompts", Type: "quantitative", Unit: "prompts"}
	case "share_percent":
		return ChartAxis{Label: "Share of classified prompts (%)", Type: "quantitative", Unit: "percent"}
	default:
		return ChartAxis{Label: "Estimated cost (USD)", Type: "quantitative", Unit: "USD"}
	}
}

// rankGroups chooses one stable set for the whole chart, never a different
// top-N per bucket. Prefixing source IDs reserves "other" for the tail.
func rankGroups(totals map[string]float64, n int) (map[string]bool, []string) {
	ids := make([]string, 0, len(totals))
	for id := range totals {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if totals[ids[i]] != totals[ids[j]] {
			return totals[ids[i]] > totals[ids[j]]
		}
		return ids[i] < ids[j]
	})
	keep := map[string]bool{}
	for i, id := range ids {
		if i < n {
			keep[id] = true
		}
	}
	return keep, ids
}

func foldChart(values []chartValue, n int, foldX bool) ([]chartValue, bool) {
	totals := map[string]float64{}
	for _, p := range values {
		id := p.series
		if foldX {
			id = p.x
		}
		totals[id] += p.value
	}
	keep, ids := rankGroups(totals, n)
	if len(ids) <= n {
		return values, false
	}
	type key struct{ x, series string }
	byKey := map[key]chartValue{}
	for _, p := range values {
		if foldX && !keep[p.x] {
			p.x, p.xLabel = "other", "Other (remaining)"
		}
		if !foldX && !keep[p.series] {
			p.series, p.seriesLabel = "other", "Other (remaining)"
		}
		k := key{p.x, p.series}
		if prev, ok := byKey[k]; ok {
			p.value += prev.value
		}
		byKey[k] = p
	}
	out := make([]chartValue, 0, len(byKey))
	for _, p := range byKey {
		out = append(out, p)
	}
	return out, true
}

func makeChart(title, kind, xLabel, metric string, values []chartValue, topN int, temporal bool, from, to time.Time, iv string, percentByX bool, notes []string) (Chart, error) {
	c := Chart{Title: title, Type: kind, XAxis: ChartAxis{Label: xLabel, Type: "nominal", Unit: "none"}, YAxis: measure(metric), Rows: []ChartRow{}, MissingData: "not_applicable", Notes: append([]string{}, notes...)}
	var folded bool
	if !temporal {
		values, folded = foldChart(values, topN, true)
		c.IncludesOther = folded
	}
	if temporal || kind == "stacked_bar" {
		values, folded = foldChart(values, topN, false)
		c.IncludesOther = c.IncludesOther || folded
	}
	if c.IncludesOther {
		c.Notes = append(c.Notes, "Other (remaining) combines all groups outside the top N; chart totals include the full result, independently of raw row truncation.")
	}
	if metric == "share_percent" {
		totals := map[string]float64{}
		for _, p := range values {
			key := "all"
			if percentByX {
				key = p.x
			}
			totals[key] += p.value
		}
		for i := range values {
			key := "all"
			if percentByX {
				key = values[i].x
			}
			if totals[key] > 0 {
				values[i].value = 100 * values[i].value / totals[key]
			}
		}
	}
	if temporal {
		c.XAxis = ChartAxis{Label: "Time (UTC)", Type: "temporal", Unit: "UTC"}
		c.MissingData = "null"
		c.Notes = append(c.Notes, "Null is missing telemetry. Keep gaps in lines; do not replace them with zero. First and last buckets may cover only part of their interval.")
		step := time.Hour
		if iv == "1 day" {
			step = 24 * time.Hour
		}
		start, end := from.UTC().Truncate(step), to.UTC().Truncate(step)
		c.PartialBuckets = !from.Equal(start) || !to.Add(time.Nanosecond).Equal(end.Add(step))
		labels := map[string]string{}
		totals := map[string]float64{}
		type key struct{ x, series string }
		lookup := map[key]float64{}
		for _, p := range values {
			labels[p.series] = p.seriesLabel
			totals[p.series] += p.value
			lookup[key{p.x, p.series}] = p.value
		}
		_, ids := rankGroups(totals, len(totals))
		count := int(end.Sub(start)/step) + 1
		if count*len(ids) > maxPoints {
			return c, inputError("chart exceeds 2000 aligned points; use interval=auto or day, lower top_n, or narrow the range")
		}
		for ts := start; !ts.After(end); ts = ts.Add(step) {
			x := ts.Format(time.RFC3339)
			for _, id := range ids {
				var value *float64
				if v, ok := lookup[key{x, id}]; ok {
					value = &v
				}
				c.Rows = append(c.Rows, ChartRow{X: x, XLabel: x, Series: id, SeriesLabel: labels[id], Value: value})
			}
		}
		return c, nil
	}
	// Order groups by total and series by their contribution, with stable ties.
	xTotals, seriesTotals := map[string]float64{}, map[string]float64{}
	for _, p := range values {
		xTotals[p.x] += p.value
		seriesTotals[p.series] += p.value
	}
	sort.Slice(values, func(i, j int) bool {
		a, b := values[i], values[j]
		if a.x != b.x {
			if a.x == "other" {
				return false
			}
			if b.x == "other" {
				return true
			}
			if xTotals[a.x] != xTotals[b.x] {
				return xTotals[a.x] > xTotals[b.x]
			}
			return a.x < b.x
		}
		if a.series == "other" {
			return false
		}
		if b.series == "other" {
			return true
		}
		if seriesTotals[a.series] != seriesTotals[b.series] {
			return seriesTotals[a.series] > seriesTotals[b.series]
		}
		return a.series < b.series
	})
	for _, p := range values {
		v := p.value
		c.Rows = append(c.Rows, ChartRow{X: p.x, XLabel: p.xLabel, Series: p.series, SeriesLabel: p.seriesLabel, Value: &v})
	}
	return c, nil
}

func usageChart(data UsageData, q QueryInfo, metric string, topN int, from, to time.Time, iv string) (Chart, error) {
	values := []chartValue{}
	if iv != "" {
		for _, b := range data.Buckets {
			v := b.Cost
			if metric == "total_tokens" {
				v = float64(b.Tokens.Input + b.Tokens.Output + b.Tokens.Cached)
			}
			values = append(values, chartValue{x: b.Bucket, xLabel: b.Bucket, series: "model:" + b.Model, seriesLabel: display(b.Model), value: v})
		}
	} else if data.Tokens.Input+data.Tokens.Output+data.Tokens.Cached > 0 {
		if metric == "total_tokens" {
			for _, p := range []struct {
				label string
				n     int64
			}{{"Input", data.Tokens.Input}, {"Output", data.Tokens.Output}, {"Cached", data.Tokens.Cached}} {
				values = append(values, chartValue{x: "partition:" + p.label, xLabel: p.label, series: metric, seriesLabel: "Tokens", value: float64(p.n)})
			}
		} else {
			values = append(values, chartValue{x: q.Scope, xLabel: categoryLabel(q.Scope), series: metric, seriesLabel: "Estimated cost", value: data.Cost})
		}
	}
	kind := "bar"
	xLabel := "Scope"
	if metric == "total_tokens" {
		xLabel = "Token partition"
	}
	if iv != "" {
		kind = "line"
	}
	return makeChart("AI usage", kind, xLabel, metric, values, topN, iv != "", from, to, iv, false, q.Warnings)
}

func display(value string) string {
	if value == "" {
		return "Unassigned"
	}
	return value
}

func costChart(rows []store.CostRow, group, metric string, topN int, q QueryInfo) (Chart, error) {
	values := make([]chartValue, 0, len(rows))
	for _, r := range rows {
		var id, label string
		switch group {
		case "model":
			id = r.ProviderName + "/" + r.RequestModel
			label = id
		case "application":
			id = r.AppID
			label = r.AppName
		case "user":
			id = r.ID
			label = r.Name
		case "department":
			id = r.Department
			label = id
		case "location":
			id = r.Location
			label = id
		}
		if label == "" {
			label = display(id)
		}
		v := r.TotalCost
		if metric == "total_tokens" {
			v = r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheCreationTokens
		}
		if metric == "cache_savings_usd" {
			v = r.CacheSavings
		}
		values = append(values, chartValue{x: group + ":" + id, xLabel: label, series: metric, seriesLabel: measure(metric).Label, value: v})
	}
	return makeChart("AI usage by "+group, "bar", categoryLabel(group), metric, values, topN, false, time.Time{}, time.Time{}, "", false, q.Warnings)
}

func categoryChart(rows []CategoryRow, group, metric string, topN int, q QueryInfo) (Chart, error) {
	values := make([]chartValue, 0, len(rows))
	for _, r := range rows {
		p := chartValue{x: "category:" + r.Category, xLabel: r.Label, series: metric, seriesLabel: "Classified prompts", value: float64(r.Prompts)}
		if group == "department" {
			p.x, p.xLabel, p.series, p.seriesLabel = "department:"+r.Department, display(r.Department), "category:"+r.Category, r.Label
		}
		values = append(values, p)
	}
	kind, xLabel := "bar", "Category"
	if group == "department" {
		kind, xLabel = "stacked_bar", "Department"
	}
	return makeChart("AI conversation topics", kind, xLabel, metric, values, topN, false, time.Time{}, time.Time{}, "", group == "department", q.Warnings)
}

func categoryTrendChart(rows []CategoryPoint, metric string, topN int, q QueryInfo, from, to time.Time, iv string) (Chart, error) {
	values := make([]chartValue, 0, len(rows))
	for _, p := range rows {
		values = append(values, chartValue{x: p.Bucket, xLabel: p.Bucket, series: "category:" + p.Category, seriesLabel: p.Label, value: float64(p.Prompts)})
	}
	return makeChart("AI conversation topics over time", "line", "Time (UTC)", metric, values, topN, true, from, to, iv, true, q.Warnings)
}
