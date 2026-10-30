package mcpserver

import (
	"context"
	"errors"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"insights/internal/store"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxPoints = 2000

// Embedded inputs keep filter descriptions in each tool's inferred JSON schema.
type Filters struct {
	Applications []string `json:"applications,omitempty" jsonschema:"Application IDs or directory aliases, e.g. wingman-chat. Omit for all applications. Values within this field are ORed; different fields are ANDed."`
	Users        []string `json:"users,omitempty" jsonschema:"Canonical user IDs, emails, or directory aliases. Omit for all users."`
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
	View     string `json:"view,omitempty" jsonschema:"usage (default) for cost and tokens, or adoption for distinct active, new and returning users plus a distribution of active UTC days. adoption cannot be combined with group_by."`
	Interval string `json:"interval,omitempty" jsonschema:"auto, hour, or day adds time-bucketed points (per model for usage). auto picks hour for windows up to 48 hours, otherwise day. Buckets align to UTC. Omit for totals only (adoption defaults to auto). Cannot be combined with group_by."`
	GroupBy  string `json:"group_by,omitempty" jsonschema:"Rank usage by model, application, department, location, or user (sorted by cost), or prompt_size for ordered input-token size bins. Cannot be combined with interval."`
	Limit    int    `json:"limit,omitempty" jsonschema:"With group_by (except prompt_size): number of ranked groups to return, 1 to 100, default 20. Totals cover all groups."`
}

type CategoryInput struct {
	RangeFilters
	GroupBy  string `json:"group_by,omitempty" jsonschema:"category (default) for overall shares, or department for category shares within each department. Cannot be combined with interval."`
	Interval string `json:"interval,omitempty" jsonschema:"auto, hour, or day returns prompt counts per UTC time bucket and category instead of a ranking. auto picks hour for windows up to 48 hours, otherwise day."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Number of ranked category rows to return: 1 to 100, default 20. Shares and totals cover all rows."`
}

// AppliedFilters mirrors the inputs without their schema descriptions.
type AppliedFilters struct {
	Applications     []string `json:"applications,omitempty"`
	Users            []string `json:"users,omitempty"`
	Departments      []string `json:"departments,omitempty"`
	Locations        []string `json:"locations,omitempty"`
	Providers        []string `json:"providers,omitempty"`
	Models           []string `json:"models,omitempty"`
	DepartmentPrefix bool     `json:"department_prefix,omitempty"`
}

type QueryInfo struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	View     string         `json:"view,omitempty"`
	GroupBy  string         `json:"group_by,omitempty"`
	Interval string         `json:"interval,omitempty"`
	Filters  AppliedFilters `json:"filters"`
}

// Response is the envelope of every tool. Each view fills only its own data
// fields; empty lists are omitted.
type Response[T any] struct {
	Query    QueryInfo `json:"query"`
	Warnings []string  `json:"warnings"`
	Data     T         `json:"data"`
}

type UsageData struct {
	Totals      *Usage          `json:"totals,omitempty" jsonschema:"All views except prompt_size and adoption."`
	Points      []UsagePoint    `json:"points,omitempty" jsonschema:"With interval: buckets that have data."`
	Groups      []UsageGroup    `json:"groups,omitempty" jsonschema:"With group_by: ranked by cost."`
	TotalGroups int             `json:"total_groups,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
	PromptSizes *PromptSizeData `json:"prompt_sizes,omitempty" jsonschema:"With group_by=prompt_size."`
	Adoption    *AdoptionData   `json:"adoption,omitempty" jsonschema:"With view=adoption."`
}

type UsagePoint struct {
	Time  string `json:"time"`
	Model string `json:"model"`
	Usage
}

type UsageGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Department string `json:"department,omitempty"`
	Location   string `json:"location,omitempty"`
	TokenUsage
}

type CategoryRow struct {
	Category     string   `json:"category"`
	Department   *string  `json:"department,omitempty"`
	Prompts      int64    `json:"prompts"`
	SharePercent *float64 `json:"share_percent,omitempty"`
}

type CategoryData struct {
	TotalPrompts    int64           `json:"total_prompts"`
	Categories      []CategoryRow   `json:"categories,omitempty" jsonschema:"Without interval: ranked by prompts."`
	TotalCategories int             `json:"total_categories,omitempty"`
	Truncated       bool            `json:"truncated,omitempty"`
	Points          []CategoryPoint `json:"points,omitempty" jsonschema:"With interval: buckets that have data."`
}

type CategoryPoint struct {
	Time         string   `json:"time"`
	Category     string   `json:"category"`
	Prompts      int64    `json:"prompts"`
	SharePercent *float64 `json:"share_percent,omitempty"`
}

type tools struct{ store *store.Store }

// A distinct error type prevents database details from reaching the client.
type inputError string

func (e inputError) Error() string { return string(e) }

func addTool[In, Out any](server *mcp.Server, name, description string, fn func(context.Context, In) (Response[Out], error)) {
	input, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	}
	output, err := jsonschema.For[Response[Out]](nil)
	if err != nil {
		panic(err)
	}
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Icons: icons, InputSchema: nonNullable(input), OutputSchema: nonNullable(output), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (_ *mcp.CallToolResult, out Response[Out], err error) {
			p, ok := ctx.Value(principalKey{}).(principal)
			if !ok || p.user == "" {
				return nil, out, errors.New("caller identity required")
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

// nonNullable drops the "null" alternative inferred for slices and pointers.
// Optional inputs are omitted rather than null, responses never contain null,
// and some clients mishandle type arrays.
func nonNullable(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}
	if len(s.Types) == 2 && slices.Contains(s.Types, "null") {
		s.Type = s.Types[0]
		if s.Type == "null" {
			s.Type = s.Types[1]
		}
		s.Types = nil
	}
	for _, p := range s.Properties {
		nonNullable(p)
	}
	nonNullable(s.Items)
	return s
}

func (t tools) query(in RangeFilters, models ModelFilters) (QueryInfo, time.Time, time.Time, store.Filter, error) {
	var q QueryInfo
	to := time.Now().UTC().Truncate(time.Second)
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
	q.From, q.To = from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)
	q.Filters = AppliedFilters{Applications: f.App, Users: f.User, Departments: f.Department, Locations: f.Location, Providers: f.Provider, Models: f.Models, DepartmentPrefix: f.DeptPrefix}
	return q, from, to, f, nil
}

// resolveInterval returns a store interval ("1 hour" or "1 day"), or "" when
// the interval is optional and omitted.
func resolveInterval(value string, optional bool, from, to time.Time) (string, error) {
	switch value {
	case "":
		if optional {
			return "", nil
		}
		fallthrough
	case "auto":
		if to.Sub(from) <= 48*time.Hour {
			return "1 hour", nil
		}
		return "1 day", nil
	case "hour":
		return "1 hour", nil
	case "day":
		return "1 day", nil
	}
	return "", inputError("interval must be auto, hour, or day")
}

// bucketWarnings flags first/last buckets that cover only part of their interval.
func bucketWarnings(from, to time.Time, iv string) []string {
	step := time.Hour
	if iv == "1 day" {
		step = 24 * time.Hour
	}
	if !from.Equal(from.UTC().Truncate(step)) || !to.Add(time.Nanosecond).Equal(to.UTC().Truncate(step).Add(step)) {
		return []string{"The first or last bucket covers only part of its interval."}
	}
	return nil
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

const unpricedWarning = "Some usage has no catalog price; costs are incomplete (see unpriced_tokens)."

func (t tools) usage(ctx context.Context, in UsageInput) (Response[UsageData], error) {
	switch in.View {
	case "", "usage":
	case "adoption":
		if in.GroupBy != "" {
			return Response[UsageData]{}, inputError("view=adoption cannot be combined with group_by")
		}
		return t.adoption(ctx, in)
	default:
		return Response[UsageData]{}, inputError("view must be usage or adoption")
	}
	if in.GroupBy != "" && in.Interval != "" {
		return Response[UsageData]{}, inputError("group_by cannot be combined with interval")
	}
	switch in.GroupBy {
	case "":
	case "prompt_size":
		return t.promptSizes(ctx, in)
	default:
		return t.usageGrouped(ctx, in)
	}
	q, from, to, f, err := t.query(in.RangeFilters, in.ModelFilters)
	if err != nil {
		return Response[UsageData]{}, err
	}
	iv, err := resolveInterval(in.Interval, true, from, to)
	if err != nil {
		return Response[UsageData]{}, err
	}
	out := Response[UsageData]{Query: q, Warnings: []string{}}
	var totals store.UsageTotals
	if iv == "" {
		if totals, err = t.store.QueryUsageTotals(ctx, from, to, f); err != nil {
			return out, err
		}
		usage := newUsage(totals)
		out.Data.Totals = &usage
	} else {
		out.Query.Interval = strings.TrimPrefix(iv, "1 ")
		points, err := t.store.QueryUsageTimeseries(ctx, from, to, iv, f)
		if err != nil {
			return out, err
		}
		if len(points) > maxPoints {
			return out, inputError("trend exceeds 2000 points; use day or narrow the range and filters")
		}
		data := UsageData{}
		totals.Tokens.Priced = true
		for _, p := range points {
			u := store.UsageTotals{Cost: p.Cost, Tokens: p.Tokens, Requests: p.Requests, CacheReadTokens: p.CacheReadTokens, CacheCreationTokens: p.CacheCreationTokens, UnpricedTokens: p.UnpricedTokens}
			data.Points = append(data.Points, UsagePoint{Time: p.Bucket.UTC().Format(time.RFC3339), Model: p.Model, Usage: newUsage(u)})
			// Derive totals from the same snapshot as the trend, matching the
			// personal API even while telemetry is being ingested.
			totals.Cost += p.Cost
			totals.Requests += p.Requests
			totals.CacheReadTokens += p.CacheReadTokens
			totals.CacheCreationTokens += p.CacheCreationTokens
			totals.UnpricedTokens += p.UnpricedTokens
			totals.Tokens.Input += p.Tokens.Input
			totals.Tokens.Output += p.Tokens.Output
			totals.Tokens.Cached += p.Tokens.Cached
			totals.Tokens.Reasoning += p.Tokens.Reasoning
			totals.Tokens.CacheSavings += p.Tokens.CacheSavings
			totals.Tokens.Priced = totals.Tokens.Priced && p.Tokens.Priced
		}
		sort.Slice(data.Points, func(i, j int) bool {
			a, b := data.Points[i], data.Points[j]
			if a.Time != b.Time {
				return a.Time < b.Time
			}
			return a.Model < b.Model
		})
		usage := newUsage(totals)
		data.Totals = &usage
		out.Data = data
		if len(points) > 0 {
			out.Warnings = append(out.Warnings, bucketWarnings(from, to, iv)...)
		}
	}
	if !totals.Tokens.Priced || totals.UnpricedTokens > 0 {
		out.Warnings = append(out.Warnings, unpricedWarning)
	}
	if totals.Tokens.Input+totals.Tokens.Output+totals.Tokens.Cached == 0 {
		out.Warnings = append(out.Warnings, "No matching usage.")
	}
	return out, nil
}

var costAggregators = map[string]func([]store.CostRow) []store.CostRow{
	"model": store.AggregateCostsByModel, "application": store.AggregateCostsByApp,
	"department": store.AggregateCostsByDepartment, "location": store.AggregateCostsByLocation, "user": store.AggregateCostsByUser,
}

func usageGroup(r store.CostRow, group string) UsageGroup {
	g := UsageGroup{TokenUsage: tokenUsage(r.TotalCost, r.CacheSavings, int64(r.InputTokens), int64(r.CacheReadTokens), int64(r.CacheCreationTokens), int64(r.OutputTokens), int64(r.ReasoningTokens), int64(r.UnpricedTokens))}
	switch group {
	case "model":
		g.ID = r.ProviderName + "/" + r.RequestModel
	case "application":
		g.ID, g.Name = r.AppID, r.AppName
	case "user":
		g.ID, g.Name, g.Department, g.Location = r.ID, r.Name, r.Department, r.Location
	case "department":
		g.ID = r.Department
	case "location":
		g.ID = r.Location
	}
	if g.Name == g.ID {
		g.Name = ""
	}
	return g
}

// usageGrouped ranks usage by one dimension. Totals come from the same query
// as the ungrouped totals so both modes agree.
func (t tools) usageGrouped(ctx context.Context, in UsageInput) (Response[UsageData], error) {
	aggregate, ok := costAggregators[in.GroupBy]
	if !ok {
		return Response[UsageData]{}, inputError("group_by must be model, application, department, location, user, or prompt_size")
	}
	q, from, to, f, err := t.query(in.RangeFilters, in.ModelFilters)
	if err != nil {
		return Response[UsageData]{}, err
	}
	limit, err := rowLimit(in.Limit)
	if err != nil {
		return Response[UsageData]{}, err
	}
	rows, err := t.store.QueryCostBreakdown(ctx, from, to, f)
	if err != nil {
		return Response[UsageData]{}, err
	}
	totals, err := t.store.QueryUsageTotals(ctx, from, to, f)
	if err != nil {
		return Response[UsageData]{}, err
	}
	groups := []UsageGroup{}
	for _, r := range aggregate(rows) {
		groups = append(groups, usageGroup(r, in.GroupBy))
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].CostUSD != groups[j].CostUSD {
			return groups[i].CostUSD > groups[j].CostUSD
		}
		return groups[i].ID < groups[j].ID
	})
	u := newUsage(totals)
	data := UsageData{Totals: &u, Groups: groups, TotalGroups: len(groups)}
	if len(groups) > limit {
		data.Groups, data.Truncated = groups[:limit], true
	}
	q.GroupBy = in.GroupBy
	out := Response[UsageData]{Query: q, Warnings: []string{}, Data: data}
	if !totals.Tokens.Priced || totals.UnpricedTokens > 0 {
		out.Warnings = append(out.Warnings, unpricedWarning)
	}
	if len(groups) == 0 {
		out.Warnings = append(out.Warnings, "No matching usage.")
	}
	return out, nil
}

func (t tools) categories(ctx context.Context, in CategoryInput) (Response[CategoryData], error) {
	if in.Interval != "" {
		if in.GroupBy != "" {
			return Response[CategoryData]{}, inputError("group_by cannot be combined with interval")
		}
		return t.categoryTrend(ctx, in)
	}
	q, from, to, f, err := t.query(in.RangeFilters, ModelFilters{})
	if err != nil {
		return Response[CategoryData]{}, err
	}
	limit, err := rowLimit(in.Limit)
	if err != nil {
		return Response[CategoryData]{}, err
	}
	group := in.GroupBy
	if group == "" {
		group = "category"
	}
	if group != "category" && group != "department" {
		return Response[CategoryData]{}, inputError("group_by must be category or department")
	}
	rows := []CategoryRow{}
	denominators := map[string]int64{}
	if group == "category" {
		summary, err := t.store.QueryClassificationSummary(ctx, from, to, f)
		if err != nil {
			return Response[CategoryData]{}, err
		}
		for _, r := range summary {
			if r.Kind == "category" && r.Matched > 0 {
				rows = append(rows, CategoryRow{Category: r.ID, Prompts: r.Matched})
				denominators[""] += r.Matched
			}
		}
	} else {
		topics, err := t.store.QueryClassificationDepartmentTopics(ctx, from, to, f)
		if err != nil {
			return Response[CategoryData]{}, err
		}
		for _, r := range topics {
			if r.Prompts > 0 {
				dept := r.Department
				rows = append(rows, CategoryRow{Category: r.Category, Department: &dept, Prompts: r.Prompts})
				denominators[dept] += r.Prompts
			}
		}
	}
	data := CategoryData{}
	for i := range rows {
		key := ""
		if rows[i].Department != nil {
			key = *rows[i].Department
		}
		rows[i].SharePercent = percent(float64(rows[i].Prompts), float64(denominators[key]))
		data.TotalPrompts += rows[i].Prompts
	}
	dept := func(r CategoryRow) string {
		if r.Department == nil {
			return ""
		}
		return *r.Department
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Prompts != b.Prompts {
			return a.Prompts > b.Prompts
		}
		if dept(a) != dept(b) {
			return dept(a) < dept(b)
		}
		return a.Category < b.Category
	})
	data.Categories, data.TotalCategories = rows, len(rows)
	if len(rows) > limit {
		data.Categories, data.Truncated = rows[:limit], true
	}
	if group == "department" {
		q.GroupBy = group
	}
	out := Response[CategoryData]{Query: q, Warnings: []string{}, Data: data}
	if len(rows) == 0 {
		out.Warnings = append(out.Warnings, "No matching classified prompts.")
	}
	return out, nil
}

func (t tools) categoryTrend(ctx context.Context, in CategoryInput) (Response[CategoryData], error) {
	q, from, to, f, err := t.query(in.RangeFilters, ModelFilters{})
	if err != nil {
		return Response[CategoryData]{}, err
	}
	iv, err := resolveInterval(in.Interval, false, from, to)
	if err != nil {
		return Response[CategoryData]{}, err
	}
	points, err := t.store.QueryClassificationTopicTimeseries(ctx, from, to, iv, f)
	if err != nil {
		return Response[CategoryData]{}, err
	}
	if len(points) > maxPoints {
		return Response[CategoryData]{}, inputError("trend exceeds 2000 points; use day or narrow the range and filters")
	}
	data := CategoryData{}
	perBucket := map[string]int64{}
	for _, p := range points {
		ts := p.Bucket.UTC().Format(time.RFC3339)
		data.Points = append(data.Points, CategoryPoint{Time: ts, Category: p.Label, Prompts: p.Count})
		perBucket[ts] += p.Count
		data.TotalPrompts += p.Count
	}
	for i := range data.Points {
		data.Points[i].SharePercent = percent(float64(data.Points[i].Prompts), float64(perBucket[data.Points[i].Time]))
	}
	sort.Slice(data.Points, func(i, j int) bool {
		a, b := data.Points[i], data.Points[j]
		if a.Time != b.Time {
			return a.Time < b.Time
		}
		return a.Category < b.Category
	})
	q.Interval = strings.TrimPrefix(iv, "1 ")
	out := Response[CategoryData]{Query: q, Warnings: []string{}, Data: data}
	if len(points) == 0 {
		out.Warnings = append(out.Warnings, "No matching classified prompts.")
	} else {
		out.Warnings = append(out.Warnings, bucketWarnings(from, to, iv)...)
	}
	return out, nil
}
