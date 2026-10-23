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
	server := mcp.NewServer(&mcp.Implementation{Name: "wingman-insights", Version: "0.1.0"}, nil)
	t := tools{store: s}
	addTool(server, "get_usage", "Get token usage and estimated USD cost, optionally with per-model trends. Non-admin callers are always scoped to their own identity; admins can query organization-wide. Input + output + cached is the total; reasoning is already included in output. A false priced flag means some usage has no known price, not free usage.", false, t.usage)
	addTool(server, "get_cost_breakdown", "Rank estimated USD costs by model, application, department, location, or user. Includes token partitions, cache savings, and unpriced tokens. Non-admin callers can only see their own costs regardless of filters or grouping. Estimates use stored catalog prices, not provider invoices.", false, t.costs)
	if admin {
		addTool(server, "get_categories", "Rank observed AI conversation topics by classified prompt count and share, optionally within each department. Admin only. Shares describe classified prompts, not all requests. One matched winning category counts per classified prompt. Empty department means unassigned. Category IDs come from telemetry; readable labels are derived from those IDs. No costs are attributed to categories.", true, t.categories)
		addTool(server, "get_category_trends", "Get classified prompt counts over time by topic. Admin only. Counts include matched winning categories, not risk checks or all platform requests. Category IDs and derived readable labels are included. No prompt or conversation content is returned.", true, t.categoryTrends)
	}
	return server
}
