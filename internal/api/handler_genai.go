package api

import "net/http"

func (h *Handler) filterOptions(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	opts, err := h.store.QueryFilterOptions(r.Context(), from, to)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, opts)
}

func (h *Handler) topConsumers(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTopConsumers(r.Context(), from, to, 10, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) activeUsers(w http.ResponseWriter, r *http.Request) {
	_, to := parseTimeRange(r)
	row, err := h.store.QueryActiveUsers(r.Context(), to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, row)
}

// anomalies lists buckets whose token consumption spikes above the rolling
// baseline, grouped per user (default), service, model or overall.
func (h *Handler) anomalies(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	groupBy := r.URL.Query().Get("group_by")
	switch groupBy {
	case "user", "app", "model", "none":
		// valid as given
	default:
		// Empty or unrecognized: default rather than let an invalid value reach
		// the store's stricter validation and surface as a 500 — matches how
		// the costs endpoint treats an unrecognized group_by (defaults, doesn't
		// error), since this is a dashboard parameter, not a security boundary.
		groupBy = "user"
	}
	rows, err := h.store.QueryTokenAnomalies(r.Context(), from, to, parseInterval(r), groupBy, 3.0, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// anomalyTimeseries returns the full scored consumption series (no grouping)
// so charts can highlight anomalous buckets in context.
func (h *Handler) anomalyTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTokenAnomalies(r.Context(), from, to, parseInterval(r), "none", 0, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// costAnomalyTimeseries returns the full scored spend series (no grouping) for
// the cost spike chart.
func (h *Handler) costAnomalyTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCostAnomalies(r.Context(), from, to, parseInterval(r), "none", 0, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// anomalyFeed ranks the strongest spend and token anomalies across the user,
// service and model dimensions into one cross-dimension feed.
func (h *Handler) anomalyFeed(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryAnomalyFeed(r.Context(), from, to, parseInterval(r), 3.0, 50, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}
