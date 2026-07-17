package api

import (
	"context"
	"net/http"

	oidc "github.com/coreos/go-oidc/v3/oidc"

	"insights/internal/store"
)

type Handler struct {
	store     *store.Store
	verifier  *oidc.IDTokenVerifier
	audiences []string
}

func NewHandler(s *store.Store) *Handler {
	verifier, audiences := newAuthFromEnv(context.Background())

	return &Handler{
		store:     s,
		verifier:  verifier,
		audiences: audiences,
	}
}

// apiBase is the mount point for every dashboard endpoint; it is itself mounted
// under the UI base path (e.g. /insights/api/...). OTLP ingest lives at the root
// /v1 instead and is registered separately in main.
const apiBase = "/api"

// routeGroup registers GET routes sharing a path prefix — the stdlib equivalent
// of a router's route group, so each domain's prefix is written once and the
// whole tree could be versioned by changing apiBase alone.
type routeGroup struct {
	mux     *http.ServeMux
	prefix  string
	handler *Handler
}

func (g routeGroup) get(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, fn)
}

func (g routeGroup) getWithToken(path string, fn http.HandlerFunc) {
	g.mux.HandleFunc("GET "+g.prefix+path, g.handler.withAuth(fn))
}

func (h *Handler) RegisterCompanion(mux *http.ServeMux) {
	companion := routeGroup{mux, "/companion", h}
	companion.getWithToken("/usage", h.usage)

	// /companion is an API-only namespace. Unmatched subpaths would otherwise
	// fall through to the SPA handler (which serves index.html for anything it
	// can't resolve); return a real 404 instead. The specific /companion/usage
	// pattern above still wins by ServeMux precedence.
	mux.HandleFunc("/companion/", http.NotFound)
}

func (h *Handler) Register(mux *http.ServeMux) {
	group := func(prefix string) routeGroup { return routeGroup{mux, apiBase + prefix, h} }

	// Authenticated routes — require a valid bearer token.
	core := group("")

	// can be removed, when companion points to /api/companion/usage
	core.getWithToken("/usage", h.usage)

	// Public routes — no authentication required.
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
	genai.get("/token-partitions", jsonRouteIv(h, h.store.QueryTokenPartitions))
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
