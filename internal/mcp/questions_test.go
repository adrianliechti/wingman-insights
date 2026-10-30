package mcpserver

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"insights/internal/store"
)

func TestUsageCallStatistics(t *testing.T) {
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
	client, _ := connect(t, s, "admin", "admins")
	args := map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "interval": "hour", "users": []string{"alice"}}
	u := call[Response[UsageData]](t, client, "get_usage", args)
	stats := *u.Data.Totals
	if stats.Requests != 3 || stats.UnpricedTokens != 500 || stats.CacheReadTokens != 200000 || stats.InputTokensPerRequest == nil || *stats.InputTokensPerRequest != round(1000700.0/3, 2) || *stats.ReasoningPercent != 10 {
		t.Fatalf("call statistics: %+v", stats)
	}
	if *stats.CacheReadPercent != round(20000000.0/1000700, 2) {
		t.Fatal("cache read share double-counted or used output tokens")
	}
	// Omit interval: summary and bucketed statistics must use the same ledger.
	delete(args, "interval")
	plain := call[Response[UsageData]](t, client, "get_usage", args)
	if !reflect.DeepEqual(*plain.Data.Totals, stats) {
		t.Fatalf("summary/trend statistics diverged: %+v vs %+v", plain.Data.Totals, stats)
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
	args := map[string]any{"from": from.Format(time.RFC3339Nano), "to": to.Format(time.RFC3339Nano), "applications": []string{"adoption-only"}, "interval": "day", "view": "adoption"}
	a := call[Response[UsageData]](t, client, "get_usage", args)
	want := store.AdoptionSummary{ActiveUsers: 2, NewUsers: 1, ReturningUsers: 1, SingleDayUsers: 1, RepeatDayUsers: 1, Requests: 3}
	if a.Data.Adoption.Totals != want || len(a.Data.Adoption.Points) != 2 || len(a.Data.Adoption.Frequency) != 2 || a.Query.View != "adoption" || len(a.Warnings) != 0 {
		t.Fatalf("adoption: %+v", a)
	}
	if a.Data.Adoption.Points[0].NewUsers != 1 || a.Data.Adoption.Points[0].ReturningUsers != 1 || a.Data.Adoption.Points[1].ReturningUsers != 1 {
		t.Fatal("new/returning baseline ignored retained history")
	}
	if f := a.Data.Adoption.Frequency; f[0].ActiveDays != 1 || f[1].ActiveDays != 2 || f[0].Users+f[1].Users != 2 {
		t.Fatal("active-day frequency not ordered or distinct")
	}
	args["departments"] = []string{"Engineering"}
	filtered := call[Response[UsageData]](t, client, "get_usage", args)
	if filtered.Data.Adoption.Totals.ActiveUsers != 1 || filtered.Data.Adoption.Totals.ReturningUsers != 1 {
		t.Fatal("adoption ignored directory filters")
	}
	args["applications"] = []string{"missing"}
	empty := call[Response[UsageData]](t, client, "get_usage", args)
	if empty.Data.Adoption.Totals.ActiveUsers != 0 || empty.Data.Adoption.Points == nil || len(empty.Data.Adoption.Points) != 0 || empty.Data.Adoption.Frequency == nil {
		t.Fatal("empty adoption fabricated observations")
	}
}

func TestPromptSizeBoundariesAndShares(t *testing.T) {
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
	client, _ := connect(t, s, "admin", "admins")
	p := call[Response[UsageData]](t, client, "get_usage", map[string]any{"group_by": "prompt_size", "users": []string{"alice"}})
	if p.Data.PromptSizes.Totals.Requests != 10 || p.Data.PromptSizes.Totals.UnpricedRequests != 1 || p.Data.PromptSizes.Totals.UnpricedTokens != 500 || len(p.Data.PromptSizes.Bins) != 5 || len(p.Warnings) == 0 {
		t.Fatalf("prompt size shares: %+v", p)
	}
	var costShare float64
	for _, b := range p.Data.PromptSizes.Bins {
		if b.Requests != 2 || *b.RequestPercent != 20 {
			t.Fatal("prompt size boundaries/order changed")
		}
		if b.CostPercent != nil {
			costShare += *b.CostPercent
		}
	}
	if math.Abs(costShare-100) > 0.05 || p.Data.PromptSizes.Bins[4].CostPercent == nil {
		t.Fatalf("cost shares: %v", costShare)
	}
	if p.Data.PromptSizes.Bins[0].MinInputTokens != 1 || *p.Data.PromptSizes.Bins[0].MaxInputTokens != 4000 || p.Data.PromptSizes.Bins[4].MinInputTokens != 272001 || p.Data.PromptSizes.Bins[4].MaxInputTokens != nil {
		t.Fatal("missing exact bin bounds")
	}
	empty := call[Response[UsageData]](t, client, "get_usage", map[string]any{"group_by": "prompt_size", "users": []string{"alice"}, "models": []string{"missing"}})
	if empty.Data.PromptSizes.Totals.Requests != 0 || len(empty.Data.PromptSizes.Bins) != 5 || empty.Data.PromptSizes.Bins[0].RequestPercent != nil {
		t.Fatal("empty distribution invented a rate")
	}
	unpriced := call[Response[UsageData]](t, client, "get_usage", map[string]any{"group_by": "prompt_size", "users": []string{"alice"}, "models": []string{"unpriced"}})
	if unpriced.Data.PromptSizes.Totals.Requests != 1 || unpriced.Data.PromptSizes.Totals.UnpricedRequests != 1 {
		t.Fatal("unpriced calls were dropped")
	}
	for _, b := range unpriced.Data.PromptSizes.Bins {
		if b.CostPercent != nil {
			t.Fatal("invented a known cost share for unpriced calls")
		}
	}
}
