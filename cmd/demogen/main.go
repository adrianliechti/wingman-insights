package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
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
}

type userDef struct {
	id    string
	email string
}

var (
	services  = []string{"chat-api", "agent-service", "embedding-worker"}
	modelDefs = []modelDef{
		{"openai", "gpt-4o", 800, 400},
		{"openai", "gpt-4o-mini", 500, 250},
		{"anthropic", "claude-sonnet-4-20250514", 1200, 600},
		{"anthropic", "claude-haiku-4-5", 400, 200},
		{"google", "gemini-2.0-flash", 600, 300},
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

	var total int
	for t := start; t.Before(now); t = t.Add(*step) {
		req := buildBatch(t)
		if err := send(req); err != nil {
			log.Fatalf("send at %s: %v", t.Format(time.RFC3339), err)
		}
		total++
	}
	log.Printf("sent %d batches covering %s", total, *dur)
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
			if m.provider == "google" {
				op = "generate_content"
			}
			if svc == "embedding-worker" {
				op = "embeddings"
			}

			for _, u := range currentUsers {
				factor := spikeFactor(t, svc, u.id)
				inputTokens := jitter(m.avgIn, 0.5) * factor
				outputTokens := jitter(m.avgOut, 0.5) * factor
				count := uint64(rand.IntN(10) + 1)
				opDur := jitter(1.5, 0.6)

				baseAttrs := []*common.KeyValue{
					kv("gen_ai.operation.name", op),
					kv("gen_ai.system", m.provider),
					kv("gen_ai.request.model", m.model),
					kv("gen_ai.response.model", m.model),
					kv("user.id", u.id),
					kv("user.email", u.email),
				}

				// Token usage: input
				allMetrics = append(allMetrics, histo(
					"gen_ai.client.token.usage", tsNano, count, inputTokens*float64(count),
					inputTokens*0.3, inputTokens*2.0,
					append(baseAttrs, kv("gen_ai.token.type", "input"))...,
				))

				// Token usage: output
				allMetrics = append(allMetrics, histo(
					"gen_ai.client.token.usage", tsNano, count, outputTokens*float64(count),
					outputTokens*0.3, outputTokens*2.0,
					append(baseAttrs, kv("gen_ai.token.type", "output"))...,
				))

				// Cache tokens for Anthropic chat operations (~30% of requests)
				if m.provider == "anthropic" && op == "chat" && rand.Float64() < 0.3 {
					cacheTok := jitter(m.avgIn*0.4, 0.3)
					allMetrics = append(allMetrics, histo(
						"gen_ai.client.token.usage", tsNano, count, cacheTok*float64(count),
						cacheTok*0.2, cacheTok*1.5,
						append(baseAttrs, kv("gen_ai.token.type", "cache_creation"))...,
					))
					cacheRead := jitter(m.avgIn*0.6, 0.3)
					allMetrics = append(allMetrics, histo(
						"gen_ai.client.token.usage", tsNano, count, cacheRead*float64(count),
						cacheRead*0.2, cacheRead*1.5,
						append(baseAttrs, kv("gen_ai.token.type", "cache_read"))...,
					))
				}

				// Operation duration
				allMetrics = append(allMetrics, histo(
					"gen_ai.client.operation.duration", tsNano, count, opDur*float64(count),
					opDur*0.2, opDur*3.0,
					baseAttrs...,
				))
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

func histo(name string, tsNano, count uint64, sum, min, max float64, attrs ...*common.KeyValue) *metrics.Metric {
	return &metrics.Metric{
		Name: name,
		Data: &metrics.Metric_Histogram{
			Histogram: &metrics.Histogram{
				AggregationTemporality: metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metrics.HistogramDataPoint{{
					Attributes:   attrs,
					TimeUnixNano: tsNano,
					Count:        count,
					Sum:          &sum,
					Min:          &min,
					Max:          &max,
				}},
			},
		},
	}
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
