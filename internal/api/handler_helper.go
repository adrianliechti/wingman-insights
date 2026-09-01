package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"insights/internal/store"
)

func parseTimeRange(r *http.Request) (time.Time, time.Time) {
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	to := now

	var fromSet, toSet bool
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
			fromSet = true
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
			toSet = true
		}
	}
	// Guard an inverted range so a swapped from/to returns the intended window
	// rather than silently empty results. Only swap when BOTH bounds were
	// explicitly supplied: if only one was given, the other is still its
	// default (now / now-24h), and comparing a default against an unrelated
	// explicit value (e.g. a "to" far in the past with no "from") isn't a
	// genuinely inverted pair — swapping there would silently turn a
	// single-sided request into a multi-year window instead of erroring or
	// using the sane default.
	if fromSet && toSet && from.After(to) {
		from, to = to, from
	}
	return from, to
}

func (h *Handler) parseFilter(r *http.Request) store.Filter {
	q := r.URL.Query()
	// User/department/location are matched against the directory table inside the
	// query (see Filter.clause); nothing to expand here.
	return store.Filter{
		App:        parseList(q.Get("app")),
		User:       parseList(q.Get("user")),
		Department: parseList(q.Get("department")),
		Location:   parseList(q.Get("location")),
		DeptPrefix: h.store.DepartmentPrefix(),
		Provider:   parseList(q.Get("provider")),
		Models:     parseList(q.Get("models")),
	}
}

// parseList splits a comma-joined query value into its deduped, trimmed items.
func parseList(value string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

// intervalRe constrains the legacy "<n> <unit>" form before it is bound into
// CAST(? AS INTERVAL). An out-of-shape value falls back to the default rather
// than reaching the engine. It is a bound parameter, not concatenated, so this
// is robustness, not an injection guard.
var intervalRe = regexp.MustCompile(`^[1-9]\d{0,4} (second|minute|hour|day|week|month)s?$`)

// iso8601Re matches ISO 8601 duration strings (P prefix required, T separator
// required before time components so that M is unambiguous: date-M = month,
// time-M = minute). Examples: PT1H, P1D, P7D, PT30M, P1M, P1DT12H.
var iso8601Re = regexp.MustCompile(`^P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// bareDesignatorRe matches a bare "<n><letter>" shorthand without any ISO
// prefix, e.g. 1H, 30M, 1D, 7D, 2W, 1Y, 45S. M unambiguously means minute
// here (there is no date context, so month must be written as P1M).
var bareDesignatorRe = regexp.MustCompile(`^(\d+)([YMWDHMS])$`)

var bareDesignatorUnit = map[string]string{
	"Y": "year",
	"M": "minute", // bare M = minute; month requires the P prefix (P1M)
	"W": "week",
	"D": "day",
	"H": "hour",
	"S": "second",
}

// parseISO8601Interval converts a duration string to the DuckDB interval
// literal form ("<n> <unit>"). It accepts:
//   - Full ISO 8601 with P prefix (P1D, PT1H, PT30M, P1M, P1DT12H …)
//   - Bare "<n><letter>" shorthand (1H, 30M, 1D, 7D) where M = minute
//
// Only the first non-zero component is used for compound ISO 8601 values.
// Returns ("", false) for invalid or all-zero input.
func parseISO8601Interval(s string) (string, bool) {
	// Try bare <n><letter> first — it is unambiguous and common.
	if m := bareDesignatorRe.FindStringSubmatch(s); m != nil {
		if m[1] != "0" {
			return m[1] + " " + bareDesignatorUnit[m[2]], true
		}
		return "", false
	}
	// Fall through to strict ISO 8601 (P prefix required).
	m := iso8601Re.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	// m indices: 1=Y 2=M(month) 3=W 4=D 5=H 6=M(minute) 7=S
	type part struct {
		idx  int
		unit string
	}
	parts := []part{
		{1, "year"}, {2, "month"}, {3, "week"}, {4, "day"},
		{5, "hour"}, {6, "minute"}, {7, "second"},
	}
	for _, p := range parts {
		if m[p.idx] != "" && m[p.idx] != "0" {
			return m[p.idx] + " " + p.unit, true
		}
	}
	return "", false // all components are zero or absent
}

// parseInterval returns a DuckDB interval literal for the ?interval= query
// parameter. It accepts:
//   - Legacy "<n> <unit>" form (e.g. "1 hour", "1 day")
//   - Bare "<n><letter>" shorthand (e.g. "1H", "1D", "7D", "30M") where M = minute
//   - Full ISO 8601 with P prefix (e.g. "PT1H", "P1D", "P1M" for month)
//
// An absent, invalid, or all-zero value falls back to "1 hour".
func parseInterval(r *http.Request) string {
	v := r.URL.Query().Get("interval")
	if intervalRe.MatchString(v) {
		return v
	}
	if iv, ok := parseISO8601Interval(v); ok {
		return iv
	}
	return "1 hour"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if v == nil {
		w.Write([]byte("[]"))
		return
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	// Log the detail server-side; return a generic message so raw engine errors
	// (table/column names, SQL) are not disclosed to clients.
	log.Printf("api: %v", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// jsonRoute adapts a store query of shape (ctx, from, to, filter) into a GET
// handler: parse the range + filters, run, encode JSON (or 500). jsonRouteIv
// adds the interval string; jsonRouteBy adds the ?by= group-by. The generic
// signatures make a mismatched query a compile error, not a runtime one.
func jsonRoute[T any](h *Handler, q func(context.Context, time.Time, time.Time, store.Filter) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to := parseTimeRange(r)
		v, err := q(r.Context(), from, to, h.parseFilter(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, v)
	}
}

func jsonRouteIv[T any](h *Handler, q func(context.Context, time.Time, time.Time, string, store.Filter) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to := parseTimeRange(r)
		v, err := q(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, v)
	}
}

func jsonRouteBy[T any](h *Handler, q func(context.Context, time.Time, time.Time, string, string, store.Filter) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to := parseTimeRange(r)
		v, err := q(r.Context(), from, to, parseInterval(r), r.URL.Query().Get("by"), h.parseFilter(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, v)
	}
}
