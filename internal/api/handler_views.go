package api

import (
	"net/http"
	"os"
	"strconv"
	"time"
)

func (h *Handler) interactions(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	n, err := h.store.QueryInteractions(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]int64{"count": n})
}

// userStats is the unified per-user table: requests, tokens, cost, active days,
// top model and engagement segment.
func (h *Handler) userStats(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryUserStats(r.Context(), from, to, 200, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// cohortRetention returns the weekly signup-cohort retention matrix (fixed
// 12-week lookback, independent of the dashboard range).
func (h *Handler) cohortRetention(w http.ResponseWriter, r *http.Request) {
	_, to := parseTimeRange(r)
	rows, err := h.store.QueryCohortRetention(r.Context(), to, 12, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// burst ranks users by peak requests-per-minute — the in-dashboard runaway/peak
// signal.
func (h *Handler) burst(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryUserBurst(r.Context(), from, to, 15, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

type budgetResponse struct {
	Budget       float64 `json:"budget"`
	MonthToDate  float64 `json:"month_to_date"`
	Projected    float64 `json:"projected"`
	MonthElapsed float64 `json:"month_elapsed"`
}

// budget reports month-to-date spend against the INSIGHTS_BUDGET_MONTHLY
// budget (USD, 0 = no budget configured) and a linear month-end projection.
// It always covers the current calendar month, independent of the dashboard
// time range; the global filters still apply.
func (h *Handler) budget(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)

	rows, err := h.store.QueryCostBreakdown(r.Context(), monthStart, now, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	var mtd float64
	for _, row := range rows {
		mtd += row.TotalCost
	}

	elapsed := float64(now.Sub(monthStart)) / float64(monthEnd.Sub(monthStart))
	resp := budgetResponse{MonthToDate: mtd, MonthElapsed: elapsed}
	if elapsed > 0 {
		resp.Projected = mtd / elapsed
	}
	if v, err := strconv.ParseFloat(os.Getenv("INSIGHTS_BUDGET_MONTHLY"), 64); err == nil {
		resp.Budget = v
	}
	writeJSON(w, resp)
}
