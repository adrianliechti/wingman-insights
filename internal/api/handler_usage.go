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
	Buckets []usageBucket `json:"buckets,omitempty"`
}

// usageBucket is one point in the cost timeseries: cost (USD) for a model over
// one interval. It intentionally omits the underlying span count.
type usageBucket struct {
	Bucket time.Time `json:"bucket"`
	Cost   float64   `json:"cost"`
	Model  string    `json:"model,omitempty"`
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

	cost, err := h.store.QueryCostTotal(r.Context(), from, to, f)
	if err != nil {
		writeErr(w, err)
		return
	}

	resp := usageResponse{Cost: cost}

	if r.URL.Query().Has("interval") {
		points, err := h.store.QueryCostTimeseries(r.Context(), from, to, parseInterval(r), "model", f)
		if err != nil {
			writeErr(w, err)
			return
		}
		// Ensure buckets serialises as [] rather than null when empty, because the
		// caller already knows it requested the bucketed view.
		buckets := make([]usageBucket, len(points))
		for i, p := range points {
			buckets[i] = usageBucket{Bucket: p.Bucket, Cost: p.Value, Model: p.Label}
		}
		resp.Buckets = buckets
	}

	writeJSON(w, resp)
}
