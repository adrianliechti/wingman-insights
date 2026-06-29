package ingest

import (
	"testing"

	common "go.opentelemetry.io/proto/otlp/common/v1"
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

func strAttr(key, val string) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: val}}}
}
