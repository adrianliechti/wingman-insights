package mcpserver

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"insights/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func chartSum(c Chart) float64 {
	var sum float64
	for _, r := range c.Rows {
		if r.Value != nil {
			sum += *r.Value
		}
	}
	return sum
}

func TestHTTPChartData(t *testing.T) {
	s := fixture(t)
	client, _ := connect(t, s, "alice")
	u := call[Response[UsageData]](t, client, "get_usage", map[string]any{"interval": "auto", "chart_metric": "total_tokens", "top_n": 1, "users": []string{"bob"}})
	if u.Chart.Type != "line" || u.Chart.YAxis.Unit != "tokens" || u.Chart.XAxis.Unit != "UTC" || u.Query.Interval != "hour" || !u.Chart.IncludesOther {
		t.Fatalf("chart metadata: %+v", u.Chart)
	}
	if chartSum(u.Chart) != 1100500 {
		t.Fatalf("chart leaked user or double-counted reasoning: %v", chartSum(u.Chart))
	}
	missing := 0
	for _, r := range u.Chart.Rows {
		if r.Value == nil {
			missing++
		}
		if r.X == "" || r.XLabel == "" || r.Series == "" || r.SeriesLabel == "" {
			t.Fatalf("unlabeled point: %+v", r)
		}
	}
	if missing == 0 || len(u.Chart.Rows) > maxPoints || u.Chart.MissingData != "null" || !u.Chart.PartialBuckets {
		t.Fatal("expected bounded aligned UTC grid with null gaps")
	}
	b, _ := json.Marshal(u.Chart)
	if !strings.Contains(string(b), `"value":null`) {
		t.Fatal("missing telemetry must serialize as null")
	}
	totals := call[Response[UsageData]](t, client, "get_usage", map[string]any{"chart_metric": "total_tokens"})
	if len(totals.Chart.Rows) != 3 || chartSum(totals.Chart) != 1100500 || totals.Chart.YAxis.Unit != "tokens" {
		t.Fatal("token partitions disagree with accounting")
	}

	c := call[Response[CostData]](t, client, "get_cost_breakdown", map[string]any{"chart_metric": "total_tokens", "top_n": 1, "limit": 1})
	if !c.Truncated || !c.Chart.IncludesOther || len(c.Chart.Rows) != 2 || chartSum(c.Chart) != 1100500 {
		t.Fatalf("raw limit lost chart data: %+v", c)
	}
	if len(c.Chart.Notes) == 0 {
		t.Fatal("chart lost unknown-pricing notes")
	}
	// Model selection remains separate from the chart readability settings.
	f := call[Response[UsageData]](t, client, "get_usage", map[string]any{"interval": "auto", "chart_metric": "total_tokens", "models": []string{"gpt-5.1"}})
	if chartSum(f.Chart) != 1100000 {
		t.Fatal("chart ignored model filter")
	}

	admin, _ := connect(t, s, "admin", "admins")
	for _, name := range []string{"get_usage", "get_cost_breakdown", "get_categories", "get_category_trends"} {
		for _, args := range []map[string]any{{"top_n": -1}, {"top_n": 21}, {"chart_metric": "arbitrary"}} {
			r, err := admin.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err == nil && !r.IsError {
				t.Fatalf("%s accepted invalid chart input: %+v", name, args)
			}
		}
	}
}

func TestHTTPCategoryCharts(t *testing.T) {
	client, _ := connect(t, fixture(t), "admin", "admins")
	overall := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"chart_metric": "share_percent", "top_n": 1, "limit": 1})
	if !overall.Truncated || !overall.Chart.IncludesOther || chartSum(overall.Chart) != 100 || len(overall.Chart.Rows) != 2 {
		t.Fatalf("category shares lost denominator: %+v", overall)
	}
	dept := call[Response[CategoryData]](t, client, "get_categories", map[string]any{"group_by": "department", "chart_metric": "share_percent", "top_n": 1, "limit": 1})
	if dept.Chart.Type != "stacked_bar" || dept.Chart.YAxis.Unit != "percent" {
		t.Fatal("department chart metadata missing")
	}
	shares := map[string]float64{}
	for _, r := range dept.Chart.Rows {
		shares[r.X] += *r.Value
	}
	if len(shares) != 2 {
		t.Fatalf("missing Other departments: %+v", shares)
	}
	for _, sum := range shares {
		if math.Abs(sum-100) > 1e-9 {
			t.Fatal("department shares must include all categories")
		}
	}
	trend := call[Response[[]CategoryPoint]](t, client, "get_category_trends", map[string]any{"interval": "auto", "chart_metric": "share_percent", "top_n": 1})
	if math.Abs(chartSum(trend.Chart)-100) > 1e-9 || !trend.Chart.IncludesOther {
		t.Fatal("category trend shares must sum to 100 within the observed bucket")
	}
	for _, name := range []string{"get_usage", "get_cost_breakdown", "get_categories", "get_category_trends"} {
		r := call[Response[json.RawMessage]](t, client, name, map[string]any{"applications": []string{"unknown"}})
		if len(r.Chart.Rows) != 0 || r.Chart.Rows == nil {
			t.Fatalf("%s invented chart data for empty result", name)
		}
	}
}

func TestChartTailAndAlignedGaps(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3*time.Hour - time.Nanosecond)
	t0, t2 := from.Format(time.RFC3339), from.Add(2*time.Hour).Format(time.RFC3339)
	rows := []CategoryPoint{
		{Bucket: t0, Category: "a", Label: "A", Prompts: 6}, {Bucket: t0, Category: "b", Label: "B", Prompts: 4},
		{Bucket: t2, Category: "b", Label: "B", Prompts: 4}, {Bucket: t2, Category: "c", Label: "C", Prompts: 2},
	}
	c, err := categoryTrendChart(rows, "prompts", 1, QueryInfo{}, from, to, "1 hour")
	if err != nil {
		t.Fatal(err)
	}
	if !c.IncludesOther || c.PartialBuckets || len(c.Rows) != 6 || chartSum(c) != 16 {
		t.Fatalf("alignment/tail: %+v", c)
	}
	// B wins over the whole range (8), even though A wins the first bucket.
	seen := map[string]bool{}
	for _, r := range c.Rows {
		seen[r.Series] = true
		if r.X == from.Add(time.Hour).Format(time.RFC3339) && r.Value != nil {
			t.Fatal("fabricated zeros in empty bucket")
		}
	}
	if !seen["category:b"] || !seen["other"] || len(seen) != 2 {
		t.Fatalf("unstable top series: %+v", seen)
	}
	for range 5 {
		repeat, err := categoryTrendChart(rows, "prompts", 1, QueryInfo{}, from, to, "1 hour")
		if err != nil || !reflect.DeepEqual(c, repeat) {
			t.Fatal("chart ordering is nondeterministic")
		}
	}
	_, err = categoryTrendChart(rows, "prompts", 1, QueryInfo{}, from, from.Add(80*24*time.Hour), "1 hour")
	if err == nil {
		t.Fatal("dense grid exceeded point budget without an error")
	}
	if iv, _ := chartInterval("auto", true, from, from.Add(90*24*time.Hour), 20); iv != "1 day" {
		t.Fatal("auto interval failed to fit long range")
	}
}

func TestChartIdentityAndTailConservation(t *testing.T) {
	rows := []store.CostRow{
		{ID: "a", Name: "Same Name", TotalCost: 9},
		{ID: "b", Name: "Same Name", TotalCost: 8},
		{ID: "other", Name: "Other (remaining)", TotalCost: 7},
		{ID: "d", Name: "D", TotalCost: 6},
	}
	c, err := costChart(rows, "user", "cost_usd", 2, QueryInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if chartSum(c) != 30 || len(c.Rows) != 3 || c.Rows[0].X == c.Rows[1].X || c.Rows[0].XLabel != c.Rows[1].XLabel || c.Rows[2].X != "other" {
		t.Fatalf("lost identity or spend: %+v", c)
	}
	all, err := costChart(rows, "user", "cost_usd", 4, QueryInfo{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range all.Rows {
		ids[r.X] = true
	}
	if !ids["user:other"] || all.IncludesOther {
		t.Fatal("real entity called other collided with synthetic tail")
	}
}

func TestOtherDepartmentPercentagesUsePooledCounts(t *testing.T) {
	rows := []CategoryRow{
		{Department: "Big", Category: "legal", Label: "Legal", Prompts: 90},
		{Department: "Big", Category: "coding", Label: "Coding", Prompts: 10},
		{Department: "Small", Category: "coding", Label: "Coding", Prompts: 10},
		{Department: "Tiny", Category: "legal", Label: "Legal", Prompts: 1},
	}
	c, err := categoryChart(rows, "department", "share_percent", 1, QueryInfo{})
	if err != nil {
		t.Fatal(err)
	}
	var share float64
	for _, r := range c.Rows {
		if r.X == "other" && r.Series == "category:legal" {
			share = *r.Value
		}
	}
	if math.Abs(share-100.0/11) > 1e-9 {
		t.Fatalf("pooled Other department legal share=%v, want %v", share, 100.0/11)
	}
}
