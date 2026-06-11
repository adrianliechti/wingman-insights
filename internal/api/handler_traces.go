package api

import (
	"net/http"
	"strconv"
)

func (h *Handler) traceList(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	onlyErrors := q.Get("status") == "error"
	rows, err := h.store.QueryTraceList(r.Context(), from, to, parseFilter(r), onlyErrors, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) spans(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	onlyErrors := q.Get("status") == "error"
	rows, err := h.store.QuerySpans(r.Context(), from, to, parseFilter(r), onlyErrors, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (h *Handler) traceByID(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("id")
	if traceID == "" {
		http.Error(w, "missing trace id", http.StatusBadRequest)
		return
	}
	rows, err := h.store.QueryTrace(r.Context(), traceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}
