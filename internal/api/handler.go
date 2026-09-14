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

func (g routeGroup) getWithForwardedIdentity(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, g.handler.withForwardedIdentity(fn))
}

func (g routeGroup) getWithForwardedAdmin(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, g.handler.withForwardedAdmin(fn))
}

func (h *Handler) RegisterPersonal(mux *http.ServeMux) {
	// /personal is behind oauth2-proxy. The proxy has already verified the
	// request, so decode its forwarded token solely to find the caller's OID.
	personal := routeGroup{mux, "/personal", h}
	personal.getWithForwardedIdentity("/usage", h.usage)
	personal.getWithForwardedIdentity("/usage-by-app", h.usageByApp)
	personal.getWithForwardedIdentity("/context-histogram", h.usageContextHistogram)
	mux.HandleFunc("/personal/", http.NotFound)

	// /companion is intentionally outside oauth2-proxy. It remains a direct
	// bearer-token API and therefore verifies its token locally.
	companion := routeGroup{mux, "/companion", h}
	companion.mux.HandleFunc("GET /companion/usage", h.withAuth(h.usage))
	companion.mux.HandleFunc("GET /companion/usage-by-app", h.withAuth(h.usageByApp))
	companion.mux.HandleFunc("GET /companion/context-histogram", h.withAuth(h.usageContextHistogram))
	mux.HandleFunc("/companion/", http.NotFound)
}

func (h *Handler) Register(mux *http.ServeMux) {
	group := func(prefix string) routeGroup { return routeGroup{mux, apiBase + prefix, h} }

	core := group("")

	// /api is behind oauth2-proxy and is deliberately not wrapped in the local
	// verifier. /me needs only the caller identity for SPA routing; every other
	// endpoint requires the configured group from the proxy-forwarded token.
	core.getWithForwardedIdentity("/me", h.me)
	core.getWithForwardedAdmin("/filters", h.filterOptions)
	core.getWithForwardedAdmin("/traces", h.traceList)
	core.getWithForwardedAdmin("/traces/{id}", h.traceByID)

	genai := group("/genai")
	genai.getWithForwardedAdmin("/token-summary", jsonRoute(h, h.store.QueryTokenSummary))
	genai.getWithForwardedAdmin("/token-timeseries", jsonRouteIv(h, h.store.QueryTokenTimeseries))
	genai.getWithForwardedAdmin("/operations", jsonRoute(h, h.store.QueryOperationSummary))
	genai.getWithForwardedAdmin("/active-users", h.activeUsers)
	genai.getWithForwardedAdmin("/operation-duration-timeseries", jsonRouteIv(h, h.store.QueryOperationDurationTimeseries))
	genai.getWithForwardedAdmin("/model-distribution", jsonRoute(h, h.store.QueryModelDistribution))
	genai.getWithForwardedAdmin("/token-partitions", jsonRouteIv(h, h.store.QueryTokenPartitions))
	genai.getWithForwardedAdmin("/errors", jsonRoute(h, h.store.QueryGenAIErrors))
	genai.getWithForwardedAdmin("/anomalies", h.anomalies)
	genai.getWithForwardedAdmin("/anomaly-timeseries", h.anomalyTimeseries)
	genai.getWithForwardedAdmin("/cost-anomaly-timeseries", h.costAnomalyTimeseries)
	genai.getWithForwardedAdmin("/anomaly-feed", h.anomalyFeed)
	genai.getWithForwardedAdmin("/costs", h.costs)
	genai.getWithForwardedAdmin("/cost-report", h.costReport)

	finops := group("/finops")
	finops.getWithForwardedAdmin("/cost-timeseries", jsonRouteBy(h, h.store.QueryCostTimeseries))
	finops.getWithForwardedAdmin("/token-timeseries", jsonRouteBy(h, h.store.QueryTokenVolumeTimeseries))
	finops.getWithForwardedAdmin("/context-histogram", jsonRoute(h, h.store.QueryContextHistogram))
	finops.getWithForwardedAdmin("/budget", h.budget)

	product := group("/product")
	product.getWithForwardedAdmin("/model-mix", jsonRouteIv(h, h.store.QueryModelMixTimeseries))
	product.getWithForwardedAdmin("/operation-mix", jsonRouteIv(h, h.store.QueryOperationMixTimeseries))
	product.getWithForwardedAdmin("/tokens-per-request", jsonRouteIv(h, h.store.QueryTokensPerRequest))
	product.getWithForwardedAdmin("/sessions-timeseries", jsonRouteIv(h, h.store.QuerySessionsTimeseries))
	product.getWithForwardedAdmin("/session-stats", jsonRoute(h, h.store.QuerySessionStats))
	product.getWithForwardedAdmin("/interactions", h.interactions)

	customers := group("/customers")
	customers.getWithForwardedAdmin("/user-stats", h.userStats)
	customers.getWithForwardedAdmin("/segments", jsonRoute(h, h.store.QueryUserSegments))
	customers.getWithForwardedAdmin("/cohort-retention", h.cohortRetention)
	customers.getWithForwardedAdmin("/app-adoption", jsonRoute(h, h.store.QueryAppAdoption))
	customers.getWithForwardedAdmin("/model-preference", jsonRoute(h, h.store.QueryModelPreferenceBySegment))
	customers.getWithForwardedAdmin("/burst", h.burst)
	customers.getWithForwardedAdmin("/new-vs-returning", jsonRouteIv(h, h.store.QueryNewVsReturningTimeseries))

	ops := group("/ops")
	ops.getWithForwardedAdmin("/latency-percentiles", jsonRouteIv(h, h.store.QueryLatencyPercentiles))
	ops.getWithForwardedAdmin("/throughput", jsonRouteIv(h, h.store.QueryThroughputTimeseries))
	ops.getWithForwardedAdmin("/ttfc-timeseries", jsonRouteIv(h, h.store.QueryTTFCTimeseries))
	ops.getWithForwardedAdmin("/error-rate", jsonRouteIv(h, h.store.QueryGenAIErrorRate))
	ops.getWithForwardedAdmin("/tools", jsonRoute(h, h.store.QueryToolStats))
	ops.getWithForwardedAdmin("/top-consumers", h.topConsumers)

	httpg := group("/http")
	httpg.getWithForwardedAdmin("/summary", jsonRoute(h, h.store.QueryHTTPSummary))
	httpg.getWithForwardedAdmin("/timeseries", jsonRouteIv(h, h.store.QueryHTTPTimeseries))
	httpg.getWithForwardedAdmin("/requests-timeseries", jsonRouteIv(h, h.store.QueryHTTPRequestsTimeseries))
	httpg.getWithForwardedAdmin("/errors-by-code", jsonRoute(h, h.store.QueryHTTPErrorsByCode))
}
