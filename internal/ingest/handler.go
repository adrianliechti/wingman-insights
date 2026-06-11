package ingest

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"insights/internal/store"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	req := &colmetrics.ExportMetricsServiceRequest{}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "application/x-protobuf"), strings.Contains(ct, "application/protobuf"):
		if err := proto.Unmarshal(body, req); err != nil {
			http.Error(w, "decode protobuf: "+err.Error(), http.StatusBadRequest)
			return
		}
	default:
		if err := protojson.Unmarshal(body, req); err != nil {
			http.Error(w, "decode json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	genaiRows, httpRows := h.extract(req)

	ctx := r.Context()
	if err := h.store.InsertGenAIMetrics(ctx, genaiRows); err != nil {
		log.Printf("insert genai metrics: %v", err)
		http.Error(w, "store genai: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.InsertHTTPMetrics(ctx, httpRows); err != nil {
		log.Printf("insert http metrics: %v", err)
		http.Error(w, "store http: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("ingested %d genai, %d http metric rows", len(genaiRows), len(httpRows))

	resp := &colmetrics.ExportMetricsServiceResponse{}
	if strings.Contains(ct, "protobuf") {
		out, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(out)
	} else {
		out, _ := protojson.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}
}

func (h *Handler) extract(req *colmetrics.ExportMetricsServiceRequest) ([]store.GenAIMetricRow, []store.HTTPMetricRow) {
	now := time.Now().UTC()
	var genaiRows []store.GenAIMetricRow
	var httpRows []store.HTTPMetricRow

	for _, rm := range req.ResourceMetrics {
		serviceName := getResourceServiceName(rm)

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

type dpFunc func(attrs []*common.KeyValue, ts time.Time, count int64, sum, min, max float64)

func iterateDataPoints(m *metrics.Metric, fn dpFunc) {
	switch d := m.Data.(type) {
	case *metrics.Metric_Histogram:
		for _, dp := range d.Histogram.DataPoints {
			ts := tsFromNano(dp.TimeUnixNano)
			fn(dp.Attributes, ts, int64(dp.Count), dp.GetSum(), dp.GetMin(), dp.GetMax())
		}
	case *metrics.Metric_Sum:
		for _, dp := range d.Sum.DataPoints {
			ts := tsFromNano(dp.TimeUnixNano)
			val := asFloat(dp)
			fn(dp.Attributes, ts, 1, val, val, val)
		}
	case *metrics.Metric_Gauge:
		for _, dp := range d.Gauge.DataPoints {
			ts := tsFromNano(dp.TimeUnixNano)
			val := asFloat(dp)
			fn(dp.Attributes, ts, 1, val, val, val)
		}
	}
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

func getResourceServiceName(rm *metrics.ResourceMetrics) string {
	if rm.Resource == nil {
		return ""
	}
	return getStringAttr(rm.Resource.Attributes, "service.name")
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

func getIntAttr(attrs []*common.KeyValue, key string) int64 {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value.GetIntValue()
		}
	}
	return 0
}

func attrsToMap(attrs []*common.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, kv := range attrs {
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
