package ingest

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"insights/internal/store"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	store *store.Store
}

func NewHandler(s *store.Store) *Handler {
	return &Handler{store: s}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req := &colmetrics.ExportMetricsServiceRequest{}
	ct, err := decodeOTLP(r, req)
	if err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}

	genaiRows, httpRows := h.extract(req)

	// One transaction for the whole export: OTLP senders retry the full request
	// on a 5xx, so persisting one family and failing the other would double-count
	// the persisted rows on the retry.
	if err := h.store.InsertMetrics(r.Context(), genaiRows, httpRows); err != nil {
		log.Printf("insert metrics: %v", err)
		http.Error(w, "store metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("ingested %d genai, %d http metric rows", len(genaiRows), len(httpRows))

	writeOTLP(w, ct, &colmetrics.ExportMetricsServiceResponse{})
}

// otlpBodyLimit caps OTLP request bodies across the metrics, traces and discard
// routes.
const otlpBodyLimit = 10 << 20

// decodeOTLP reads and unmarshals an OTLP request body into msg, choosing
// protobuf vs JSON by Content-Type, and returns the Content-Type so the response
// is encoded to match. Gzip transport compression is transparently decoded —
// the OTel Collector's otlphttp exporter compresses by default, and SDKs do when
// OTEL_EXPORTER_OTLP_COMPRESSION=gzip is set. The body limit applies to the
// decompressed bytes, so a compressed body can't expand past it.
func decodeOTLP(r *http.Request, msg proto.Message) (string, error) {
	reader := io.Reader(r.Body)
	if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return "", fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		reader = gz
	}
	// Read one byte past the limit so an oversized body is rejected with a clear
	// error instead of being silently truncated into an unmarshal failure.
	body, err := io.ReadAll(io.LimitReader(reader, otlpBodyLimit+1))
	if err != nil {
		return "", err
	}
	if len(body) > otlpBodyLimit {
		return "", fmt.Errorf("body exceeds %d byte limit", otlpBodyLimit)
	}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "protobuf") {
		return ct, proto.Unmarshal(body, msg)
	}
	return ct, protojson.Unmarshal(body, msg)
}

// writeOTLP marshals an OTLP response in the same encoding as the request.
func writeOTLP(w http.ResponseWriter, ct string, resp proto.Message) {
	if strings.Contains(ct, "protobuf") {
		out, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(out)
		return
	}
	out, _ := protojson.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func (h *Handler) extract(req *colmetrics.ExportMetricsServiceRequest) ([]store.GenAIMetricRow, []store.HTTPMetricRow) {
	now := time.Now().UTC()
	var genaiRows []store.GenAIMetricRow
	var httpRows []store.HTTPMetricRow

	for _, rm := range req.ResourceMetrics {
		serviceName := getResourceServiceName(rm.Resource)

		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				name := m.Name
				switch {
				case strings.HasPrefix(name, "gen_ai."):
					genaiRows = append(genaiRows, extractGenAI(m, serviceName, now)...)
				case strings.HasPrefix(name, "http.client."), strings.HasPrefix(name, "http.server."):
					httpRows = append(httpRows, extractHTTP(m, serviceName, now)...)
				}
			}
		}
	}
	return genaiRows, httpRows
}

// --- shared helpers ---

type dpFunc func(attrs []*common.KeyValue, ts time.Time, count int64, sum float64)

func iterateDataPoints(m *metrics.Metric, fn dpFunc) {
	switch d := m.Data.(type) {
	case *metrics.Metric_Histogram:
		warnCumulative(m.Name, d.Histogram.AggregationTemporality)
		for _, dp := range d.Histogram.DataPoints {
			fn(dp.Attributes, tsFromNano(dp.TimeUnixNano), int64(dp.Count), dp.GetSum())
		}
	case *metrics.Metric_Sum:
		warnCumulative(m.Name, d.Sum.AggregationTemporality)
		for _, dp := range d.Sum.DataPoints {
			fn(dp.Attributes, tsFromNano(dp.TimeUnixNano), 1, asFloat(dp))
		}
	case *metrics.Metric_Gauge:
		for _, dp := range d.Gauge.DataPoints {
			fn(dp.Attributes, tsFromNano(dp.TimeUnixNano), 1, asFloat(dp))
		}
	default:
		// ExponentialHistogram / Summary aren't handled; log so the data loss is
		// visible rather than silent (the wingman emitter uses explicit-bucket
		// histograms, so this is a guard for other OTLP senders).
		log.Printf("ingest: unhandled metric data type %T for %q", m.Data, m.Name)
	}
}

// warnedCumulative dedupes the temporality warning to one line per metric name.
var warnedCumulative sync.Map

// warnCumulative flags the one alignment assumption between this ingest and the
// wingman gateway that can't be enforced in code: the store aggregates metric
// data points by SUM() over time windows, which is only correct for DELTA
// temporality. wingman installs a delta selector on its insights exporter
// (pkg/otel/otel_meter.go), so this never fires in the intended setup; if some
// other sender (or a misconfigured exporter) delivers CUMULATIVE histograms or
// counters, each export restates the running total and every count/sum query
// over-reports. Log once per metric so the misconfiguration is visible.
func warnCumulative(name string, t metrics.AggregationTemporality) {
	if t != metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		return
	}
	if _, seen := warnedCumulative.LoadOrStore(name, struct{}{}); seen {
		return
	}
	log.Printf("ingest: metric %q uses CUMULATIVE aggregation temporality; the store aggregates data points as DELTA, so its counts/sums will be over-reported — configure the exporter to send delta temporality to the insights endpoint", name)
}

func asFloat(dp *metrics.NumberDataPoint) float64 {
	switch v := dp.Value.(type) {
	case *metrics.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metrics.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

func tsFromNano(ns uint64) time.Time {
	if ns == 0 {
		return time.Now().UTC()
	}
	return time.Unix(0, int64(ns)).UTC()
}

func getResourceServiceName(res *resourcepb.Resource) string {
	if res == nil {
		return ""
	}
	return getStringAttr(res.Attributes, "service.name")
}

// appID resolves the calling application's identity for the app_id column: the
// gateway-stamped service.peer.name (the client's OAuth azp/appid) when present,
// otherwise the resource service.name. The peer is only set for OIDC auth that
// yields an azp/appid claim (e.g. Entra); header/static/anonymous auth and
// non-Entra apps carry no peer, so without this fallback those rows would be
// unattributed in the App filter. service.name is then the gateway's own
// resource name ("wingman" or TELEMETRY_NAME), or — for a directly-instrumented
// app exporting its own telemetry — that app's name.
func appID(attrs []*common.KeyValue, serviceName string) string {
	if peer := getStringAttr(attrs, "service.peer.name"); peer != "" {
		return peer
	}
	return serviceName
}

func getStringAttr(attrs []*common.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.Key == key {
			if sv := kv.Value.GetStringValue(); sv != "" {
				return sv
			}
		}
	}
	return ""
}

// getIntAttr reads an integer attribute, tolerating int, double (truncated) and
// numeric-string encodings — some OTLP senders promote integers to doubles or
// strings in transit, and a silent 0 would drop token counts / status codes.
func getIntAttr(attrs []*common.KeyValue, key string) int64 {
	for _, kv := range attrs {
		if kv.Key != key {
			continue
		}
		switch v := kv.Value.GetValue().(type) {
		case *common.AnyValue_IntValue:
			return v.IntValue
		case *common.AnyValue_DoubleValue:
			return int64(v.DoubleValue)
		case *common.AnyValue_StringValue:
			if n, err := strconv.ParseInt(v.StringValue, 10, 64); err == nil {
				return n
			}
		}
		return 0
	}
	return 0
}

func attrsToMap(attrs []*common.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		// Most OTel attributes are already strings; skip the reflection-based
		// Sprintf for that common case, which runs once per attribute per row.
		if sv, ok := kv.Value.GetValue().(*common.AnyValue_StringValue); ok {
			m[kv.Key] = sv.StringValue
			continue
		}
		m[kv.Key] = fmt.Sprintf("%v", valueToInterface(kv.Value))
	}
	return m
}

func valueToInterface(v *common.AnyValue) any {
	if v == nil {
		return nil
	}
	switch val := v.Value.(type) {
	case *common.AnyValue_StringValue:
		return val.StringValue
	case *common.AnyValue_IntValue:
		return val.IntValue
	case *common.AnyValue_DoubleValue:
		return val.DoubleValue
	case *common.AnyValue_BoolValue:
		return val.BoolValue
	case *common.AnyValue_ArrayValue:
		items := make([]any, len(val.ArrayValue.Values))
		for i, item := range val.ArrayValue.Values {
			items[i] = valueToInterface(item)
		}
		return items
	case *common.AnyValue_KvlistValue:
		m := make(map[string]any, len(val.KvlistValue.Values))
		for _, kv := range val.KvlistValue.Values {
			m[kv.Key] = valueToInterface(kv.Value)
		}
		return m
	}
	return nil
}
