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
	dur      = flag.Duration("duration", 70*24*time.Hour, "how far back to generate data")
	step     = flag.Duration("interval", time.Hour, "time between data points")
	numUsers = flag.Int("users", 120, "number of distinct end users")
)

type modelDef struct {
	provider string
	model    string
	avgIn    float64
	avgOut   float64
	reasons  bool // emits reasoning (thinking) tokens as a subset of output
}

// userDef is one synthetic end user. joinAt/churnAt give cohort retention shape;
// intensity drives how often the user is active and how many requests they send,
// which in turn places them in an engagement segment.
type userDef struct {
	id        string
	email     string
	service   string // calling application (service.peer.name)
	joinAt    time.Time
	churnAt   time.Time // zero => never churns
	intensity float64   // 0..1
}

var (
	services  = []string{"chat-api", "agent-service", "embedding-worker"}
	modelDefs = []modelDef{
		{"openai", "gpt-4o", 800, 400, false},                      // 0 premium
		{"openai", "gpt-4o-mini", 500, 250, false},                 // 1 cheap
		{"anthropic", "claude-sonnet-4-20250514", 1200, 600, true}, // 2 premium
		{"anthropic", "claude-haiku-4-5", 400, 200, false},         // 3 cheap
		{"gcp.gemini", "gemini-2.0-flash", 600, 300, true},         // 4 cheap
	}
	premiumModels = []int{0, 2}
	cheapModels   = []int{1, 3, 4}
	firstNames    = []string{
		"alice", "bob", "carol", "dave", "eve", "frank", "grace", "heidi",
		"ivan", "judy", "mallory", "niaj", "olivia", "peggy", "rupert", "sybil",
		"trent", "victor", "walter", "wendy", "xena", "yuri", "zoe",
	}
	httpRoutes = []string{"/v1/chat/completions", "/v1/embeddings", "/v1/completions", "/api/agents/invoke"}
	errorCodes = []int{400, 401, 403, 404, 429, 500, 502, 503}

	users []userDef
)

// spike windows make consumption anomalies detectable in demo data: one user
// going wild, and one service-wide surge. user-001/user-002 are pinned active
// below so the spikes always land on a live user.
type spikeDef struct {
	user    string // empty = all users
	service string // empty = all services
	offset  float64
	length  time.Duration
	factor  float64
}

var spikes = []spikeDef{
	{user: "user-001", offset: 0.82, length: 2 * time.Hour, factor: 8},
	{service: "chat-api", offset: 0.92, length: 5 * time.Hour, factor: 8},
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
	users = genUsers(start, now, *numUsers)

	var total, traces int
	for t := start; t.Before(now); t = t.Add(*step) {
		active := activeUsers(t)

		if req := buildBatch(t, active); len(req.ResourceMetrics) > 0 {
			if err := send(req); err != nil {
				log.Fatalf("send at %s: %v", t.Format(time.RFC3339), err)
			}
			total++
		}

		// Agent traces (invoke_agent → chat → tool) carry the per-request cache
		// breakdown and materialized cost that the cost/anomaly views read.
		treq := buildTraces(t, active)
		if len(treq.ResourceSpans) > 0 {
			if err := sendTraces(treq); err != nil {
				log.Fatalf("send traces at %s: %v", t.Format(time.RFC3339), err)
			}
			traces++
		}
	}

	// A runaway agent loop: one user firing dozens of requests within two
	// minutes — the signal the Anomalies burst panel and cost anomalies surface.
	if err := sendTraces(emitBurst()); err != nil {
		log.Fatalf("send burst: %v", err)
	}

	log.Printf("sent %d metric batches and %d trace batches (%d users over %s)", total, traces, len(users), *dur)
}

// genUsers builds a population with a power-law engagement mix and staggered
// signup weeks (cohorts) with partial churn, so segmentation and retention have
// shape. user-001 and user-002 are pinned as always-on power users that the
// spikes and burst target.
func genUsers(start, end time.Time, n int) []userDef {
	if n < 2 {
		n = 2
	}
	weeks := int(end.Sub(start).Hours()/(24*7)) + 1
	out := make([]userDef, 0, n)
	for i := 0; i < n; i++ {
		var intensity float64
		switch r := rand.Float64(); {
		case r < 0.05:
			intensity = 0.7 + rand.Float64()*0.3 // power
		case r < 0.20:
			intensity = 0.35 + rand.Float64()*0.25 // frequent
		case r < 0.50:
			intensity = 0.10 + rand.Float64()*0.15 // regular
		default:
			intensity = 0.01 + rand.Float64()*0.06 // casual
		}

		// Older cohorts are a touch larger (squared bias toward week 0).
		joinWeek := int(float64(weeks) * rand.Float64() * rand.Float64())
		joinAt := start.Add(time.Duration(joinWeek) * 7 * 24 * time.Hour)

		var churnAt time.Time
		if rand.Float64() < 0.45 {
			life := 1 + rand.IntN(weeks)
			churnAt = joinAt.Add(time.Duration(life) * 7 * 24 * time.Hour)
		}

		name := fmt.Sprintf("user%d", i+1)
		if i < len(firstNames) {
			name = firstNames[i]
		}
		out = append(out, userDef{
			id:        fmt.Sprintf("user-%03d", i+1),
			email:     name + "@example.com",
			service:   services[rand.IntN(len(services))],
			joinAt:    joinAt,
			churnAt:   churnAt,
			intensity: intensity,
		})
	}

	// Pin two always-on power users for the spike / burst windows.
	out[0] = userDef{id: "user-001", email: "alice@example.com", service: "chat-api", joinAt: start, intensity: 0.92}
	out[1] = userDef{id: "user-002", email: "bob@example.com", service: "agent-service", joinAt: start, intensity: 0.85}
	return out
}

func activeUsers(t time.Time) []userDef {
	dayFactor := 0.15
	switch h := t.Hour(); {
	case h >= 9 && h <= 17:
		dayFactor = 1.0
	case h >= 6 && h <= 22:
		dayFactor = 0.5
	}
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		dayFactor *= 0.4
	}

	var out []userDef
	for _, u := range users {
		if t.Before(u.joinAt) {
			continue
		}
		if !u.churnAt.IsZero() && !t.Before(u.churnAt) {
			continue
		}
		if rand.Float64() < u.intensity*dayFactor {
			out = append(out, u)
		}
	}
	return out
}

// reqMult is how many requests an active user sends in one step — heavier for
// engaged users, so request rate (and thus segment) tracks intensity.
func reqMult(u userDef) int {
	return 1 + rand.IntN(1+int(u.intensity*8))
}

func opFor(svc string, m modelDef) string {
	switch {
	case svc == "embedding-worker":
		return "embeddings"
	case m.provider == "gcp.gemini":
		return "generate_content"
	default:
		return "chat"
	}
}

// modelFor biases model choice by engagement: power users lean premium, casual
// users lean cheap — so model-preference-by-segment shows a real split.
func modelFor(u userDef) modelDef {
	switch {
	case u.intensity > 0.55 && rand.Float64() < 0.7:
		return modelDefs[premiumModels[rand.IntN(len(premiumModels))]]
	case u.intensity < 0.2 && rand.Float64() < 0.7:
		return modelDefs[cheapModels[rand.IntN(len(cheapModels))]]
	default:
		return modelDefs[rand.IntN(len(modelDefs))]
	}
}

func sessionFor(u userDef, t time.Time) string {
	// Sessions rotate roughly every two hours per user.
	return fmt.Sprintf("session-%s-%d", u.id, t.Unix()/7200)
}

func buildBatch(t time.Time, active []userDef) *colmetrics.ExportMetricsServiceRequest {
	tsNano := uint64(t.UnixNano())
	bySvc := map[string][]*metrics.Metric{}

	for _, u := range active {
		svc := u.service
		m := modelFor(u)
		op := opFor(svc, m)
		factor := spikeFactor(t, svc, u.id)
		count := uint64(reqMult(u))
		outputTokens := jitter(m.avgOut, 0.5) * factor
		opDur := jitter(1.5, 0.6)
		sessionID := sessionFor(u, t)

		// token.usage carries the inclusive input/output totals (cache is a subset
		// of input); the per-request cache/reasoning split lives on spans.
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
			// service.peer.name is the calling app on the metric data point (the
			// gateway stamps it); the resource service.name below is the gateway.
			kv("service.peer.name", svc),
		}

		bySvc[svc] = append(bySvc[svc],
			histo("gen_ai.client.token.usage", tsNano, count, inputTokens*float64(count),
				inputTokens*0.3, inputTokens*2.0, withAttr(baseAttrs, kv("gen_ai.token.type", "input"))...),
			histo("gen_ai.client.token.usage", tsNano, count, outputTokens*float64(count),
				outputTokens*0.3, outputTokens*2.0, withAttr(baseAttrs, kv("gen_ai.token.type", "output"))...),
			histo("gen_ai.client.operation.duration", tsNano, count, opDur*float64(count),
				opDur*0.2, opDur*3.0, baseAttrs...),
		)
		if op == "chat" {
			ttfc := jitter(0.35, 0.6)
			bySvc[svc] = append(bySvc[svc], histo("gen_ai.client.operation.time_to_first_chunk",
				tsNano, count, ttfc*float64(count), ttfc*0.3, ttfc*2.5, baseAttrs...))
		}
	}

	// HTTP metrics are operational and emitted for every service each step.
	for _, svc := range services {
		bySvc[svc] = append(bySvc[svc], httpMetrics(tsNano)...)
	}

	var rms []*metrics.ResourceMetrics
	for _, svc := range services {
		ms := bySvc[svc]
		if len(ms) == 0 {
			continue
		}
		rms = append(rms, &metrics.ResourceMetrics{
			Resource: &resource.Resource{
				// The gateway emits the metrics; the calling app is service.peer.name
				// on each data point (see baseAttrs), not the resource service.name.
				Attributes: []*common.KeyValue{kv("service.name", "wingman")},
			},
			ScopeMetrics: []*metrics.ScopeMetrics{{
				Scope:   &common.InstrumentationScope{Name: "github.com/adrianliechti/wingman"},
				Metrics: ms,
			}},
		})
	}
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: rms}
}

// httpMetrics emits inbound (server) and outbound (client) HTTP request
// histograms for one interval, with an occasional error mixed in.
func httpMetrics(tsNano uint64) []*metrics.Metric {
	var out []*metrics.Metric
	for _, route := range httpRoutes {
		count := uint64(rand.IntN(50) + 5)
		lat := jitter(0.15, 0.8)
		status := 200
		var errType string
		if rand.Float64() < 0.08 {
			status = pick(errorCodes)
			errType = fmt.Sprintf("%d", status)
		}
		out = append(out, histo("http.server.request.duration", tsNano, count,
			lat*float64(count), lat*0.1, lat*4.0,
			kv("http.request.method", "POST"), kv("http.route", route),
			kvInt("http.response.status_code", int64(status)), kv("url.scheme", "https"),
			kv("error.type", errType)))
		if status >= 400 {
			okCount := uint64(rand.IntN(40) + 10)
			okLat := jitter(0.12, 0.5)
			out = append(out, histo("http.server.request.duration", tsNano, okCount,
				okLat*float64(okCount), okLat*0.1, okLat*3.0,
				kv("http.request.method", "POST"), kv("http.route", route),
				kvInt("http.response.status_code", 200), kv("url.scheme", "https"),
				kv("error.type", "")))
		}
	}
	for _, m := range modelDefs {
		count := uint64(rand.IntN(15) + 1)
		lat := jitter(0.8, 0.5)
		status := int64(200)
		var errType string
		if rand.Float64() < 0.03 {
			status = int64(pick([]int{429, 500, 502, 503}))
			errType = fmt.Sprintf("%d", status)
		}
		out = append(out, histo("http.client.request.duration", tsNano, count,
			lat*float64(count), lat*0.2, lat*3.0,
			kv("http.request.method", "POST"), kv("server.address", fmt.Sprintf("api.%s.com", m.provider)),
			kvInt("http.response.status_code", status), kv("url.scheme", "https"),
			kv("error.type", errType)))
	}
	return out
}

var demoTools = []string{"web_search", "retrieve_documents", "execute_code", "summarize"}

func randID(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return b
}

// buildTraces emits one agent-style trace per active user: invoke_agent →
// chat {model} → execute_tool {tool}, under the user's primary service.
func buildTraces(t time.Time, active []userDef) *coltrace.ExportTraceServiceRequest {
	var rms []*tracepb.ResourceSpans
	for _, u := range active {
		spans := buildUserTrace(t, u, u.service)
		if len(spans) == 0 {
			continue
		}
		rms = append(rms, &tracepb.ResourceSpans{
			Resource: &resource.Resource{
				Attributes: []*common.KeyValue{kv("service.name", u.service)},
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
	m := modelFor(u)
	sessionID := sessionFor(u, t)

	traceID := randID(16)
	rootID := randID(8)
	factor := spikeFactor(t, svc, u.id)

	userAttrs := []*common.KeyValue{
		kv("user.id", u.id),
		kv("user.email", u.email),
		kv("gen_ai.conversation.id", sessionID),
		kv("service.peer.name", svc),
	}

	rootStart := t
	cursor := rootStart
	var spans []*tracepb.Span

	// Engaged users send more chats per agent invocation.
	chatCount := reqMult(u)
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
		cacheCreation, cacheRead := cacheTokens(m, "chat", m.avgIn*factor)
		inTok := int64(jitter(m.avgIn, 0.5)*factor) + int64(cacheCreation) + int64(cacheRead)
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

// emitBurst is a runaway agent loop: user-002 firing ~80 chat requests ~1.5s
// apart inside one session — a request-rate burst plus a cost/token spike.
func emitBurst() *coltrace.ExportTraceServiceRequest {
	u := users[1]
	svc := u.service
	m := modelDefs[2] // claude-sonnet (premium → a clear cost spike)
	burstTime := demoEnd.Add(-36 * time.Hour)
	traceID := randID(16)
	rootID := randID(8)
	session := fmt.Sprintf("session-%s-runaway", u.id)
	userAttrs := []*common.KeyValue{
		kv("user.id", u.id),
		kv("user.email", u.email),
		kv("gen_ai.conversation.id", session),
		kv("service.peer.name", svc),
	}

	cursor := burstTime
	var spans []*tracepb.Span
	for range 80 {
		chatID := randID(8)
		dur := time.Duration(1200 * float64(time.Millisecond))
		inTok := int64(jitter(m.avgIn, 0.2))
		outTok := int64(jitter(m.avgOut, 0.2))
		spans = append(spans, &tracepb.Span{
			TraceId:           traceID,
			SpanId:            chatID,
			ParentSpanId:      rootID,
			Name:              "chat " + m.model,
			Kind:              tracepb.Span_SPAN_KIND_CLIENT,
			StartTimeUnixNano: uint64(cursor.UnixNano()),
			EndTimeUnixNano:   uint64(cursor.Add(dur).UnixNano()),
			Attributes: append([]*common.KeyValue{
				kv("gen_ai.operation.name", "chat"),
				kv("gen_ai.provider.name", m.provider),
				kv("gen_ai.request.model", m.model),
				kv("gen_ai.response.model", m.model),
				kvInt("gen_ai.usage.input_tokens", inTok),
				kvInt("gen_ai.usage.output_tokens", outTok),
			}, userAttrs...),
			Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
		})
		cursor = cursor.Add(1500 * time.Millisecond)
	}

	root := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            rootID,
		Name:              "invoke_agent assistant",
		Kind:              tracepb.Span_SPAN_KIND_INTERNAL,
		StartTimeUnixNano: uint64(burstTime.UnixNano()),
		EndTimeUnixNano:   uint64(cursor.UnixNano()),
		Attributes: append([]*common.KeyValue{
			kv("gen_ai.operation.name", "invoke_agent"),
			kv("gen_ai.provider.name", "wingman"),
			kv("gen_ai.agent.name", "assistant"),
		}, userAttrs...),
		Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
	}
	spans = append([]*tracepb.Span{root}, spans...)
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{kv("service.name", svc)}},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &common.InstrumentationScope{Name: "github.com/adrianliechti/wingman"},
			Spans: spans,
		}},
	}}}
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
