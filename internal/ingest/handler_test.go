package ingest

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"strings"
	"testing"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// TestGetIntAttr covers the int/double/string coercion that keeps token counts
// and status codes from silently becoming 0 when a sender promotes the type.
func TestGetIntAttr(t *testing.T) {
	attrs := []*common.KeyValue{
		{Key: "i", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 42}}},
		{Key: "d", Value: &common.AnyValue{Value: &common.AnyValue_DoubleValue{DoubleValue: 7.9}}},
		{Key: "s", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "13"}}},
		{Key: "bad", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "nope"}}},
		{Key: "nil", Value: nil},
	}
	cases := map[string]int64{"i": 42, "d": 7, "s": 13, "bad": 0, "nil": 0, "missing": 0}
	for k, want := range cases {
		if got := getIntAttr(attrs, k); got != want {
			t.Errorf("getIntAttr(%q) = %d, want %d", k, got, want)
		}
	}
}

// TestAppID covers the app_id fallback: the gateway-stamped service.peer.name
// wins, but when it's absent (non-OIDC auth / non-Entra apps) we fall back to
// the resource service.name so the row stays attributed in the App filter.
func TestAppID(t *testing.T) {
	peer := []*common.KeyValue{strAttr("service.peer.name", "client-app")}
	none := []*common.KeyValue{strAttr("gen_ai.operation.name", "chat")}

	cases := []struct {
		name        string
		attrs       []*common.KeyValue
		serviceName string
		want        string
	}{
		{"peer wins over service.name", peer, "wingman", "client-app"},
		{"fallback to service.name", none, "wingman", "wingman"},
		{"no peer, no service.name", none, "", ""},
	}
	for _, c := range cases {
		if got := appID(c.attrs, c.serviceName); got != c.want {
			t.Errorf("%s: appID = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDecodeOTLPGzip covers transport compression: the OTel Collector's otlphttp
// exporter gzips by default, and SDKs do with OTEL_EXPORTER_OTLP_COMPRESSION=gzip,
// so both encodings must decode through a Content-Encoding: gzip body.
func TestDecodeOTLPGzip(t *testing.T) {
	pb, err := proto.Marshal(&colmetrics.ExportMetricsServiceRequest{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cases := []struct {
		name, contentType string
		body              []byte
	}{
		{"json", "application/json", []byte(`{"resourceMetrics":[]}`)},
		{"protobuf", "application/x-protobuf", pb},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write(c.body)
		gz.Close()

		r := httptest.NewRequest("POST", "/v1/metrics", &buf)
		r.Header.Set("Content-Type", c.contentType)
		r.Header.Set("Content-Encoding", "gzip")
		ct, err := decodeOTLP(r, &colmetrics.ExportMetricsServiceRequest{})
		if err != nil {
			t.Errorf("%s: decodeOTLP = %v", c.name, err)
		}
		if ct != c.contentType {
			t.Errorf("%s: content type = %q, want %q", c.name, ct, c.contentType)
		}
	}

	// A body that claims gzip but isn't must error, not decode garbage.
	r := httptest.NewRequest("POST", "/v1/metrics", strings.NewReader("not gzip"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	if _, err := decodeOTLP(r, &colmetrics.ExportMetricsServiceRequest{}); err == nil {
		t.Error("bad gzip: expected error, got nil")
	}
}

// TestDecodeOTLPBodyLimit verifies an oversized body is rejected with an
// explicit limit error rather than truncated into an unmarshal failure.
func TestDecodeOTLPBodyLimit(t *testing.T) {
	big := bytes.Repeat([]byte("a"), otlpBodyLimit+1)
	r := httptest.NewRequest("POST", "/v1/metrics", bytes.NewReader(big))
	r.Header.Set("Content-Type", "application/json")
	if _, err := decodeOTLP(r, &colmetrics.ExportMetricsServiceRequest{}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversized body: err = %v, want limit error", err)
	}
}

func strAttr(key, val string) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: val}}}
}

// TestExtractWingmanMetric covers the wingman.* ingest branch: a Wingman-defined
// histogram (e.g. the classification score) must be stored as a GenAI row, with
// its wingman.classification.* attributes preserved in the attributes map and
// gen_ai.conversation.id mapped to the session id.
func TestExtractWingmanMetric(t *testing.T) {
	h := &Handler{}
	req := &colmetrics.ExportMetricsServiceRequest{
		ResourceMetrics: []*metrics.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*common.KeyValue{strAttr("service.name", "wingman-chat")}},
			ScopeMetrics: []*metrics.ScopeMetrics{{
				Metrics: []*metrics.Metric{{
					Name: "wingman.classification.score",
					Data: &metrics.Metric_Histogram{Histogram: &metrics.Histogram{
						AggregationTemporality: metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
						DataPoints: []*metrics.HistogramDataPoint{{
							Count: 1,
							Sum:   proto.Float64(0.9),
							Attributes: []*common.KeyValue{
								strAttr("gen_ai.operation.name", "evaluate"),
								strAttr("gen_ai.request.model", "classifier"),
								strAttr("gen_ai.conversation.id", "chat-1"),
								strAttr("wingman.classification.kind", "category"),
								strAttr("wingman.classification.id", "legal"),
							},
						}},
					}},
				}},
			}},
		}},
	}

	genai, httpRows := h.extract(req)
	if len(httpRows) != 0 {
		t.Fatalf("http rows = %d, want 0", len(httpRows))
	}
	if len(genai) != 1 {
		t.Fatalf("genai rows = %d, want 1", len(genai))
	}
	row := genai[0]
	if row.MetricName != "wingman.classification.score" {
		t.Errorf("metric name = %q", row.MetricName)
	}
	if row.OperationName != "evaluate" || row.RequestModel != "classifier" {
		t.Errorf("operation/model = %q/%q", row.OperationName, row.RequestModel)
	}
	if row.SessionID != "chat-1" {
		t.Errorf("session id = %q, want chat-1", row.SessionID)
	}
	if row.Count != 1 || row.Sum != 0.9 {
		t.Errorf("count/sum = %d/%v, want 1/0.9", row.Count, row.Sum)
	}
	if row.Attributes["wingman.classification.kind"] != "category" ||
		row.Attributes["wingman.classification.id"] != "legal" {
		t.Errorf("classification attributes not preserved: %v", row.Attributes)
	}
}
