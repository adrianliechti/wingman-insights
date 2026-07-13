package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestParseInterval(t *testing.T) {
	cases := map[string]string{
		// Legacy DuckDB literal form — unchanged.
		"15 minute":   "15 minute",
		"6 hour":      "6 hour",
		"1 day":       "1 day",
		"99999 weeks": "99999 weeks",
		"":            "1 hour", // unset → default
		"xyz":         "1 hour", // not "<n> <unit>" → default
		"1; DROP":     "1 hour", // junk → default, never reaches the engine
		"0 hour":      "1 hour", // all-zero → default; DuckDB rejects a zero interval
		"00000 day":   "1 hour", // all-zero with leading zeros → default

		// Full ISO 8601 (P prefix, T separator required).
		"PT1H":  "1 hour",
		"PT30M": "30 minute",
		"PT45S": "45 second",
		"P1D":   "1 day",
		"P7D":   "7 day",
		"P2W":   "2 week",
		"P1M":   "1 month", // ISO P-prefix: M = month
		"P1Y":   "1 year",
		// Compound ISO: largest non-zero component wins.
		"P1DT12H": "1 day",
		// Bare <n><letter> shorthand — no prefix.
		"1H":  "1 hour",
		"30M": "30 minute", // bare M = minute
		"1D":  "1 day",
		"7D":  "7 day",
		"2W":  "2 week",
		"1Y":  "1 year",
		"45S": "45 second",
		// All-zero or structurally invalid → default.
		"PT0H": "1 hour",
		"P":    "1 hour",
		"T":    "1 hour",
		"0H":   "1 hour",
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
