package ingest

import (
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"insights/internal/store"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TracesHandler ingests OTLP trace exports and stores GenAI spans (any span
// carrying a gen_ai.* attribute). Other spans (plain HTTP, fetch
// instrumentation, ...) are acknowledged but dropped — HTTP visibility comes
// from metrics.
type TracesHandler struct {
	store *store.Store
}

func NewTracesHandler(s *store.Store) *TracesHandler {
	return &TracesHandler{store: s}
}

func (h *TracesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req := &coltrace.ExportTraceServiceRequest{}
	ct, err := decodeOTLP(r, req)
	if err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}

	rows := extractSpans(req)
	if err := h.store.InsertSpans(r.Context(), rows); err != nil {
		log.Printf("insert spans: %v", err)
		http.Error(w, "store spans: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(rows) > 0 {
		log.Printf("ingested %d genai spans", len(rows))
	}

	writeOTLP(w, ct, &coltrace.ExportTraceServiceResponse{})
}

var spanKinds = map[tracepb.Span_SpanKind]string{
	tracepb.Span_SPAN_KIND_INTERNAL: "internal",
	tracepb.Span_SPAN_KIND_SERVER:   "server",
	tracepb.Span_SPAN_KIND_CLIENT:   "client",
	tracepb.Span_SPAN_KIND_PRODUCER: "producer",
	tracepb.Span_SPAN_KIND_CONSUMER: "consumer",
}

func extractSpans(req *coltrace.ExportTraceServiceRequest) []store.SpanRow {
	now := time.Now().UTC()
	var rows []store.SpanRow

	for _, rs := range req.ResourceSpans {
		serviceName := ""
		if rs.Resource != nil {
			serviceName = getStringAttr(rs.Resource.Attributes, "service.name")
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if !hasGenAIAttr(sp.Attributes) {
					continue
				}
				rows = append(rows, extractSpan(sp, serviceName, now))
			}
		}
	}
	return rows
}

func hasGenAIAttr(attrs []*common.KeyValue) bool {
	for _, kv := range attrs {
		if strings.HasPrefix(kv.Key, "gen_ai.") {
			return true
		}
	}
	return false
}

func extractSpan(sp *tracepb.Span, serviceName string, now time.Time) store.SpanRow {
	attrs := sp.Attributes

	// gen_ai.conversation.id is the semconv-standard correlation id; session.id
	// (general) and mcp.session.id (MCP servers) are fallbacks.
	session := firstStringAttr(attrs, "gen_ai.conversation.id", "session.id", "mcp.session.id")

	status := ""
	switch sp.Status.GetCode() {
	case tracepb.Status_STATUS_CODE_OK:
		status = "ok"
	case tracepb.Status_STATUS_CODE_ERROR:
		status = "error"
	}

	start := tsFromNano(sp.StartTimeUnixNano)
	var duration float64
	if sp.EndTimeUnixNano > sp.StartTimeUnixNano {
		duration = time.Duration(sp.EndTimeUnixNano - sp.StartTimeUnixNano).Seconds()
	}

	return store.SpanRow{
		ReceivedAt:    now,
		Time:          start,
		Duration:      duration,
		TraceID:       hex.EncodeToString(sp.TraceId),
		SpanID:        hex.EncodeToString(sp.SpanId),
		ParentSpanID:  hex.EncodeToString(sp.ParentSpanId),
		Name:          sp.Name,
		Kind:          spanKinds[sp.Kind],
		Status:        status,
		AppID:         appID(attrs, serviceName),
		OperationName: getStringAttr(attrs, "gen_ai.operation.name"),
		ProviderName:  getStringAttr(attrs, "gen_ai.provider.name"),
		RequestModel:  getStringAttr(attrs, "gen_ai.request.model"),
		ResponseModel: getStringAttr(attrs, "gen_ai.response.model"),
		AgentName:     getStringAttr(attrs, "gen_ai.agent.name"),
		ToolName:      getStringAttr(attrs, "gen_ai.tool.name"),
		UserID:        getStringAttr(attrs, "user.id"),
		UserEmail:     getStringAttr(attrs, "user.email"),
		SessionID:     session,
		ErrorType:     getStringAttr(attrs, "error.type"),
		FinishReasons: getStringArrayAttr(attrs, "gen_ai.response.finish_reasons"),
		// gen_ai.usage.input_tokens is the inclusive prompt total; the cache
		// counts below are subsets of it (priced separately at query time).
		InputTokens:   getIntAttr(attrs, "gen_ai.usage.input_tokens"),
		OutputTokens:  getIntAttr(attrs, "gen_ai.usage.output_tokens"),
		CacheRead:     getIntAttr(attrs, "gen_ai.usage.cache_read.input_tokens"),
		CacheCreation: getIntAttr(attrs, "gen_ai.usage.cache_creation.input_tokens"),
		Reasoning:     getIntAttr(attrs, "gen_ai.usage.reasoning.output_tokens"),
		Attributes:    attrsToMap(attrs),
	}
}

func firstStringAttr(attrs []*common.KeyValue, keys ...string) string {
	for _, key := range keys {
		if v := getStringAttr(attrs, key); v != "" {
			return v
		}
	}
	return ""
}

func getStringArrayAttr(attrs []*common.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.Key != key {
			continue
		}
		if av := kv.Value.GetArrayValue(); av != nil {
			var parts []string
			for _, v := range av.Values {
				if s := v.GetStringValue(); s != "" {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, ",")
		}
		return kv.Value.GetStringValue()
	}
	return ""
}
