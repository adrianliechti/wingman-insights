package mcpserver

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"insights/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxPoints = 2000

// Embedded inputs keep filter descriptions in each tool's inferred JSON schema.
type Filters struct {
	Applications []string `json:"applications,omitempty" jsonschema:"Application IDs or directory aliases, e.g. wingman-chat. Omit for all permitted applications. Values within this field are ORed; different fields are ANDed."`
	Users        []string `json:"users,omitempty" jsonschema:"Canonical user IDs, emails, or directory aliases. Admins only choose users; for other callers this is replaced by their token identity."`
	Departments  []string `json:"departments,omitempty" jsonschema:"Department values, e.g. Engineering. Case insensitive; uses the deployment's configured department-prefix behavior. Requires directory data."`
	Locations    []string `json:"locations,omitempty" jsonschema:"Office location values, e.g. Zurich. Case insensitive; requires directory data."`
}

type RangeFilters struct {
	From string `json:"from,omitempty" jsonschema:"Inclusive range start in RFC3339, e.g. 2026-10-01T00:00:00Z. Defaults to 24 hours before to. Maximum window: 90 days."`
	To   string `json:"to,omitempty" jsonschema:"Inclusive range end in RFC3339. Defaults to the current UTC time."`
	Filters
}

type ModelFilters struct {
	Providers []string `json:"providers,omitempty" jsonschema:"Exact provider IDs, e.g. openai. Omit for all providers. Values within a field are ORed; different fields are ANDed."`
	Models    []string `json:"models,omitempty" jsonschema:"Exact upstream model IDs, e.g. gpt-5.1. Omit for all models. Unknown values produce no matching data."`
}

type UsageInput struct {
	RangeFilters
	ModelFilters
	ChartOptions
	ComparePrevious bool   `json:"compare_previous,omitempty" jsonschema:"Compare with the immediately preceding equal-length window. No boundary event is counted in both periods. Cannot be combined with compare_from/compare_to."`
	CompareFrom     string `json:"compare_from,omitempty" jsonschema:"Optional comparison start in RFC3339, e.g. last calendar month's start. Supply compare_to too; each window is limited to 90 days. Unequal windows are reported explicitly."`
	CompareTo       string `json:"compare_to,omitempty" jsonschema:"Optional inclusive comparison end in RFC3339. Supply compare_from too. For adjacent periods end before the current from to avoid overlap."`
	Interval        string `json:"interval,omitempty" jsonschema:"For a time-series chart use auto (recommended), hour, or day. Auto chooses hourly or daily buckets to fit the chart. Omit for totals only. Buckets align to UTC boundaries."`
	ChartMetric     string `json:"chart_metric,omitempty" jsonschema:"Chart measure: cost_usd (default), total_tokens, requests, cost_per_request_usd, input_tokens_per_request, output_tokens_per_request, tokens_per_request, cache_read_share_percent, reasoning_share_percent, or cache_savings_usd. Requests are token-bearing provider calls, not conversations. Averages and shares use pooled denominators; undefined values are null."`
}

type CostInput struct {
	RangeFilters
	ModelFilters
	ChartOptions
	ChartMetric string `json:"chart_metric,omitempty" jsonschema:"Chart measure: cost_usd (default), total_tokens, or cache_savings_usd. Chart groups are ranked by this measure; the original data rows remain ranked by cost."`
	GroupBy     string `json:"group_by,omitempty" jsonschema:"Grouping: model (default), application, department, location, or user. Non-admin results remain personal for every grouping."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Number of ranked rows to return: 1 to 100, default 20. Totals cover all matching rows before truncation."`
}

type CategoryInput struct {
	RangeFilters
	ChartOptions
	ChartMetric string `json:"chart_metric,omitempty" jsonschema:"Chart measure: prompts (default) or share_percent (0 to 100). Department shares use each department's classified prompt count, including Other."`
	GroupBy     string `json:"group_by,omitempty" jsonschema:"Grouping: category (default) for overall shares, or department for category shares within each department."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Number of ranked category rows to return: 1 to 100, default 20. Shares and total prompts are computed before truncation."`
}

type CategoryTrendInput struct {
	RangeFilters
	ChartOptions
	ChartMetric string `json:"chart_metric,omitempty" jsonschema:"Chart measure: prompts (default) or share_percent (0 to 100 within each time bucket). Counts describe classified prompts only."`
	Interval    string `json:"interval,omitempty" jsonschema:"Trend interval: auto (default, recommended for charts), hour, or day. Buckets align to UTC boundaries. At most 2000 topic/bucket points; use day or narrow the time range and filters if exceeded."`
}

type AppliedFilters struct {
	Filters
	Providers        []string `json:"providers,omitempty"`
	Models           []string `json:"models,omitempty"`
	DepartmentPrefix bool     `json:"department_prefix"`
}

type QueryInfo struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	Scope    string         `json:"scope"`
	Filters  AppliedFilters `json:"filters"`
	Interval string         `json:"interval,omitempty"`
	Warnings []string       `json:"warnings"`
}

type Response[T any] struct {
	Query     QueryInfo `json:"query"`
	Data      T         `json:"data"`
	TotalRows int       `json:"total_rows"`
	Truncated bool      `json:"truncated"`
	Chart     Chart     `json:"chart"`
}

type UsageBucket struct {
	Bucket     string            `json:"bucket"`
	Model      string            `json:"model"`
	Cost       float64           `json:"cost_usd"`
	Tokens     store.TokenTotals `json:"tokens"`
	Statistics UsageStatistics   `json:"statistics"`
}

type UsageData struct {
	Cost       float64           `json:"cost_usd"`
	Tokens     store.TokenTotals `json:"tokens"`
	Buckets    []UsageBucket     `json:"buckets"`
	Statistics UsageStatistics   `json:"statistics"`
	Comparison *UsageComparison  `json:"comparison,omitempty"`
}

type CostData struct {
	Currency       string          `json:"currency"`
	GroupBy        string          `json:"group_by"`
	TotalCost      float64         `json:"total_cost_usd"`
	UnpricedTokens float64         `json:"unpriced_tokens"`
	Rows           []store.CostRow `json:"rows"`
}

type CategoryRow struct {
	Category   string  `json:"category"`
	Label      string  `json:"label"`
	Department string  `json:"department"`
	Prompts    int64   `json:"prompts"`
	Share      float64 `json:"share"`
}

type CategoryData struct {
	GroupBy      string        `json:"group_by"`
	TotalPrompts int64         `json:"total_prompts"`
	Rows         []CategoryRow `json:"rows"`
}

type CategoryPoint struct {
	Bucket   string `json:"bucket"`
	Category string `json:"category"`
	Label    string `json:"label"`
	Prompts  int64  `json:"prompts"`
}

type tools struct{ store *store.Store }

// A distinct error type prevents database details from reaching the client.
type inputError string

func (e inputError) Error() string { return string(e) }

func addTool[In, Out any](server *mcp.Server, name, description string, adminOnly bool, fn func(context.Context, In) (Out, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (_ *mcp.CallToolResult, out Out, err error) {
			p, ok := ctx.Value(principalKey{}).(principal)
			if !ok || p.user == "" {
				return nil, out, errors.New("caller identity required")
			}
			if adminOnly && !p.admin {
				return nil, out, errors.New("admin group membership required")
			}
			started := time.Now()
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			out, err = fn(ctx, in)
			log.Printf("mcp: user=%q tool=%s duration=%s success=%t", p.user, name, time.Since(started), err == nil)
			if err != nil {
				var badInput inputError
				if !errors.As(err, &badInput) {
					if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
						err = errors.New("query cancelled or timed out; narrow the range or filters")
					} else {
						log.Printf("mcp: tool=%s query failed: %v", name, err)
						err = errors.New("internal query error")
					}
				}
			}
			return nil, out, err
		})
}

func (t tools) query(ctx context.Context, in RangeFilters, models ModelFilters) (QueryInfo, time.Time, time.Time, store.Filter, error) {
	q := QueryInfo{Warnings: []string{}}
	to := time.Now().UTC()
	var err error
	if in.To != "" {
		to, err = time.Parse(time.RFC3339, in.To)
		if err != nil {
			return q, time.Time{}, time.Time{}, store.Filter{}, inputError("to must be RFC3339")
		}
	}
	from := to.Add(-24 * time.Hour)
	if in.From != "" {
		from, err = time.Parse(time.RFC3339, in.From)
		if err != nil {
			return q, from, to, store.Filter{}, inputError("from must be RFC3339")
		}
	}
	if !from.Before(to) || to.Sub(from) > 90*24*time.Hour {
		return q, from, to, store.Filter{}, inputError("from must precede to and the window must not exceed 90 days")
	}
	f := store.Filter{App: in.Applications, User: in.Users, Department: in.Departments, Location: in.Locations, Provider: models.Providers, Models: models.Models, DeptPrefix: t.store.DepartmentPrefix()}
	for _, values := range [][]string{f.App, f.User, f.Department, f.Location, f.Provider, f.Models} {
		if len(values) > 100 {
			return q, from, to, f, inputError("each filter accepts at most 100 values")
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return q, from, to, f, inputError("filter values must not be empty")
			}
		}
	}
	p, _ := ctx.Value(principalKey{}).(principal)
	q.Scope = "organization"
	if !p.admin {
		f.User = []string{p.user}
		q.Scope = "personal"
	}
	q.From, q.To = from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)
	q.Filters = AppliedFilters{Filters: Filters{Applications: f.App, Users: f.User, Departments: f.Department, Locations: f.Location}, Providers: f.Provider, Models: f.Models, DepartmentPrefix: f.DeptPrefix}
	return q, from, to, f, nil
}

func interval(value string, optional bool) (string, error) {
	if value == "" && optional {
		return "", nil
	}
	switch value {
	case "", "hour":
		return "1 hour", nil
	case "day":
		return "1 day", nil
	default:
		return "", inputError("interval must be hour or day")
	}
}

func rowLimit(n int) (int, error) {
	if n == 0 {
		return 20, nil
	}
	if n < 1 || n > 100 {
		return 0, inputError("limit must be between 1 and 100")
	}
	return n, nil
}

func (t tools) usage(ctx context.Context, in UsageInput) (Response[UsageData], error) {
	var out Response[UsageData]
	q, from, to, f, err := t.query(ctx, in.RangeFilters, in.ModelFilters)
	if err != nil {
		return out, err
	}
	topN, err := chartOptions(in.ChartOptions)
	if err != nil {
		return out, err
	}
	metric, err := chartMetric(in.ChartMetric, "cost_usd", usageMetrics...)
	if err != nil {
		return out, err
	}
	iv, err := chartInterval(in.Interval, true, from, to, topN)
	if err != nil {
		return out, err
	}
	compareFrom, compareTo, compare, err := comparisonRange(in, from, to)
	if err != nil {
		return out, err
	}
	var totals store.UsageTotals
	out.Data = UsageData{Tokens: store.TokenTotals{Priced: true}, Buckets: []UsageBucket{}}
	if iv == "" {
		u, err := t.store.QueryUsageTotals(ctx, from, to, f)
		if err != nil {
			return out, err
		}
		out.Data.Cost, out.Data.Tokens = u.Cost, u.Tokens
		totals = u
	} else {
		q.Interval = strings.TrimPrefix(iv, "1 ")
		points, err := t.store.QueryUsageTimeseries(ctx, from, to, iv, f)
		if err != nil {
			return out, err
		}
		if len(points) > maxPoints {
			return out, inputError("trend exceeds 2000 points; use day or narrow the range and filters")
		}
		for _, p := range points {
			out.Data.Buckets = append(out.Data.Buckets, UsageBucket{Bucket: p.Bucket.UTC().Format(time.RFC3339), Model: p.Model, Cost: p.Cost, Tokens: p.Tokens, Statistics: usageStatistics(store.UsageTotals{Cost: p.Cost, Tokens: p.Tokens, Requests: p.Requests, CacheReadTokens: p.CacheReadTokens, CacheCreationTokens: p.CacheCreationTokens, UnpricedTokens: p.UnpricedTokens})})
			totals.Requests += p.Requests
			totals.CacheReadTokens += p.CacheReadTokens
			totals.CacheCreationTokens += p.CacheCreationTokens
			totals.UnpricedTokens += p.UnpricedTokens
			// Derive totals from the same snapshot as the trend, matching the
			// personal API even while telemetry is being ingested.
			out.Data.Cost += p.Cost
			out.Data.Tokens.Input += p.Tokens.Input
			out.Data.Tokens.Output += p.Tokens.Output
			out.Data.Tokens.Cached += p.Tokens.Cached
			out.Data.Tokens.Reasoning += p.Tokens.Reasoning
			out.Data.Tokens.CacheSavings += p.Tokens.CacheSavings
			out.Data.Tokens.Priced = out.Data.Tokens.Priced && p.Tokens.Priced
		}
		sort.Slice(out.Data.Buckets, func(i, j int) bool {
			a, b := out.Data.Buckets[i], out.Data.Buckets[j]
			if a.Bucket != b.Bucket {
				return a.Bucket < b.Bucket
			}
			return a.Model < b.Model
		})
	}
	totals.Cost, totals.Tokens = out.Data.Cost, out.Data.Tokens
	out.Data.Statistics = usageStatistics(totals)
	if compare {
		previous, err := t.store.QueryUsageTotals(ctx, compareFrom, compareTo, f)
		if err != nil {
			return out, err
		}
		out.Data.Comparison = compareUsage(totals, previous, compareFrom, compareTo, metric)
		if !previous.Tokens.Priced {
			q.Warnings = append(q.Warnings, "Comparison-period cost is incomplete because some usage has no catalog price.")
		}
		if to.Sub(from) != compareTo.Sub(compareFrom) && !in.ComparePrevious {
			warning := "Comparison windows have different lengths; changes in totals are not normalized by duration."
			q.Warnings = append(q.Warnings, warning)
			out.Data.Comparison.Chart.Notes = append(out.Data.Comparison.Chart.Notes, warning)
		}
	}
	if !out.Data.Tokens.Priced {
		q.Warnings = append(q.Warnings, "Some usage has no catalog price; estimated cost is incomplete.")
	}
	if out.Data.Tokens.Input+out.Data.Tokens.Output+out.Data.Tokens.Cached == 0 {
		q.Warnings = append(q.Warnings, "No matching recorded usage.")
	}
	out.Query, out.TotalRows = q, len(out.Data.Buckets)
	out.Chart, err = usageChart(out.Data, q, metric, topN, from, to, iv)
	return out, err
}

func (t tools) costs(ctx context.Context, in CostInput) (Response[CostData], error) {
	var out Response[CostData]
	q, from, to, f, err := t.query(ctx, in.RangeFilters, in.ModelFilters)
	if err != nil {
		return out, err
	}
	topN, err := chartOptions(in.ChartOptions)
	if err != nil {
		return out, err
	}
	metric, err := chartMetric(in.ChartMetric, "cost_usd", "cost_usd", "total_tokens", "cache_savings_usd")
	if err != nil {
		return out, err
	}
	limit, err := rowLimit(in.Limit)
	if err != nil {
		return out, err
	}
	group := in.GroupBy
	if group == "" {
		group = "model"
	}
	aggregators := map[string]func([]store.CostRow) []store.CostRow{
		"model": store.AggregateCostsByModel, "application": store.AggregateCostsByApp,
		"department": store.AggregateCostsByDepartment, "location": store.AggregateCostsByLocation, "user": store.AggregateCostsByUser,
	}
	aggregate, ok := aggregators[group]
	if !ok {
		return out, inputError("group_by must be model, application, department, location, or user")
	}
	rows, err := t.store.QueryCostBreakdown(ctx, from, to, f)
	if err != nil {
		return out, err
	}
	out.Data = CostData{Currency: "USD", GroupBy: group, Rows: aggregate(rows)}
	priced := true
	for _, r := range rows {
		out.Data.TotalCost += r.TotalCost
		out.Data.UnpricedTokens += r.UnpricedTokens
		priced = priced && r.Priced
	}
	// Existing aggregators sort by spend; add a deterministic key for ties.
	sort.Slice(out.Data.Rows, func(i, j int) bool {
		a, b := out.Data.Rows[i], out.Data.Rows[j]
		if a.TotalCost != b.TotalCost {
			return a.TotalCost > b.TotalCost
		}
		key := func(r store.CostRow) string {
			return strings.Join([]string{r.ProviderName, r.RequestModel, r.AppID, r.Department, r.Location, r.ID}, "\x00")
		}
		return key(a) < key(b)
	})
	out.TotalRows = len(out.Data.Rows)
	chartRows := out.Data.Rows
	if len(out.Data.Rows) > limit {
		out.Data.Rows = out.Data.Rows[:limit]
		out.Truncated = true
	}
	if !priced {
		q.Warnings = append(q.Warnings, "Some usage has no catalog price; estimated cost is incomplete.")
	}
	if out.TotalRows == 0 {
		q.Warnings = append(q.Warnings, "No matching recorded usage.")
	}
	out.Query = q
	out.Chart, err = costChart(chartRows, group, metric, topN, q)
	return out, err
}

func categoryLabel(id string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(id))
	for i, w := range words {
		r := []rune(w)
		if len(r) > 0 {
			words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
		}
	}
	return strings.Join(words, " ")
}

func (t tools) categories(ctx context.Context, in CategoryInput) (Response[CategoryData], error) {
	var out Response[CategoryData]
	q, from, to, f, err := t.query(ctx, in.RangeFilters, ModelFilters{})
	if err != nil {
		return out, err
	}
	topN, err := chartOptions(in.ChartOptions)
	if err != nil {
		return out, err
	}
	metric, err := chartMetric(in.ChartMetric, "prompts", "prompts", "share_percent")
	if err != nil {
		return out, err
	}
	limit, err := rowLimit(in.Limit)
	if err != nil {
		return out, err
	}
	group := in.GroupBy
	if group == "" {
		group = "category"
	}
	if group != "category" && group != "department" {
		return out, inputError("group_by must be category or department")
	}
	out.Data = CategoryData{GroupBy: group, Rows: []CategoryRow{}}
	denominators := map[string]int64{}
	if group == "category" {
		rows, err := t.store.QueryClassificationSummary(ctx, from, to, f)
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			if r.Kind == "category" && r.Matched > 0 {
				out.Data.Rows = append(out.Data.Rows, CategoryRow{Category: r.ID, Label: categoryLabel(r.ID), Prompts: r.Matched})
				denominators[""] += r.Matched
			}
		}
	} else {
		rows, err := t.store.QueryClassificationDepartmentTopics(ctx, from, to, f)
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			if r.Prompts > 0 {
				out.Data.Rows = append(out.Data.Rows, CategoryRow{Category: r.Category, Label: categoryLabel(r.Category), Department: r.Department, Prompts: r.Prompts})
				denominators[r.Department] += r.Prompts
			}
		}
	}
	for i := range out.Data.Rows {
		r := &out.Data.Rows[i]
		r.Share = float64(r.Prompts) / float64(denominators[r.Department])
		out.Data.TotalPrompts += r.Prompts
	}
	sort.Slice(out.Data.Rows, func(i, j int) bool {
		a, b := out.Data.Rows[i], out.Data.Rows[j]
		if a.Prompts != b.Prompts {
			return a.Prompts > b.Prompts
		}
		if a.Department != b.Department {
			return a.Department < b.Department
		}
		return a.Category < b.Category
	})
	out.TotalRows = len(out.Data.Rows)
	chartRows := out.Data.Rows
	if len(out.Data.Rows) > limit {
		out.Data.Rows = out.Data.Rows[:limit]
		out.Truncated = true
	}
	q.Warnings = append(q.Warnings, "Shares are fractions of classified prompts, not all platform requests. No category costs are inferred.")
	if out.TotalRows == 0 {
		q.Warnings = append(q.Warnings, "No matching recorded categories.")
	}
	out.Query = q
	out.Chart, err = categoryChart(chartRows, group, metric, topN, q)
	return out, err
}

func (t tools) categoryTrends(ctx context.Context, in CategoryTrendInput) (Response[[]CategoryPoint], error) {
	var out Response[[]CategoryPoint]
	q, from, to, f, err := t.query(ctx, in.RangeFilters, ModelFilters{})
	if err != nil {
		return out, err
	}
	topN, err := chartOptions(in.ChartOptions)
	if err != nil {
		return out, err
	}
	metric, err := chartMetric(in.ChartMetric, "prompts", "prompts", "share_percent")
	if err != nil {
		return out, err
	}
	iv, err := chartInterval(in.Interval, false, from, to, topN)
	if err != nil {
		return out, err
	}
	points, err := t.store.QueryClassificationTopicTimeseries(ctx, from, to, iv, f)
	if err != nil {
		return out, err
	}
	if len(points) > maxPoints {
		return out, inputError("trend exceeds 2000 points; use day or narrow the range and filters")
	}
	out.Data = []CategoryPoint{}
	for _, p := range points {
		out.Data = append(out.Data, CategoryPoint{Bucket: p.Bucket.UTC().Format(time.RFC3339), Category: p.Label, Label: categoryLabel(p.Label), Prompts: p.Count})
	}
	sort.Slice(out.Data, func(i, j int) bool {
		a, b := out.Data[i], out.Data[j]
		if a.Bucket != b.Bucket {
			return a.Bucket < b.Bucket
		}
		return a.Category < b.Category
	})
	q.Interval = strings.TrimPrefix(iv, "1 ")
	q.Warnings = append(q.Warnings, "Counts cover classified prompts only. Missing buckets are not filled; absent data does not prove zero activity.")
	if len(out.Data) == 0 {
		q.Warnings = append(q.Warnings, "No matching recorded categories.")
	}
	out.Query, out.TotalRows = q, len(out.Data)
	out.Chart, err = categoryTrendChart(out.Data, metric, topN, q, from, to, iv)
	return out, err
}
