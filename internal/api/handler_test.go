package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
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
		"0 hour":      "1 hour", // all-zero → default; DuckDB rejects a zero interval
		"00000 day":   "1 hour", // all-zero with leading zeros → default
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

// TestParseTimeRangeSingleBoundDoesNotCollideWithDefault guards against a
// swap that fires when only one of from/to is given: the other stays at its
// default (now / now-24h), and comparing that default against an unrelated
// explicit bound isn't a genuinely inverted pair. Before this was fixed, a
// `to` in the distant past with no `from` swapped the untouched default
// (now-24h) into `from`, producing a multi-year window instead of leaving the
// default (now-24h) as `from` untouched.
func TestParseTimeRangeSingleBoundDoesNotCollideWithDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/?to=2020-01-01T00:00:00Z", nil)
	from, _ := parseTimeRange(r)
	wantFrom := time.Now().UTC().Add(-24 * time.Hour)
	if d := from.Sub(wantFrom); d < -time.Minute || d > time.Minute {
		t.Errorf("from = %v, want ~%v (untouched default, not swapped with `to`)", from, wantFrom)
	}
}
