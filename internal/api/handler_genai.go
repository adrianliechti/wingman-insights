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
	rows, err := h.store.QueryTokenSummary(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) tokenTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTokenTimeseries(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryOperationSummary(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) userSummary(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryUserTokenSummary(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
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

func (h *Handler) activeUsersTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryActiveUsersTimeseries(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) operationDurationTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryOperationDurationTimeseries(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) modelDistribution(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryModelDistribution(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) cacheEfficiency(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCacheEfficiency(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) tokenComposition(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTokenComposition(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) reasoningShare(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryReasoningShare(r.Context(), from, to, parseInterval(r), h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) genaiErrors(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryGenAIErrors(r.Context(), from, to, h.parseFilter(r))
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

// costAnomalies lists buckets whose spend spikes above the rolling baseline,
// grouped per user (default), service, model or overall — the spend counterpart
// to anomalies.
func (h *Handler) costAnomalies(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "user"
	}
	rows, err := h.store.QueryCostAnomalies(r.Context(), from, to, parseInterval(r), groupBy, 3.0, h.parseFilter(r))
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
