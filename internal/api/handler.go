package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	"insights/internal/store"
)

type Handler struct {
	store *store.Store
}

func NewHandler(s *store.Store) *Handler {
	return &Handler{store: s}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/filters", h.filterOptions)
	mux.HandleFunc("GET /api/genai/token-summary", h.tokenSummary)
	mux.HandleFunc("GET /api/genai/token-timeseries", h.tokenTimeseries)
	mux.HandleFunc("GET /api/genai/operations", h.operations)
	mux.HandleFunc("GET /api/genai/user-summary", h.userSummary)
	mux.HandleFunc("GET /api/genai/active-users", h.activeUsers)
	mux.HandleFunc("GET /api/genai/active-users-timeseries", h.activeUsersTimeseries)
	mux.HandleFunc("GET /api/genai/operation-duration-timeseries", h.operationDurationTimeseries)
	mux.HandleFunc("GET /api/genai/model-distribution", h.modelDistribution)
	mux.HandleFunc("GET /api/genai/cache-efficiency", h.cacheEfficiency)
	mux.HandleFunc("GET /api/genai/token-composition", h.tokenComposition)
	mux.HandleFunc("GET /api/genai/reasoning-share", h.reasoningShare)
	mux.HandleFunc("GET /api/genai/errors", h.genaiErrors)
	mux.HandleFunc("GET /api/genai/anomalies", h.anomalies)
	mux.HandleFunc("GET /api/genai/anomaly-timeseries", h.anomalyTimeseries)
	mux.HandleFunc("GET /api/genai/costs", h.costs)
	mux.HandleFunc("GET /api/genai/cost-report", h.costReport)
	mux.HandleFunc("GET /api/traces", h.traceList)
	mux.HandleFunc("GET /api/traces/{id}", h.traceByID)
	mux.HandleFunc("GET /api/finops/cost-timeseries", h.costTimeseries)
	mux.HandleFunc("GET /api/finops/budget", h.budget)
	mux.HandleFunc("GET /api/product/model-mix", h.modelMix)
	mux.HandleFunc("GET /api/product/operation-mix", h.operationMix)
	mux.HandleFunc("GET /api/product/tokens-per-request", h.tokensPerRequest)
	mux.HandleFunc("GET /api/product/sessions-timeseries", h.sessionsTimeseries)
	mux.HandleFunc("GET /api/product/session-stats", h.sessionStats)
	mux.HandleFunc("GET /api/ops/latency-percentiles", h.latencyPercentiles)
	mux.HandleFunc("GET /api/ops/ttfc-timeseries", h.ttfcTimeseries)
	mux.HandleFunc("GET /api/ops/error-rate", h.genaiErrorRate)
	mux.HandleFunc("GET /api/ops/tools", h.toolStats)
	mux.HandleFunc("GET /api/http/summary", h.httpSummary)
	mux.HandleFunc("GET /api/http/timeseries", h.httpTimeseries)
	mux.HandleFunc("GET /api/http/requests-timeseries", h.httpRequestsTimeseries)
	mux.HandleFunc("GET /api/http/errors-by-code", h.httpErrorsByCode)
	mux.HandleFunc("GET /api/http/top-routes", h.topRoutes)
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

func parseFilter(r *http.Request) store.Filter {
	q := r.URL.Query()
	return store.Filter{
		Service:  q.Get("service"),
		User:     q.Get("user"),
		Provider: q.Get("provider"),
		Model:    q.Get("model"),
	}
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
