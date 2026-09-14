package api

import (
	"context"
	"net/http"

	oidc "github.com/coreos/go-oidc/v3/oidc"

	"insights/internal/store"
)

type Handler struct {
	store    *store.Store
	verifier *oidc.IDTokenVerifier
}

func NewHandler(s *store.Store) *Handler {
	return &Handler{
		store:    s,
		verifier: newAuthFromEnv(context.Background()),
	}
}

const apiBase = "/api"

type routeGroup struct {
	mux     *http.ServeMux
	prefix  string
	handler *Handler
}

func (g routeGroup) getWithAdmin(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, g.handler.withAdmin(fn))
}

func (g routeGroup) getWithAuth(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, g.handler.withAuth(fn))
}

func (h *Handler) RegisterPersonal(mux *http.ServeMux) {
	for _, prefix := range []string{"/personal", "/companion"} {
		personal := routeGroup{mux, prefix, h}
		personal.getWithAuth("/usage", h.usage)
		personal.getWithAuth("/usage-by-app", h.usageByApp)
		personal.getWithAuth("/context-histogram", h.usageContextHistogram)

		mux.HandleFunc(prefix+"/", http.NotFound)
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	group := func(prefix string) routeGroup { return routeGroup{mux, apiBase + prefix, h} }

	core := group("")

	core.getWithAuth("/me", h.me)

	// GET /api/debug/token reports which header (Authorization vs
	// X-Forwarded-Access-Token) supplied the caller's token, its unverified
	// claims, and whether it passes this server's verifier — for diagnosing
	// audience/issuer mismatches. It deliberately bypasses withAuth so it
	// still works when auth would fail.
	mux.HandleFunc("GET "+apiBase+"/debug/token", h.debugToken)

	core.getWithAdmin("/filters", h.filterOptions)
	core.getWithAdmin("/traces", h.traceList)
	core.getWithAdmin("/traces/{id}", h.traceByID)

	genai := group("/genai")
	genai.getWithAdmin("/token-summary", jsonRoute(h, h.store.QueryTokenSummary))
	genai.getWithAdmin("/token-timeseries", jsonRouteIv(h, h.store.QueryTokenTimeseries))
	genai.getWithAdmin("/operations", jsonRoute(h, h.store.QueryOperationSummary))
	genai.getWithAdmin("/active-users", h.activeUsers)
	genai.getWithAdmin("/operation-duration-timeseries", jsonRouteIv(h, h.store.QueryOperationDurationTimeseries))
	genai.getWithAdmin("/model-distribution", jsonRoute(h, h.store.QueryModelDistribution))
	genai.getWithAdmin("/token-partitions", jsonRouteIv(h, h.store.QueryTokenPartitions))
	genai.getWithAdmin("/errors", jsonRoute(h, h.store.QueryGenAIErrors))
	genai.getWithAdmin("/anomalies", h.anomalies)
	genai.getWithAdmin("/anomaly-timeseries", h.anomalyTimeseries)
	genai.getWithAdmin("/cost-anomaly-timeseries", h.costAnomalyTimeseries)
	genai.getWithAdmin("/anomaly-feed", h.anomalyFeed)
	genai.getWithAdmin("/costs", h.costs)
	genai.getWithAdmin("/cost-report", h.costReport)

	finops := group("/finops")
	finops.getWithAdmin("/cost-timeseries", jsonRouteBy(h, h.store.QueryCostTimeseries))
	finops.getWithAdmin("/token-timeseries", jsonRouteBy(h, h.store.QueryTokenVolumeTimeseries))
	finops.getWithAdmin("/context-histogram", jsonRoute(h, h.store.QueryContextHistogram))
	finops.getWithAdmin("/budget", h.budget)

	product := group("/product")
	product.getWithAdmin("/model-mix", jsonRouteIv(h, h.store.QueryModelMixTimeseries))
	product.getWithAdmin("/operation-mix", jsonRouteIv(h, h.store.QueryOperationMixTimeseries))
	product.getWithAdmin("/tokens-per-request", jsonRouteIv(h, h.store.QueryTokensPerRequest))
	product.getWithAdmin("/sessions-timeseries", jsonRouteIv(h, h.store.QuerySessionsTimeseries))
	product.getWithAdmin("/session-stats", jsonRoute(h, h.store.QuerySessionStats))
	product.getWithAdmin("/interactions", h.interactions)

	customers := group("/customers")
	customers.getWithAdmin("/user-stats", h.userStats)
	customers.getWithAdmin("/segments", jsonRoute(h, h.store.QueryUserSegments))
	customers.getWithAdmin("/cohort-retention", h.cohortRetention)
	customers.getWithAdmin("/app-adoption", jsonRoute(h, h.store.QueryAppAdoption))
	customers.getWithAdmin("/model-preference", jsonRoute(h, h.store.QueryModelPreferenceBySegment))
	customers.getWithAdmin("/burst", h.burst)
	customers.getWithAdmin("/new-vs-returning", jsonRouteIv(h, h.store.QueryNewVsReturningTimeseries))

	ops := group("/ops")
	ops.getWithAdmin("/latency-percentiles", jsonRouteIv(h, h.store.QueryLatencyPercentiles))
	ops.getWithAdmin("/throughput", jsonRouteIv(h, h.store.QueryThroughputTimeseries))
	ops.getWithAdmin("/ttfc-timeseries", jsonRouteIv(h, h.store.QueryTTFCTimeseries))
	ops.getWithAdmin("/error-rate", jsonRouteIv(h, h.store.QueryGenAIErrorRate))
	ops.getWithAdmin("/tools", jsonRoute(h, h.store.QueryToolStats))
	ops.getWithAdmin("/top-consumers", h.topConsumers)

	httpg := group("/http")
	httpg.getWithAdmin("/summary", jsonRoute(h, h.store.QueryHTTPSummary))
	httpg.getWithAdmin("/timeseries", jsonRouteIv(h, h.store.QueryHTTPTimeseries))
	httpg.getWithAdmin("/requests-timeseries", jsonRouteIv(h, h.store.QueryHTTPRequestsTimeseries))
	httpg.getWithAdmin("/errors-by-code", jsonRoute(h, h.store.QueryHTTPErrorsByCode))
}
