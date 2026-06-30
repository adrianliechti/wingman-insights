package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParseInterval(t *testing.T) {
	cases := map[string]string{
		"15 minute":   "15 minute",
		"6 hour":      "6 hour",
		"1 day":       "1 day",
		"99999 weeks": "99999 weeks",
		"":            "1 hour", // unset → default
		"xyz":         "1 hour", // not "<n> <unit>" → default
		"1; DROP":     "1 hour", // junk → default, never reaches the engine
	}
	for in, want := range cases {
		r := httptest.NewRequest("GET", "/?interval="+url.QueryEscape(in), nil)
		if got := parseInterval(r); got != want {
			t.Errorf("parseInterval(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTimeRangeSwapsInverted(t *testing.T) {
	r := httptest.NewRequest("GET", "/?from=2026-06-10T00:00:00Z&to=2026-06-01T00:00:00Z", nil)
	from, to := parseTimeRange(r)
	if from.After(to) {
		t.Errorf("inverted range not swapped: from=%v after to=%v", from, to)
	}
}
