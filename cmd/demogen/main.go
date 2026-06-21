package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

var (
	endpoint = flag.String("endpoint", "http://localhost:4318/v1/metrics", "OTLP metrics endpoint")
	dur      = flag.Duration("duration", 24*time.Hour, "how far back to generate data")
	step     = flag.Duration("interval", 5*time.Minute, "time between data points")
)

type modelDef struct {
	provider string
	model    string
	avgIn    float64
	avgOut   float64
	reasons  bool // emits reasoning (thinking) tokens as a subset of output
}

type userDef struct {
	id    string
	email string
}

var (
	services  = []string{"chat-api", "agent-service", "embedding-worker"}
	modelDefs = []modelDef{
		{"openai", "gpt-4o", 800, 400, false},
		{"openai", "gpt-4o-mini", 500, 250, false},
		{"anthropic", "claude-sonnet-4-20250514", 1200, 600, true},
		{"anthropic", "claude-haiku-4-5", 400, 200, false},
		{"gcp.gemini", "gemini-2.0-flash", 600, 300, true},
	}
	users = []userDef{
		{"user-001", "alice@example.com"},
		{"user-002", "bob@example.com"},
		{"user-003", "carol@example.com"},
		{"user-004", "dave@example.com"},
		{"user-005", "eve@example.com"},
		{"user-006", "frank@example.com"},
		{"user-007", "grace@example.com"},
		{"user-008", "heidi@example.com"},
	}
	operations = []string{"chat", "embeddings", "generate_content"}
	httpRoutes = []string{"/v1/chat/completions", "/v1/embeddings", "/v1/completions", "/api/agents/invoke"}
	errorCodes = []int{400, 401, 403, 404, 429, 500, 502, 503}
)

// spike windows make token consumption anomalies detectable in demo data:
// one user going wild for an hour, and one service-wide surge.
type spikeDef struct {
	user    string  // empty = all users
	service string  // empty = all services
	offset  float64 // fraction of -duration..now where the spike starts
	length  time.Duration
	factor  float64
}

var spikes = []spikeDef{
	{user: "user-003", offset: 0.70, length: time.Hour, factor: 9},
	{service: "chat-api", offset: 0.45, length: 45 * time.Minute, factor: 5},
}

var demoStart, demoEnd time.Time

func spikeFactor(t time.Time, svc, user string) float64 {
	factor := 1.0
	total := demoEnd.Sub(demoStart)
	for _, s := range spikes {
		begin := demoStart.Add(time.Duration(s.offset * float64(total)))
		if t.Before(begin) || t.After(begin.Add(s.length)) {
			continue
		}
		if s.user != "" && s.user != user {
			continue
		}
		if s.service != "" && s.service != svc {
			continue
		}
		factor *= s.factor
	}
	return factor
}

func main() {
	flag.Parse()
	now := time.Now().UTC()
	start := now.Add(-*dur)
	demoStart, demoEnd = start, now

	var total, traces int
	for t := start; t.Before(now); t = t.Add(*step) {
		req := buildBatch(t)
		if err := send(req); err != nil {
			log.Fatalf("send at %s: %v", t.Format(time.RFC3339), err)
		}
		total++

		// Emit agent traces (agent → chat → tool) per active user. Spans carry
		// the per-request cache breakdown that cost/cache analytics read from.
		treq := buildTraces(t)
		if len(treq.ResourceSpans) > 0 {
			if err := sendTraces(treq); err != nil {
				log.Fatalf("send traces at %s: %v", t.Format(time.RFC3339), err)
			}
			traces++
		}
	}
	log.Printf("sent %d metric batches and %d trace batches covering %s", total, traces, *dur)
}

func activeUsers(t time.Time) []userDef {
	hour := t.Hour()
	// Simulate varying activity: more users during business hours
	var active int
	switch {
	case hour >= 9 && hour <= 17:
		active = 5 + rand.IntN(4) // 5-8 users
	case hour >= 6 && hour <= 22:
		active = 2 + rand.IntN(4) // 2-5 users
	default:
		active = rand.IntN(3) // 0-2 users at night
	}
	if active > len(users) {
		active = len(users)
	}
	if active == 0 {
		return nil
	}
	// Shuffle and take first N
	perm := rand.Perm(len(users))
	result := make([]userDef, active)
	for i := 0; i < active; i++ {
		result[i] = users[perm[i]]
	}
	return result
}

func buildBatch(t time.Time) *colmetrics.ExportMetricsServiceRequest {
	tsNano := uint64(t.UnixNano())
	var rms []*metrics.ResourceMetrics
	currentUsers := activeUsers(t)

	for _, svc := range services {
		var allMetrics []*metrics.Metric

		for _, m := range modelDefs {
			op := pick(operations)
			// Only certain operations for certain models
			if m.provider == "gcp.gemini" {
				op = "generate_content"
			}
			if svc == "embedding-worker" {
				op = "embeddings"
			}

			for _, u := range currentUsers {
				factor := spikeFactor(t, svc, u.id)
				outputTokens := jitter(m.avgOut, 0.5) * factor
				count := uint64(rand.IntN(10) + 1)
				opDur := jitter(1.5, 0.6)
				// Sessions rotate roughly every two hours per user.
				sessionID := fmt.Sprintf("session-%s-%d", u.id, t.Unix()/7200)

				// The token.usage histogram carries the inclusive input/output
				// totals (cache is a subset of input, matching wingman). The
				// per-request cache and reasoning breakdown lives on spans
				// (gen_ai.usage.*), not metrics.
				cacheCreation, cacheRead := cacheTokens(m, op, m.avgIn*factor)
				inputTokens := jitter(m.avgIn, 0.5)*factor + cacheCreation + cacheRead

				baseAttrs := []*common.KeyValue{
					kv("gen_ai.operation.name", op),
					kv("gen_ai.provider.name", m.provider),
					kv("gen_ai.request.model", m.model),
					kv("gen_ai.response.model", m.model),
					kv("user.id", u.id),
					kv("user.email", u.email),
					kv("gen_ai.conversation.id", sessionID),
				}

				allMetrics = append(allMetrics, histo(
					"gen_ai.client.token.usage", tsNano, count, inputTokens*float64(count),
					inputTokens*0.3, inputTokens*2.0,
					withAttr(baseAttrs, kv("gen_ai.token.type", "input"))...,
				))
				allMetrics = append(allMetrics, histo(
					"gen_ai.client.token.usage", tsNano, count, outputTokens*float64(count),
					outputTokens*0.3, outputTokens*2.0,
					withAttr(baseAttrs, kv("gen_ai.token.type", "output"))...,
				))

				// Operation duration
				allMetrics = append(allMetrics, histo(
					"gen_ai.client.operation.duration", tsNano, count, opDur*float64(count),
					opDur*0.2, opDur*3.0,
					baseAttrs...,
				))

				// Time to first chunk for streaming chat
				if op == "chat" {
					ttfc := jitter(0.35, 0.6)
					allMetrics = append(allMetrics, histo(
						"gen_ai.client.operation.time_to_first_chunk", tsNano, count,
						ttfc*float64(count), ttfc*0.3, ttfc*2.5,
						baseAttrs...,
					))
				}
			}
		}

		// HTTP server metrics
		for _, route := range httpRoutes {
			count := uint64(rand.IntN(50) + 5)
			lat := jitter(0.15, 0.8)
			status := 200
			var errType string
			if rand.Float64() < 0.08 {
				status = pick(errorCodes)
				errType = fmt.Sprintf("%d", status)
			}
			allMetrics = append(allMetrics, histo(
				"http.server.request.duration", tsNano, count,
				lat*float64(count), lat*0.1, lat*4.0,
				kv("http.request.method", "POST"),
				kv("http.route", route),
				kvInt("http.response.status_code", int64(status)),
				kv("url.scheme", "https"),
				kv("error.type", errType),
			))
			// Also add some successful requests alongside errors
			if status >= 400 {
				okCount := uint64(rand.IntN(40) + 10)
				okLat := jitter(0.12, 0.5)
				allMetrics = append(allMetrics, histo(
					"http.server.request.duration", tsNano, okCount,
					okLat*float64(okCount), okLat*0.1, okLat*3.0,
					kv("http.request.method", "POST"),
					kv("http.route", route),
					kvInt("http.response.status_code", 200),
					kv("url.scheme", "https"),
					kv("error.type", ""),
				))
			}
		}

		// HTTP client metrics (outbound to LLM providers)
		for _, m := range modelDefs {
			count := uint64(rand.IntN(15) + 1)
			lat := jitter(0.8, 0.5)
			status := int64(200)
			var errType string
			if rand.Float64() < 0.03 {
				status = int64(pick([]int{429, 500, 502, 503}))
				errType = fmt.Sprintf("%d", status)
			}
			allMetrics = append(allMetrics, histo(
				"http.client.request.duration", tsNano, count,
				lat*float64(count), lat*0.2, lat*3.0,
				kv("http.request.method", "POST"),
				kv("server.address", fmt.Sprintf("api.%s.com", m.provider)),
				kvInt("http.response.status_code", status),
				kv("url.scheme", "https"),
				kv("error.type", errType),
			))
		}

		rms = append(rms, &metrics.ResourceMetrics{
			Resource: &resource.Resource{
				Attributes: []*common.KeyValue{kv("service.name", svc)},
			},
			ScopeMetrics: []*metrics.ScopeMetrics{{
				Scope:   &common.InstrumentationScope{Name: "github.com/adrianliechti/wingman"},
				Metrics: allMetrics,
			}},
		})
	}
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: rms}
}

var demoTools = []string{"web_search", "retrieve_documents", "execute_code", "summarize"}

func randID(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return b
}

// buildTraces emits one agent-style trace per active user, like wingman-chat:
// invoke_agent → chat {model} → execute_tool {tool}.
func buildTraces(t time.Time) *coltrace.ExportTraceServiceRequest {
	var rms []*tracepb.ResourceSpans
	for _, u := range activeUsers(t) {
		svc := pick(services)
		spans := buildUserTrace(t, u, svc)
		if len(spans) == 0 {
			continue
		}
		rms = append(rms, &tracepb.ResourceSpans{
			Resource: &resource.Resource{
				Attributes: []*common.KeyValue{kv("service.name", svc)},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &common.InstrumentationScope{Name: "github.com/adrianliechti/wingman"},
				Spans: spans,
			}},
		})
	}
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: rms}
}

func buildUserTrace(t time.Time, u userDef, svc string) []*tracepb.Span {
	m := modelDefs[rand.IntN(len(modelDefs))]
	sessionID := fmt.Sprintf("session-%s-%d", u.id, t.Unix()/7200)

	traceID := randID(16)
	rootID := randID(8)
	factor := spikeFactor(t, svc, u.id)

	userAttrs := []*common.KeyValue{
		kv("user.id", u.id),
		kv("user.email", u.email),
		kv("gen_ai.conversation.id", sessionID),
	}

	rootStart := t
	cursor := rootStart
	var spans []*tracepb.Span

	chatCount := 1 + rand.IntN(3)
	for range chatCount {
		chatID := randID(8)
		chatDur := time.Duration(jitter(1.8, 0.6) * float64(time.Second))
		errType := ""
		status := tracepb.Status_STATUS_CODE_OK
		if rand.Float64() < 0.06 {
			errType = pick([]string{"429", "500", "timeout"})
			status = tracepb.Status_STATUS_CODE_ERROR
		}
		outTok := int64(jitter(m.avgOut, 0.5) * factor)
		// Cache reads/creations are a subset of the inclusive input total.
		cacheCreation, cacheRead := cacheTokens(m, "chat", m.avgIn*factor)
		inTok := int64(jitter(m.avgIn, 0.5)*factor) + int64(cacheCreation) + int64(cacheRead)
		// Reasoning is a subset of the inclusive output total.
		var reasoning int64
		if m.reasons && rand.Float64() < 0.6 {
			reasoning = int64(jitter(m.avgOut*0.45, 0.4) * factor)
			if reasoning >= outTok {
				reasoning = outTok * 3 / 4
			}
		}

		attrs := append([]*common.KeyValue{
			kv("gen_ai.operation.name", "chat"),
			kv("gen_ai.provider.name", m.provider),
			kv("gen_ai.request.model", m.model),
			kv("gen_ai.response.model", m.model),
			kvInt("gen_ai.usage.input_tokens", inTok),
			kvInt("gen_ai.usage.output_tokens", outTok),
		}, userAttrs...)
		if cacheRead > 0 {
			attrs = append(attrs, kvInt("gen_ai.usage.cache_read.input_tokens", int64(cacheRead)))
		}
		if cacheCreation > 0 {
			attrs = append(attrs, kvInt("gen_ai.usage.cache_creation.input_tokens", int64(cacheCreation)))
		}
		if reasoning > 0 {
			attrs = append(attrs, kvInt("gen_ai.usage.reasoning.output_tokens", reasoning))
		}
		if errType != "" {
			attrs = append(attrs, kv("error.type", errType))
		}

		spans = append(spans, &tracepb.Span{
			TraceId:           traceID,
			SpanId:            chatID,
			ParentSpanId:      rootID,
			Name:              "chat " + m.model,
			Kind:              tracepb.Span_SPAN_KIND_CLIENT,
			StartTimeUnixNano: uint64(cursor.UnixNano()),
			EndTimeUnixNano:   uint64(cursor.Add(chatDur).UnixNano()),
			Attributes:        attrs,
			Status:            &tracepb.Status{Code: status},
		})

		// Some chats trigger tool executions.
		if rand.Float64() < 0.5 {
			tool := pick(demoTools)
			toolDur := time.Duration(jitter(0.4, 0.7) * float64(time.Second))
			spans = append(spans, &tracepb.Span{
				TraceId:           traceID,
				SpanId:            randID(8),
				ParentSpanId:      chatID,
				Name:              "execute_tool " + tool,
				Kind:              tracepb.Span_SPAN_KIND_INTERNAL,
				StartTimeUnixNano: uint64(cursor.Add(chatDur / 3).UnixNano()),
				EndTimeUnixNano:   uint64(cursor.Add(chatDur/3 + toolDur).UnixNano()),
				Attributes: append([]*common.KeyValue{
					kv("gen_ai.operation.name", "execute_tool"),
					kv("gen_ai.provider.name", m.provider),
					kv("gen_ai.tool.name", tool),
					kv("gen_ai.tool.type", "function"),
				}, userAttrs...),
				Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
			})
		}
		cursor = cursor.Add(chatDur + time.Duration(jitter(0.3, 0.5)*float64(time.Second)))
	}

	root := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            rootID,
		Name:              "invoke_agent assistant",
		Kind:              tracepb.Span_SPAN_KIND_INTERNAL,
		StartTimeUnixNano: uint64(rootStart.UnixNano()),
		EndTimeUnixNano:   uint64(cursor.UnixNano()),
		Attributes: append([]*common.KeyValue{
			kv("gen_ai.operation.name", "invoke_agent"),
			kv("gen_ai.provider.name", "wingman"),
			kv("gen_ai.agent.name", "assistant"),
		}, userAttrs...),
		Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
	}
	return append([]*tracepb.Span{root}, spans...)
}

func sendTraces(req *coltrace.ExportTraceServiceRequest) error {
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.Replace(*endpoint, "/v1/metrics", "/v1/traces", 1)
	resp, err := http.Post(url, "application/x-protobuf", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func send(req *colmetrics.ExportMetricsServiceRequest) error {
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := http.Post(*endpoint, "application/x-protobuf", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// histo emits a delta histogram for one interval — the shape wingman's insights
// metric exporter produces. The datapoint covers (StartTime, Time] = one step,
// so summing datapoints over a window yields the true total.
func histo(name string, tsNano, count uint64, sum, min, max float64, attrs ...*common.KeyValue) *metrics.Metric {
	startNano := tsNano - uint64((*step).Nanoseconds())
	return &metrics.Metric{
		Name: name,
		Data: &metrics.Metric_Histogram{
			Histogram: &metrics.Histogram{
				AggregationTemporality: metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metrics.HistogramDataPoint{{
					Attributes:        attrs,
					StartTimeUnixNano: startNano,
					TimeUnixNano:      tsNano,
					Count:             count,
					Sum:               &sum,
					Min:               &min,
					Max:               &max,
				}},
			},
		},
	}
}

// cacheTokens returns (cacheCreation, cacheRead) for a request — non-zero only
// for Anthropic chat ~30% of the time, mirroring provider-managed prompt caching.
// base is the request's typical input size; both are subsets of it.
func cacheTokens(m modelDef, op string, base float64) (float64, float64) {
	if m.provider != "anthropic" || op != "chat" || rand.Float64() >= 0.3 {
		return 0, 0
	}
	return jitter(base*0.4, 0.3), jitter(base*0.6, 0.3)
}

// withAttr returns a fresh slice of base + extra, never aliasing base's backing
// array (so successive histo() calls don't clobber each other's attributes).
func withAttr(base []*common.KeyValue, extra ...*common.KeyValue) []*common.KeyValue {
	out := make([]*common.KeyValue, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

func kv(key, val string) *common.KeyValue {
	return &common.KeyValue{
		Key:   key,
		Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: val}},
	}
}

func kvInt(key string, val int64) *common.KeyValue {
	return &common.KeyValue{
		Key:   key,
		Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: val}},
	}
}

func jitter(base, spread float64) float64 {
	return base + base*spread*(rand.Float64()*2-1)
}

func pick[T any](items []T) T {
	return items[rand.IntN(len(items))]
}
