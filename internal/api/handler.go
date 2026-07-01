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

type Handler struct {
	store *store.Store
}

func NewHandler(s *store.Store) *Handler {
	return &Handler{store: s}
}

// apiBase is the mount point for every dashboard endpoint; it is itself mounted
// under the UI base path (e.g. /insights/api/...). OTLP ingest lives at the root
// /v1 instead and is registered separately in main.
const apiBase = "/api"

// routeGroup registers GET routes sharing a path prefix — the stdlib equivalent
// of a router's route group, so each domain's prefix is written once and the
// whole tree could be versioned by changing apiBase alone.
type routeGroup struct {
	mux    *http.ServeMux
	prefix string
}

func (g routeGroup) get(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, fn)
}

func (h *Handler) Register(mux *http.ServeMux) {
	group := func(prefix string) routeGroup { return routeGroup{mux, apiBase + prefix} }

	core := group("")
	core.get("/filters", h.filterOptions)
	core.get("/traces", h.traceList)
	core.get("/traces/{id}", h.traceByID)

	genai := group("/genai")
	genai.get("/token-summary", jsonRoute(h, h.store.QueryTokenSummary))
	genai.get("/token-timeseries", jsonRouteIv(h, h.store.QueryTokenTimeseries))
	genai.get("/operations", jsonRoute(h, h.store.QueryOperationSummary))
	genai.get("/active-users", h.activeUsers)
	genai.get("/operation-duration-timeseries", jsonRouteIv(h, h.store.QueryOperationDurationTimeseries))
	genai.get("/model-distribution", jsonRoute(h, h.store.QueryModelDistribution))
	genai.get("/cache-efficiency", jsonRouteIv(h, h.store.QueryCacheEfficiency))
	genai.get("/token-composition", jsonRouteIv(h, h.store.QueryTokenComposition))
	genai.get("/reasoning-share", jsonRouteIv(h, h.store.QueryReasoningShare))
	genai.get("/errors", jsonRoute(h, h.store.QueryGenAIErrors))
	genai.get("/anomalies", h.anomalies)
	genai.get("/anomaly-timeseries", h.anomalyTimeseries)
	genai.get("/cost-anomaly-timeseries", h.costAnomalyTimeseries)
	genai.get("/anomaly-feed", h.anomalyFeed)
	genai.get("/costs", h.costs)
	genai.get("/cost-report", h.costReport)

	finops := group("/finops")
	finops.get("/cost-timeseries", jsonRouteBy(h, h.store.QueryCostTimeseries))
	finops.get("/token-timeseries", jsonRouteBy(h, h.store.QueryTokenVolumeTimeseries))
	finops.get("/budget", h.budget)

	product := group("/product")
	product.get("/model-mix", jsonRouteIv(h, h.store.QueryModelMixTimeseries))
	product.get("/operation-mix", jsonRouteIv(h, h.store.QueryOperationMixTimeseries))
	product.get("/tokens-per-request", jsonRouteIv(h, h.store.QueryTokensPerRequest))
	product.get("/sessions-timeseries", jsonRouteIv(h, h.store.QuerySessionsTimeseries))
	product.get("/session-stats", jsonRoute(h, h.store.QuerySessionStats))
	product.get("/interactions", h.interactions)

	customers := group("/customers")
	customers.get("/user-stats", h.userStats)
	customers.get("/segments", jsonRoute(h, h.store.QueryUserSegments))
	customers.get("/cohort-retention", h.cohortRetention)
	customers.get("/app-adoption", jsonRoute(h, h.store.QueryAppAdoption))
	customers.get("/model-preference", jsonRoute(h, h.store.QueryModelPreferenceBySegment))
	customers.get("/burst", h.burst)
	customers.get("/new-vs-returning", jsonRouteIv(h, h.store.QueryNewVsReturningTimeseries))

	ops := group("/ops")
	ops.get("/latency-percentiles", jsonRouteIv(h, h.store.QueryLatencyPercentiles))
	ops.get("/throughput", jsonRouteIv(h, h.store.QueryThroughputTimeseries))
	ops.get("/ttfc-timeseries", jsonRouteIv(h, h.store.QueryTTFCTimeseries))
	ops.get("/error-rate", jsonRouteIv(h, h.store.QueryGenAIErrorRate))
	ops.get("/tools", jsonRoute(h, h.store.QueryToolStats))
	ops.get("/top-consumers", h.topConsumers)

	httpg := group("/http")
	httpg.get("/summary", jsonRoute(h, h.store.QueryHTTPSummary))
	httpg.get("/timeseries", jsonRouteIv(h, h.store.QueryHTTPTimeseries))
	httpg.get("/requests-timeseries", jsonRouteIv(h, h.store.QueryHTTPRequestsTimeseries))
	httpg.get("/errors-by-code", jsonRoute(h, h.store.QueryHTTPErrorsByCode))
}

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
		App:        q.Get("app"),
		User:       q.Get("user"),
		Department: q.Get("department"),
		Location:   q.Get("location"),
		DeptPrefix: h.store.DepartmentPrefix(),
		Provider:   q.Get("provider"),
		Models:     parseModels(q.Get("models")),
	}
}

func parseModels(value string) []string {
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

// intervalRe constrains the interval to a "<n> <unit>" form before it is bound
// into CAST(? AS INTERVAL). An out-of-shape value (which would make DuckDB raise
// a cast error surfaced as a 500) falls back to the default rather than reaching
// the engine. It is a bound parameter, not concatenated, so this is robustness,
// not an injection guard.
var intervalRe = regexp.MustCompile(`^[1-9]\d{0,4} (second|minute|hour|day|week|month)s?$`)

func parseInterval(r *http.Request) string {
	if v := r.URL.Query().Get("interval"); intervalRe.MatchString(v) {
		return v
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
