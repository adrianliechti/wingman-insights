package mcpserver

import (
	"context"
	"math"
	"testing"
	"time"

	"insights/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUsageComparisonAndCallStatistics(t *testing.T) {
	s := fixture(t)
	from := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	to := from.Add(2 * time.Hour)
	if err := s.InsertSpans(context.Background(), []store.SpanRow{
		{Time: from.Add(-time.Hour), ReceivedAt: from, UserID: "alice", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 1000, OutputTokens: 100},
		{Time: from.Add(-time.Hour), ReceivedAt: from, UserID: "bob", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 9999999},
		{Time: from, ReceivedAt: from, UserID: "alice", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 200},
	}); err != nil {
		t.Fatal(err)
	}
	client, _ := connect(t, s, "alice")
	args := map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "interval": "hour", "chart_metric": "requests", "compare_previous": true, "users": []string{"bob"}}
	u := call[Response[UsageData]](t, client, "get_usage", args)
	stats := u.Data.Statistics
	if stats.Requests != 3 || stats.UnpricedTokens != 500 || stats.CacheReadTokens != 200000 || stats.InputTokensPerRequest == nil || math.Abs(*stats.InputTokensPerRequest-1000700.0/3) > 1e-8 || *stats.ReasoningSharePercent != 10 {
		t.Fatalf("call statistics: %+v", stats)
	}
	if math.Abs(*stats.CacheReadSharePercent-20000000.0/1000700) > 1e-8 {
		t.Fatal("cache read share double-counted or used output tokens")
	}
	c := u.Data.Comparison
	if c == nil || c.Statistics.Requests != 1 || c.Tokens.Input != 1000 || c.To != from.Add(-time.Nanosecond).Format(time.RFC3339Nano) {
		t.Fatalf("comparison leaked users or overlapped bounds: %+v", c)
	}
	d := c.Changes["requests"]
	if *d.AbsoluteChange != 2 || *d.PercentChange != 200 || chartSum(u.Chart) != 3 || chartSum(c.Chart) != 4 {
		t.Fatalf("comparison changes: %+v", c)
	}
	// Omit interval: summary and bucketed statistics must use the same ledger.
	delete(args, "interval")
	plain := call[Response[UsageData]](t, client, "get_usage", args)
	if plain.Data.Statistics.Requests != stats.Requests || *plain.Data.Statistics.CostPerRequest != *stats.CostPerRequest {
		t.Fatal("summary/trend statistics diverged")
	}
	delete(args, "compare_previous")
	args["compare_from"], args["compare_to"] = "2000-01-01T00:00:00Z", "2000-01-02T00:00:00Z"
	empty := call[Response[UsageData]](t, client, "get_usage", args)
	if empty.Data.Comparison.Changes["cost_usd"].PercentChange != nil || empty.Data.Comparison.Statistics.CostPerRequest != nil {
		t.Fatal("zero baseline invented a percentage or average")
	}
	for _, bad := range []map[string]any{
		{"compare_from": "2000-01-01T00:00:00Z"},
		{"compare_to": "2000-01-02T00:00:00Z"},
		{"compare_previous": true, "compare_from": "2000-01-01T00:00:00Z"},
		{"compare_from": "2000-01-02T00:00:00Z", "compare_to": "2000-01-01T00:00:00Z"},
		{"compare_from": "2000-01-01T00:00:00Z", "compare_to": "2001-01-01T00:00:00Z"},
	} {
		r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_usage", Arguments: bad})
		if err == nil && !r.IsError {
			t.Fatalf("accepted invalid comparison: %+v", bad)
		}
	}
}

func TestAverageChartsPoolOtherDenominators(t *testing.T) {
	ts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	d := UsageData{Tokens: store.TokenTotals{Input: 1}, Buckets: []UsageBucket{
		{Bucket: ts.Format(time.RFC3339), Model: "a", Cost: 100, Tokens: store.TokenTotals{Input: 100}, Statistics: UsageStatistics{Requests: 1}},
		{Bucket: ts.Format(time.RFC3339), Model: "b", Cost: 9, Tokens: store.TokenTotals{Input: 900}, Statistics: UsageStatistics{Requests: 9}},
		{Bucket: ts.Format(time.RFC3339), Model: "c", Cost: 200, Tokens: store.TokenTotals{Input: 10}, Statistics: UsageStatistics{Requests: 1}},
	}}
	c, err := usageChart(d, QueryInfo{}, "cost_per_request_usd", 1, ts, ts.Add(time.Hour-time.Nanosecond), "1 hour")
	if err != nil {
		t.Fatal(err)
	}
	var other float64
	for _, r := range c.Rows {
		if r.Series == "other" {
			other = *r.Value
		}
	}
	if math.Abs(other-10.9) > 1e-9 {
		t.Fatalf("Other cost/call=%v, want pooled 109/10", other)
	}
}

func TestAdoptionUsesObservedCanonicalProviderActivity(t *testing.T) {
	s := fixture(t)
	from := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	to := from.Add(72*time.Hour - time.Nanosecond)
	if _, err := s.DB().Exec(`INSERT INTO directory(alias,id,kind) VALUES ('svc','svc','application'), ('bob@example.com','bob','user')`); err != nil {
		t.Fatal(err)
	}
	spans := []store.SpanRow{
		{Time: from.Add(-24 * time.Hour), UserID: "alice@example.com", InputTokens: 10},
		{Time: from.Add(time.Hour), UserID: "alice@example.com", InputTokens: 10},
		{Time: from.Add(25 * time.Hour), UserID: "alice", InputTokens: 10},
		{Time: from.Add(2 * time.Hour), UserEmail: "bob@example.com", InputTokens: 10},
		{Time: from.Add(3 * time.Hour), UserID: "svc", InputTokens: 10},
		{Time: from.Add(3 * time.Hour), InputTokens: 10},
		{Time: from.Add(3 * time.Hour), UserID: "alice", InputTokens: 999999, Attributes: map[string]string{"wingman.telemetry.source": "chat"}},
		{Time: from.Add(3 * time.Hour), UserID: "alice", ToolName: "tool"},
	}
	for i := range spans {
		spans[i].ReceivedAt = from
		spans[i].AppID = "adoption-only"
		spans[i].RequestModel = "gpt-5.1"
		spans[i].ProviderName = "openai"
	}
	if err := s.InsertSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	client, _ := connect(t, s, "admin", "admins")
	args := map[string]any{"from": from.Format(time.RFC3339Nano), "to": to.Format(time.RFC3339Nano), "applications": []string{"adoption-only"}, "interval": "day", "chart_metric": "new_vs_returning_users"}
	a := call[Response[store.AdoptionReport]](t, client, "get_adoption", args)
	want := store.AdoptionSummary{ActiveUsers: 2, NewUsers: 1, ReturningUsers: 1, SingleDayUsers: 1, RepeatDayUsers: 1, Requests: 3}
	if a.Data.Summary != want || len(a.Data.Buckets) != 2 || len(a.Data.Frequency) != 2 {
		t.Fatalf("adoption: %+v", a.Data)
	}
	if a.Data.Buckets[0].NewUsers != 1 || a.Data.Buckets[0].ReturningUsers != 1 || a.Data.Buckets[1].ReturningUsers != 1 {
		t.Fatal("new/returning baseline ignored retained history")
	}
	if len(a.Chart.Rows) != 6 || a.Chart.PartialBuckets || a.Chart.YAxis.Unit != "users" {
		t.Fatalf("adoption chart: %+v", a.Chart)
	}
	args["chart_metric"] = "active_days"
	f := call[Response[store.AdoptionReport]](t, client, "get_adoption", args)
	if len(f.Chart.Rows) != 2 || f.Chart.Rows[0].X != "1" || f.Chart.Rows[1].X != "2" || chartSum(f.Chart) != 2 {
		t.Fatal("active-day frequency not ordered or distinct")
	}
	args["departments"] = []string{"Engineering"}
	filtered := call[Response[store.AdoptionReport]](t, client, "get_adoption", args)
	if filtered.Data.Summary.ActiveUsers != 1 || filtered.Data.Summary.ReturningUsers != 1 {
		t.Fatal("adoption ignored directory filters")
	}
	personal, _ := connect(t, s, "alice")
	if r, err := personal.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_adoption", Arguments: args}); err == nil && !r.IsError {
		t.Fatal("personal caller read admin adoption")
	}
	args["applications"] = []string{"missing"}
	empty := call[Response[store.AdoptionReport]](t, client, "get_adoption", args)
	if empty.Data.Summary.ActiveUsers != 0 || len(empty.Chart.Rows) != 0 || empty.Data.Buckets == nil || empty.Data.Frequency == nil {
		t.Fatal("empty adoption fabricated observations")
	}
}

func TestPromptSizeBoundariesSharesAndPersonalScope(t *testing.T) {
	s := fixture(t)
	ts := time.Now().UTC().Add(-time.Hour)
	spans := []store.SpanRow{}
	for _, n := range []int64{4000, 4001, 64000, 64001, 200000, 200001, 272000, 272001} {
		spans = append(spans, store.SpanRow{Time: ts, ReceivedAt: ts, UserID: "alice", AppID: "chat", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: n})
	}
	spans = append(spans, store.SpanRow{Time: ts, ReceivedAt: ts, UserID: "alice", RequestModel: "gpt-5.1", OutputTokens: 20})
	if err := s.InsertSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	client, _ := connect(t, s, "alice")
	p := call[Response[PromptSizeData]](t, client, "get_prompt_sizes", map[string]any{"users": []string{"bob"}, "chart_metric": "request_share_percent"})
	if p.Query.Scope != "personal" || p.Data.Requests != 10 || p.Data.UnpricedRequests != 1 || p.Data.UnpricedTokens != 500 || len(p.Data.Rows) != 5 || math.Abs(chartSum(p.Chart)-100) > 1e-9 {
		t.Fatalf("prompt size shares/scope: %+v", p)
	}
	for i, r := range p.Data.Rows {
		if r.Requests != 2 || *r.RequestSharePercent != 20 || p.Chart.Rows[i].XLabel != r.Bucket {
			t.Fatal("prompt size boundaries/order changed")
		}
	}
	if p.Data.Rows[0].MinInputTokens != 1 || *p.Data.Rows[0].MaxInputTokens != 4000 || p.Data.Rows[4].MinInputTokens != 272001 || p.Data.Rows[4].MaxInputTokens != nil {
		t.Fatal("missing exact bin bounds")
	}
	c := call[Response[PromptSizeData]](t, client, "get_prompt_sizes", map[string]any{"chart_metric": "cost_share_percent"})
	if math.Abs(chartSum(c.Chart)-100) > 1e-9 || c.Data.Rows[4].CostSharePercent == nil || len(c.Chart.Notes) == 0 {
		t.Fatal("cost shares or incomplete pricing notes missing")
	}
	empty := call[Response[PromptSizeData]](t, client, "get_prompt_sizes", map[string]any{"models": []string{"missing"}})
	if empty.Data.Requests != 0 || len(empty.Data.Rows) != 5 || len(empty.Chart.Rows) != 0 || empty.Data.Rows[0].RequestSharePercent != nil {
		t.Fatal("empty distribution invented a rate")
	}
	unpriced := call[Response[PromptSizeData]](t, client, "get_prompt_sizes", map[string]any{"models": []string{"unpriced"}, "chart_metric": "cost_share_percent"})
	if unpriced.Data.Requests != 1 || unpriced.Data.UnpricedRequests != 1 {
		t.Fatal("unpriced calls were dropped")
	}
	for _, r := range unpriced.Chart.Rows {
		if r.Value != nil {
			t.Fatal("invented a known cost share for unpriced calls")
		}
	}
}
