package api

import (
	"net/http"
	"time"
)

// usageResponse is the shape of GET /api/usage. Cost is always present.
// Buckets is included only when the caller supplies an interval parameter; it
// is omitted entirely (not serialised as null or []) when no interval is given.
type usageResponse struct {
	Cost    float64       `json:"cost"`
	Tokens  usageTokens   `json:"tokens"`
	Buckets []usageBucket `json:"buckets,omitempty"`
}

// usageTokens is the aggregate token consumption for the window. Input is the
// inclusive prompt total; Cached is the cached subset of that input.
type usageTokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cached int64 `json:"cached"`
}

// usageBucket is one point in the cost timeseries: cost (USD) and token
// consumption for a model over one interval. It intentionally omits the
// underlying span count.
type usageBucket struct {
	Bucket time.Time   `json:"bucket"`
	Cost   float64     `json:"cost"`
	Model  string      `json:"model,omitempty"`
	Tokens usageTokens `json:"tokens"`
}

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	from, to := parseTimeRange(r)
	f := h.parseFilter(r)
	f.User = user

	resp := usageResponse{}

	if r.URL.Query().Has("interval") {
		// The bucketed view carries per-model cost and tokens; the window totals
		// are just their sum, so a single scan serves both — no separate totals
		// query.
		points, err := h.store.QueryUsageTimeseries(r.Context(), from, to, parseInterval(r), f)
		if err != nil {
			writeErr(w, err)
			return
		}
		// Ensure buckets serialises as [] rather than null when empty, because the
		// caller already knows it requested the bucketed view.
		buckets := make([]usageBucket, len(points))
		for i, p := range points {
			buckets[i] = usageBucket{
				Bucket: p.Bucket,
				Cost:   p.Cost,
				Model:  p.Model,
				Tokens: usageTokens{Input: p.Input, Output: p.Output, Cached: p.Cached},
			}
			resp.Cost += p.Cost
			resp.Tokens.Input += p.Input
			resp.Tokens.Output += p.Output
			resp.Tokens.Cached += p.Cached
		}
		resp.Buckets = buckets
	} else {
		totals, err := h.store.QueryUsageTotals(r.Context(), from, to, f)
		if err != nil {
			writeErr(w, err)
			return
		}
		resp.Cost = totals.Cost
		resp.Tokens = usageTokens{Input: totals.Tokens.Input, Output: totals.Tokens.Output, Cached: totals.Tokens.Cached}
	}

	writeJSON(w, resp)
}
