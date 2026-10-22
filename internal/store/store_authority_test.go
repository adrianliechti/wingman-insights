package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGatewayUsageKeepsChatActivity(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Add(-time.Minute)
	gateway := SpanRow{ReceivedAt: ts, Time: ts, TraceID: "gateway", SpanID: "provider", SessionID: "conversation-1", UserID: "user-1", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 1000, OutputTokens: 100, CacheRead: 200, Reasoning: 10, Attributes: map[string]string{"wingman.telemetry.source": "gateway"}}
	chat := gateway
	chat.TraceID = "chat"
	chat.SpanID = "root"
	chat.Attributes = map[string]string{"wingman.telemetry.source": "chat", "gen_ai.usage.input_tokens": "1000"}
	tool := chat
	tool.SpanID = "tool"
	tool.ParentSpanID = "root"
	tool.ToolName = "files_search"
	tool.Duration = 0.5
	tool.Attributes = map[string]string{"wingman.telemetry.source": "chat", "wingman.mcp.server.id": "files", "mcp.method.name": "tools/call"}
	// Export ordering and matching counts cannot change which producer accounts.
	for _, span := range []SpanRow{chat, tool, gateway} {
		if err := s.InsertSpans(ctx, []SpanRow{span}); err != nil {
			t.Fatal(err)
		}
	}
	f := Filter{User: []string{"user-1"}}
	costs, err := s.QueryCostBreakdown(ctx, ts.Add(-time.Hour), time.Now().UTC(), f)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, wantCost, _, _ := gateway.costBreakdown()
	if len(costs) != 1 || costs[0].TotalCost != wantCost || costs[0].CacheReadTokens != 200 {
		t.Fatalf("costs = %+v, want provider cost %v", costs, wantCost)
	}
	spans, err := s.QueryTrace(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 {
		t.Fatalf("chat spans = %d", len(spans))
	}
	for _, span := range spans {
		if span.InputTokens != 0 || span.OutputTokens != 0 || span.Cost != 0 {
			t.Fatalf("chat billed: %+v", span)
		}
	}
	for _, span := range spans {
		if span.SpanID == "root" && span.Attrs["gen_ai.usage.input_tokens"] != "1000" {
			t.Fatalf("raw usage lost: %+v", span.Attrs)
		}
	}
	tools, err := s.QueryToolStats(ctx, ts.Add(-time.Hour), time.Now().UTC(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].ToolName != "files_search" || tools[0].MCPServer != "files" || tools[0].Count != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	stats, err := s.QuerySessionStats(ctx, ts.Add(-time.Hour), time.Now().UTC(), f)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sessions != 1 || stats.AvgTokensPerSess != 1100 {
		t.Fatalf("session stats = %+v", stats)
	}
	costs, err = s.QueryCostBreakdown(ctx, ts.Add(-time.Hour), time.Now().UTC(), Filter{User: []string{"other"}})
	if err != nil || len(costs) != 0 {
		t.Fatalf("unrelated user: %+v %v", costs, err)
	}
}

func TestEqualNestedGatewayCallsBothCount(t *testing.T) {
	s := openStore(t)
	ts := time.Now().UTC()
	span := SpanRow{ReceivedAt: ts, Time: ts, TraceID: "trace", SpanID: "parent", InputTokens: 100, OutputTokens: 10, Attributes: map[string]string{"wingman.telemetry.source": "gateway"}}
	child := span
	child.SpanID = "child"
	child.ParentSpanID = "parent"
	if err := s.InsertSpans(context.Background(), []SpanRow{span, child}); err != nil {
		t.Fatal(err)
	}
	var tokens int64
	if err := s.db.QueryRow(`SELECT SUM(input_tokens+output_tokens) FROM genai_spans`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 220 {
		t.Fatalf("tokens = %d, want 220", tokens)
	}
}

func TestHistoricalChatUsageSuppressedOnOpen(t *testing.T) {
	s := openStore(t)
	ts := time.Now().UTC()
	ctx := context.Background()
	chat := SpanRow{ReceivedAt: ts, Time: ts, TraceID: "old-chat", SpanID: "root", ProviderName: "openai", RequestModel: "gpt-5.1", InputTokens: 1000, OutputTokens: 100, Attributes: map[string]string{"wingman.operation.name": "chat", "gen_ai.usage.input_tokens": "1000"}}
	gateway := chat
	gateway.TraceID = "gateway"
	gateway.Attributes = map[string]string{"gen_ai.provider.name": "openai"}
	if err := s.InsertSpans(ctx, []SpanRow{chat, gateway}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertGenAIMetrics(ctx, []GenAIMetricRow{{ReceivedAt: ts, Time: ts, MetricName: "gen_ai.client.token.usage", TokenType: "input", Count: 1, Sum: 1000, Attributes: map[string]string{"gen_ai.system": "openai"}}}); err != nil {
		t.Fatal(err)
	}
	// Run the startup migrations twice to verify the policy is idempotent.
	for range 2 {
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	var tokens int64
	if err := s.db.QueryRow(`SELECT SUM(input_tokens+output_tokens) FROM genai_spans`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 1100 {
		t.Fatalf("tokens = %d, want gateway's 1100", tokens)
	}
	var name string
	if err := s.db.QueryRow(`SELECT metric_name FROM genai_metrics`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "wingman.chat.client.token.usage" {
		t.Fatalf("metric name = %s", name)
	}
}
