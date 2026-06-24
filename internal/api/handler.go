package api

import (
	"encoding/json"
	"net/http"
	"reflect"
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
	genai.get("/token-summary", h.tokenSummary)
	genai.get("/token-timeseries", h.tokenTimeseries)
	genai.get("/operations", h.operations)
	genai.get("/active-users", h.activeUsers)
	genai.get("/operation-duration-timeseries", h.operationDurationTimeseries)
	genai.get("/model-distribution", h.modelDistribution)
	genai.get("/cache-efficiency", h.cacheEfficiency)
	genai.get("/token-composition", h.tokenComposition)
	genai.get("/reasoning-share", h.reasoningShare)
	genai.get("/errors", h.genaiErrors)
	genai.get("/anomalies", h.anomalies)
	genai.get("/anomaly-timeseries", h.anomalyTimeseries)
	genai.get("/cost-anomaly-timeseries", h.costAnomalyTimeseries)
	genai.get("/anomaly-feed", h.anomalyFeed)
	genai.get("/costs", h.costs)
	genai.get("/cost-report", h.costReport)

	finops := group("/finops")
	finops.get("/cost-timeseries", h.costTimeseries)
	finops.get("/token-timeseries", h.tokenVolumeTimeseries)
	finops.get("/budget", h.budget)

	product := group("/product")
	product.get("/model-mix", h.modelMix)
	product.get("/operation-mix", h.operationMix)
	product.get("/tokens-per-request", h.tokensPerRequest)
	product.get("/sessions-timeseries", h.sessionsTimeseries)
	product.get("/session-stats", h.sessionStats)
	product.get("/interactions", h.interactions)

	customers := group("/customers")
	customers.get("/user-stats", h.userStats)
	customers.get("/segments", h.userSegments)
	customers.get("/cohort-retention", h.cohortRetention)
	customers.get("/app-adoption", h.appAdoption)
	customers.get("/model-preference", h.modelPreference)
	customers.get("/burst", h.burst)
	customers.get("/new-vs-returning", h.newVsReturning)

	ops := group("/ops")
	ops.get("/latency-percentiles", h.latencyPercentiles)
	ops.get("/throughput", h.throughput)
	ops.get("/ttfc-timeseries", h.ttfcTimeseries)
	ops.get("/error-rate", h.genaiErrorRate)
	ops.get("/tools", h.toolStats)
	ops.get("/top-consumers", h.topConsumers)

	httpg := group("/http")
	httpg.get("/summary", h.httpSummary)
	httpg.get("/timeseries", h.httpTimeseries)
	httpg.get("/requests-timeseries", h.httpRequestsTimeseries)
	httpg.get("/errors-by-code", h.httpErrorsByCode)
}

func parseTimeRange(r *http.Request) (time.Time, time.Time) {
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	to := now

	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	return from, to
}

func (h *Handler) parseFilter(r *http.Request) store.Filter {
	q := r.URL.Query()
	// User/department/location are matched against the directory table inside the
	// query (see Filter.clause); nothing to expand here.
	return store.Filter{
		Service:    q.Get("service"),
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

func parseInterval(r *http.Request) string {
	if v := r.URL.Query().Get("interval"); v != "" {
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
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
