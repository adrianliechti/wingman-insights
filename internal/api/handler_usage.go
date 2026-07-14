package api

import (
	"net/http"

	"insights/internal/store"
)

// usageResponse is the shape of GET /api/usage. Cost is always present.
// Buckets is included only when the caller supplies an interval parameter; it
// is omitted entirely (not serialised as null or []) when no interval is given.
type usageResponse struct {
	Cost    float64                 `json:"cost"`
	Buckets []store.TimeseriesPoint `json:"buckets,omitempty"`
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
		buckets, err := h.store.QueryTokenVolumeTimeseries(r.Context(), from, to, parseInterval(r), "model", f)
		if err != nil {
			writeErr(w, err)
			return
		}
		// Ensure buckets serialises as [] rather than null when empty, because the
		// caller already knows it requested the bucketed view.
		if buckets == nil {
			buckets = []store.TimeseriesPoint{}
		}
		resp.Buckets = buckets
	}

	writeJSON(w, resp)
}
