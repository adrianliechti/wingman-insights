// Package mcpserver exposes the Insights query layer as read-only MCP tools.
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"insights/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type principal struct {
	user  string
	admin bool
}

type principalKey struct{}

// NewHandler trusts the identity and groups in the bearer JWT without verifying
// it. This is a temporary integration boundary: the caller/upstream must supply
// a trusted token. It deliberately does not change dashboard/companion auth.
func NewHandler(s *store.Store, adminGroup string) http.Handler {
	personal := newServer(s, false)
	admin := newServer(s, true)
	transport := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, _ := r.Context().Value(principalKey{}).(principal)
		if p.admin {
			return admin
		}
		return personal
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
	})
	protected := http.NewCrossOriginProtection().Handler(http.MaxBytesHandler(transport, 1<<20))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := tokenPrincipal(r.Header.Get("Authorization"), adminGroup)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="insights-mcp"`)
			http.Error(w, "a bearer JWT with an oid or sub identity is required", http.StatusUnauthorized)
			return
		}
		protected.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func tokenPrincipal(header, adminGroup string) (principal, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 32*1024 {
		return principal{}, false
	}
	jwt := strings.Split(parts[1], ".")
	if len(jwt) != 3 || jwt[0] == "" {
		return principal{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(jwt[1])
	if err != nil {
		return principal{}, false
	}
	var claims struct {
		OID    string   `json:"oid"`
		Sub    string   `json:"sub"`
		Groups []string `json:"groups"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return principal{}, false
	}
	user := strings.TrimSpace(claims.OID)
	if user == "" {
		user = strings.TrimSpace(claims.Sub)
	}
	if user == "" {
		return principal{}, false
	}
	p := principal{user: user}
	for _, group := range claims.Groups {
		if adminGroup != "" && group == adminGroup {
			p.admin = true
		}
	}
	return p, true
}

func newServer(s *store.Store, admin bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "wingman-insights", Version: "0.3.0"}, nil)
	t := tools{store: s}
	addTool(server, "get_usage", "Answer how usage/spending changed and whether it coincided with more provider calls, larger inputs/outputs, less cache reuse, or more reasoning. Returns cost, tokens, call counts, per-call averages, cache-read/reasoning shares, pricing coverage and chart data. Set compare_previous=true for the preceding equal-length window, or compare_from/compare_to for a calendar-period comparison; changes from a zero baseline are null. Use interval=auto for trends and chart_metric to select counts, tokens, costs, per-call averages or percentages. Rates and Other groups use pooled denominators. For model-mix changes query get_cost_breakdown by model in each period; observations alone do not prove a cause. Non-admin data and both comparison windows are always scoped to the caller. Calls are token-bearing provider spans, not conversations; input+output+cached is the token total and reasoning is already included in output. Show warnings with charts and keep nulls as gaps.", false, t.usage)
	addTool(server, "get_cost_breakdown", "Rank estimated USD costs by model, application, department, location, or user. Includes a ready-to-plot bar chart with stable IDs, readable labels, units, and top_n groups plus Other. Chart totals cover all matches even when raw rows are truncated by limit. Choose chart_metric=cost_usd, total_tokens, or cache_savings_usd; use horizontal bars for long labels and a zero baseline. Includes token partitions, cache savings, and unpriced tokens. Non-admin callers can only see their own costs regardless of filters or grouping. Estimates use stored catalog prices, not provider invoices. Include query warnings when presenting charts.", false, t.costs)
	addTool(server, "get_prompt_sizes", "Answer whether large prompts account for a disproportionate share of spending. Returns ordered input-token size bins with provider-call counts, stored cost, per-call averages, request/cost percentages, exact inclusive bounds and unpriced coverage. Chart metrics select counts, cost or shares (0 to 100). Prompt size includes cached input; output-only calls are excluded. Use the same filters as usage. Personal callers cannot read other users' data. Compare request shares with cost shares; this describes observed cost distribution, not proof that reducing prompts would preserve quality.", false, t.promptSizes)
	if admin {
		addTool(server, "get_adoption", "Answer how many observed users are active, new or returning, and whether they use AI on multiple days. Admin only. Returns distinct-user totals, new/returning trends and a frequency distribution of active UTC days. chart_metric=active_users or new_vs_returning_users shows trends; active_days shows a frequency histogram. New means first observed matching token-bearing provider activity in retained history, not signup; filters also apply to the historical baseline. Aliases resolve to canonical identities; anonymous calls and known application identities are excluded. Do not sum bucket distinct-user counts or interpret a single active day in this window as proof of permanent abandonment. No licensed-user denominator, adoption percentage, productivity or ROI is inferred.", true, t.adoption)
		addTool(server, "get_categories", "Rank observed AI conversation topics by classified prompt count and share, optionally within each department. Includes a ready-to-plot bar chart, or stacked bars for department comparison. Use chart_metric=share_percent for percentages (0 to 100), or prompts for counts. top_n keeps the largest departments/categories and combines the rest as Other; shares retain their full denominators. Chart uses all matches independently of raw row limit. Admin only. Shares describe classified prompts, not all requests. One matched winning category counts per classified prompt. Empty department means unassigned. Category IDs come from telemetry; readable labels are derived from those IDs. No costs are attributed to categories.", true, t.categories)
		addTool(server, "get_category_trends", "Get classified prompt counts over time by topic, with a ready-to-plot line chart on a shared UTC grid. Use interval=auto and chart_metric=prompts or share_percent. Chart rows have x, x_label, series, series_label, value; keep nulls as gaps rather than zero. top_n picks series across the whole window and combines remaining topics as Other. Admin only. Counts include matched winning categories, not risk checks or all platform requests. Category IDs and derived readable labels are included. No prompt or conversation content is returned.", true, t.categoryTrends)
	}
	return server
}
