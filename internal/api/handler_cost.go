package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"insights/internal/store"
)

// aggregateCostRows applies the group_by aggregation shared by the costs and
// cost-report endpoints. dflt names the grouping used when the parameter is
// empty or unrecognized ("user" for the JSON endpoint, "none" — the full
// user × model breakdown — for the CSV).
func aggregateCostRows(rows []store.CostRow, groupBy, dflt string) []store.CostRow {
	switch groupBy {
	case "user", "model", "app", "department", "location", "none":
	default:
		groupBy = dflt
	}
	switch groupBy {
	case "user":
		return store.AggregateCostsByUser(rows)
	case "model":
		return store.AggregateCostsByModel(rows)
	case "app":
		return store.AggregateCostsByApp(rows)
	case "department":
		return store.AggregateCostsByDepartment(rows)
	case "location":
		return store.AggregateCostsByLocation(rows)
	}
	return rows // "none"
}

// costs returns priced token usage. group_by=user (default) | model | app |
// department | location | none (none = full user × model breakdown).
func (h *Handler) costs(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCostBreakdown(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, aggregateCostRows(rows, r.URL.Query().Get("group_by"), "user"))
}

// costReport streams a cost breakdown as a CSV download. By default it is the
// full user × model breakdown; group_by=department (etc.) exports the same
// aggregations the costs endpoint serves — e.g. a per-department showback for
// cost-center chargeback. Aggregated rows leave the columns that don't apply
// to their grouping empty.
func (h *Handler) costReport(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r)
	rows, err := h.store.QueryCostBreakdown(r.Context(), from, to, h.parseFilter(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	rows = aggregateCostRows(rows, groupBy, "none")

	name := "cost-report"
	switch groupBy {
	case "user", "model", "app", "department", "location":
		name += "_by-" + groupBy
	}
	filename := fmt.Sprintf("%s_%s_%s.csv", name, from.Format("2006-01-02"), to.Format("2006-01-02"))
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
			row.ID, row.Name, row.Kind, row.Department, row.Location, row.AppID, row.ProviderName, row.RequestModel,
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
