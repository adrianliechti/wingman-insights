package ingest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
	"insights/internal/store"
)

func TestGatewayAccountingKeepsChatActivity(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	ns := uint64(now.UnixNano())
	ctx := context.Background()
	post := func(handler http.Handler, message proto.Message) {
		t.Helper()
		body, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/telemetry", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-protobuf")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("ingest: %d %s", res.Code, res.Body.String())
		}
	}
	attrs := func(extra ...*common.KeyValue) []*common.KeyValue {
		return append([]*common.KeyValue{strAttr("user.id", "user-1"), strAttr("gen_ai.request.model", "gpt-5.1")}, extra...)
	}
	number := func(k string, n int64) *common.KeyValue {
		return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: n}}}
	}
	for _, source := range []string{"chat", "gateway"} {
		scope := "github.com/adrianliechti/wingman"
		if source == "chat" {
			scope = "wingman"
		}
		resource := &resourcepb.Resource{Attributes: []*common.KeyValue{strAttr("service.name", "renamed-"+source), strAttr("wingman.telemetry.source", source)}}
		span := &tracepb.Span{TraceId: []byte(source + "-trace-123456789"), SpanId: []byte("model-id"), Name: "chat gpt-5.1", StartTimeUnixNano: ns, EndTimeUnixNano: ns + 1000, Attributes: attrs(strAttr("gen_ai.provider.name", "openai"), number("gen_ai.usage.input_tokens", 1000), number("gen_ai.usage.output_tokens", 100))}
		spans := []*tracepb.Span{span}
		if source == "chat" {
			spans = append(spans, &tracepb.Span{TraceId: span.TraceId, SpanId: []byte("tool-id1"), ParentSpanId: span.SpanId, Name: "execute_tool files_search", StartTimeUnixNano: ns, EndTimeUnixNano: ns + 500, Attributes: attrs(strAttr("gen_ai.operation.name", "execute_tool"), strAttr("gen_ai.tool.name", "files_search"), strAttr("wingman.mcp.server.id", "files"), strAttr("mcp.method.name", "tools/call"))})
		}
		post(NewTracesHandler(s), &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{Resource: resource, ScopeSpans: []*tracepb.ScopeSpans{{Scope: &common.InstrumentationScope{Name: scope}, Spans: spans}}}}})
		sum := 1000.0
		metric := &metrics.Metric{Name: "gen_ai.client.token.usage", Data: &metrics.Metric_Histogram{Histogram: &metrics.Histogram{AggregationTemporality: metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: []*metrics.HistogramDataPoint{{TimeUnixNano: ns, Count: 1, Sum: &sum, Attributes: attrs(strAttr("gen_ai.token.type", "input"))}}}}}
		batch := []*metrics.Metric{metric}
		if source == "chat" {
			score := 0.9
			batch = append(batch, &metrics.Metric{Name: "wingman.classification.score", Data: &metrics.Metric_Histogram{Histogram: &metrics.Histogram{AggregationTemporality: metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: []*metrics.HistogramDataPoint{{TimeUnixNano: ns, Count: 1, Sum: &score, Attributes: attrs(strAttr("wingman.classification.kind", "category"), strAttr("wingman.classification.id", "legal"), &common.KeyValue{Key: "wingman.classification.matched", Value: &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: true}}})}}}}})
		}
		post(NewHandler(s), &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metrics.ResourceMetrics{{Resource: resource, ScopeMetrics: []*metrics.ScopeMetrics{{Scope: &common.InstrumentationScope{Name: scope}, Metrics: batch}}}}})
	}
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	f := store.Filter{User: []string{"user-1"}}
	tokens, err := s.QueryTokenSummary(ctx, from, to, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].TotalTokens != 1000 || tokens[0].TotalRequests != 1 {
		t.Fatalf("gateway metric usage = %+v", tokens)
	}
	costs, err := s.QueryCostBreakdown(ctx, from, to, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != 1 || costs[0].InputTokens != 1000 || costs[0].OutputTokens != 100 || costs[0].TotalCost <= 0 {
		t.Fatalf("gateway span usage = %+v", costs)
	}
	tools, err := s.QueryToolStats(ctx, from, to, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].MCPServer != "files" {
		t.Fatalf("chat tools = %+v", tools)
	}
	classification, err := s.QueryClassificationStats(ctx, from, to, f)
	if err != nil {
		t.Fatal(err)
	}
	if classification.Prompts != 1 || classification.Conversations != 0 || classification.Users != 1 {
		t.Fatalf("classification = %+v", classification)
	}
	traces, err := s.QueryTraceList(ctx, from, to, f, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Fatalf("both emitter traces must remain: %+v", traces)
	}
}

func TestChatResourceCannotClaimGatewayScope(t *testing.T) {
	resource := &resourcepb.Resource{Attributes: []*common.KeyValue{strAttr("wingman.telemetry.source", "chat")}}
	if source := telemetrySource(resource, "github.com/adrianliechti/wingman"); source != "chat" {
		t.Fatalf("forged scope source = %s", source)
	}
}
