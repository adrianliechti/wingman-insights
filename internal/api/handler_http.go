package api

import "net/http"

func (h *Handler) httpSummary(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryHTTPSummary(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) httpTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryHTTPTimeseries(r.Context(), from, to, parseInterval(r), parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) httpRequestsTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryHTTPRequestsTimeseries(r.Context(), from, to, parseInterval(r), parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) httpErrorsByCode(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryHTTPErrorsByCode(r.Context(), from, to, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) topRoutes(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryTopRoutes(r.Context(), from, to, 10, parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}
