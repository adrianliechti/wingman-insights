package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"insights/internal/store"
)

// costs returns priced token usage. group_by=user (default) | model | app |
// department | location | none (none = full user × model breakdown).
func (h *Handler) costs(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCostBreakdown(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	switch r.URL.Query().Get("group_by") {
	case "model":
		rows = store.AggregateCostsByModel(rows)
	case "app":
		rows = store.AggregateCostsByApp(rows)
	case "department":
		rows = store.AggregateCostsByDepartment(rows)
	case "location":
		rows = store.AggregateCostsByLocation(rows)
	case "none":
	default:
		rows = store.AggregateCostsByUser(rows)
	}
	writeJSON(w, rows)
}

// costReport streams the full user × model cost breakdown as a CSV download.
func (h *Handler) costReport(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCostBreakdown(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}

	filename := fmt.Sprintf("cost-report_%s_%s.csv", from.Format("2006-01-02"), to.Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	cw := csv.NewWriter(w)
	cw.Write([]string{
		"id", "name", "kind", "department", "location", "app", "provider", "model",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "reasoning_tokens",
		"input_cost_usd", "output_cost_usd", "cache_read_cost_usd", "cache_creation_cost_usd",
		"total_cost_usd", "priced",
	})
	var totalCost float64
	for _, row := range rows {
		totalCost += row.TotalCost
		cw.Write([]string{
			row.ID, row.Name, row.Kind, row.Department, row.Location, row.ServiceName, row.ProviderName, row.RequestModel,
			fmtTokens(row.InputTokens), fmtTokens(row.OutputTokens),
			fmtTokens(row.CacheReadTokens), fmtTokens(row.CacheCreationTokens), fmtTokens(row.ReasoningTokens),
			fmtCost(row.InputCost), fmtCost(row.OutputCost),
			fmtCost(row.CacheReadCost), fmtCost(row.CacheCreationCost),
			fmtCost(row.TotalCost), strconv.FormatBool(row.Priced),
		})
	}
	cw.Write([]string{"TOTAL", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", fmtCost(totalCost), ""})
	cw.Flush()
}

func fmtTokens(v float64) string {
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func fmtCost(v float64) string {
	return strconv.FormatFloat(v, 'f', 6, 64)
}
