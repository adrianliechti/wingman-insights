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

func (h *Handler) tokenSummary(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTokenSummary(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) tokenTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTokenTimeseries(r.Context(), from, to, parseInterval(r), parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryOperationSummary(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) userSummary(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryUserTokenSummary(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) activeUsers(w http.ResponseWriter, r *http.Request) {
	_, to := parseTimeRange(r)
	row, err := h.store.QueryActiveUsers(r.Context(), to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, row)
}

func (h *Handler) activeUsersTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryActiveUsersTimeseries(r.Context(), from, to, parseInterval(r), parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) operationDurationTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryOperationDurationTimeseries(r.Context(), from, to, parseInterval(r), parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) modelDistribution(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryModelDistribution(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) cacheEfficiency(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCacheEfficiency(r.Context(), from, to, parseInterval(r), parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) genaiErrors(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryGenAIErrors(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// anomalies lists buckets whose token consumption spikes above the rolling
// baseline, grouped per user (default), service, model or overall.
func (h *Handler) anomalies(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "user"
	}
	rows, err := h.store.QueryTokenAnomalies(r.Context(), from, to, parseInterval(r), groupBy, 3.0, parseFilter(r))
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
	rows, err := h.store.QueryTokenAnomalies(r.Context(), from, to, parseInterval(r), "none", 0, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}
