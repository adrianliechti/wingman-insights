// Package mcpserver exposes the Insights query layer as read-only MCP tools.
package mcpserver

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"

	"insights/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type principal struct {
	user  string
	admin bool
}

type principalKey struct{}

type handlerConfig struct {
	dev     *principal
	origins []string
}

func (c *handlerConfig) allowsAnyOrigin() bool { return slices.Contains(c.origins, "*") }

// Option customizes the MCP handler. The zero set keeps the production
// behavior: every request must carry a bearer token.
type Option func(*handlerConfig)

// WithDevFallback accepts unauthenticated callers as the given identity instead
// of answering 401. It exists so local development can skip token plumbing and
// must never be enabled in a deployment; the caller owns that gate.
func WithDevFallback(user string, admin bool) Option {
	return func(c *handlerConfig) {
		if user = strings.TrimSpace(user); user == "" {
			user = "dev"
		}
		c.dev = &principal{user: user, admin: admin}
	}
}

// WithAllowedOrigins lets browser clients on the given origins call the
// endpoint: each is registered as a trusted origin and answered with CORS
// headers. A single "*" allows any origin, which also disables the cross-origin
// protection entirely — only sound when the network itself is the boundary.
// Without this option cross-origin browser requests stay rejected.
func WithAllowedOrigins(origins ...string) Option {
	return func(c *handlerConfig) {
		for _, origin := range origins {
			if origin = strings.TrimSpace(origin); origin != "" {
				c.origins = append(c.origins, origin)
			}
		}
	}
}

// Verifier authenticates a request and reports the caller's identity and
// whether it holds the admin group. It must verify the credential itself
// (signature, issuer, audience, expiry), not merely decode it.
type Verifier func(*http.Request) (user string, admin bool, err error)

// NewHandler serves MCP to callers that verify passes. In production verify is
// the dashboard's OIDC check (api.Handler.VerifyBearer).
func NewHandler(s *store.Store, verify Verifier, opts ...Option) http.Handler {
	var cfg handlerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	server := newServer(s)
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
	})
	// A wildcard allowlist makes every request "trusted", so the CSRF check can
	// only reject callers the operator already chose to admit.
	var guarded http.Handler = http.MaxBytesHandler(transport, 1<<20)
	if !cfg.allowsAnyOrigin() {
		protection := http.NewCrossOriginProtection()
		for _, origin := range cfg.origins {
			// Rejected only for malformed values; the allowlist is operator input.
			if err := protection.AddTrustedOrigin(origin); err != nil {
				panic("mcp: invalid allowed origin " + origin + ": " + err.Error())
			}
		}
		guarded = protection.Handler(guarded)
	}
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal{}, false
		if verify != nil {
			if user, admin, err := verify(r); err == nil && user != "" {
				p, ok = principal{user: user, admin: admin}, true
			} else if err != nil && r.Header.Get("Authorization") != "" {
				log.Printf("mcp: auth: %v", err)
			}
		}
		if !ok {
			// A valid token still wins when present, so the bypass changes only
			// the unauthenticated case.
			if cfg.dev == nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="insights-mcp"`)
				http.Error(w, "a valid bearer token is required", http.StatusUnauthorized)
				return
			}
			p = *cfg.dev
		}
		// Same rule as the dashboard's org-wide views: the token must carry
		// INSIGHTS_ADMIN_GROUP. With the variable unset nobody is admitted.
		if !p.admin {
			http.Error(w, "admin group membership required", http.StatusForbidden)
			return
		}
		guarded.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
	// CORS wraps auth so preflights, which carry no credentials, are answered
	// instead of collecting a 401 the browser would report as a CORS failure.
	return withCORS(&cfg, authenticated)
}

// withCORS answers browser preflights and annotates allowed cross-origin
// responses. It is a no-op when no origins are configured.
func withCORS(cfg *handlerConfig, next http.Handler) http.Handler {
	if len(cfg.origins) == 0 {
		return next
	}
	any := cfg.allowsAnyOrigin()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := any && origin != ""
		if !allowed && slices.Contains(cfg.origins, origin) {
			allowed = true
			// The response varies per origin, so caches must not share it.
			w.Header().Add("Vary", "Origin")
		}
		if allowed {
			h := w.Header()
			if any {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
			}
			// Streamable HTTP clients read the session and negotiated version
			// off the response; both are invisible to JS unless exposed.
			h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id, Mcp-Protocol-Version, WWW-Authenticate")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
				requested := r.Header.Get("Access-Control-Request-Headers")
				if requested == "" {
					requested = "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID"
				}
				h.Set("Access-Control-Allow-Headers", requested)
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func newServer(s *store.Store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "wingman-insights", Version: "0.5.0", Icons: icons}, nil)
	t := tools{store: s}
	addTool(server, "get_activity", "Analyze observed custom-agent, MCP, plugin, and built-in tool usage using the same execution accounting as Customers Activity. Every response is {query, warnings, data}; show applicable warnings. view=summary (default) returns totals for distinct_custom_agents, agent_runs, mcp_tool_calls, plugin_executions, builtin_tool_calls, and most_used_mcp_tool when available. view=agents|mcp|plugins|builtin returns category totals. group_by=server|tool for mcp, plugin for plugins, or tool for builtin adds groups ranked by executions, with total_groups and truncated; limit defaults to 20, maximum 100, and totals cover all groups. Plugin groups include bounded plugin-owned skill details, which are part of plugin totals. interval=auto|hour|day adds points:[{time,series,executions}] instead of groups; group_by and interval cannot be combined. Agent trend series first_observed + other_runs sum to agent_runs: first observed means the earliest identified run across retained history, not creation; other runs includes repeats and unidentified runs. Only non-plugin configured agents are counted; delegated chat helpers are excluded. MCP trends split by server, plugin trends by plugin, built-in trends by tool. Activity trends zero-fill intervals for observed entities; zero means no matching recorded executions, not proof of no usage. Executions include successful, failed and unreported outcomes; avg_duration_ms is in milliseconds. Plugin attribution overlaps other views: never add plugin executions to tool or agent totals. Distinct agent counts exclude missing IDs and are not an inventory of all installed agents. No private agent or personal-skill names/IDs, raw traces, prompts, or tool arguments are returned. To compare periods, call once per period with the same filters.", t.activity)
	addTool(server, "get_usage", "Answer how AI usage and spending look and why they changed. Every response is {query, warnings, data}; show warnings with results. data by view (other fields and empty lists are omitted): totals only (default) -> {totals}; interval=auto|hour|day -> {totals, points:[{time, model, ...}]} with points only for buckets that have data (missing buckets are absent, not zero); group_by=model|application|department|location|user -> {totals, groups:[{id, name?, department?, location?, ...}], total_groups, truncated} ranked by cost, empty id means unassigned; group_by=prompt_size -> {prompt_sizes:{totals, bins:[{label, min_input_tokens, max_input_tokens, requests, cost_usd, request_percent, cost_percent, cost_per_request_usd}]}} in ascending size, to tell whether large prompts drive spending; view=adoption -> {adoption:{totals, points, frequency}} with distinct active/new/returning users per interval and frequency:[{active_days, users}] of active UTC days; new means first observed activity in retained history under the same filters, not signup, and per-bucket user counts must not be summed. Usage figures: requests are token-bearing provider calls, not conversations; input_tokens includes cache_read_tokens and cache_creation_tokens, output_tokens includes reasoning_tokens, total_tokens = input_tokens + output_tokens. cost_usd and cache_savings_usd are estimates at stored catalog prices, not invoices; unpriced_tokens > 0 means cost is incomplete. Per-request averages and *_percent values (0 to 100) are pooled and omitted when undefined. Groups have no request counts. To compare periods, call once per period with the same filters; observations alone do not prove a cause.", t.usage)
	addTool(server, "get_categories", "Rank observed AI conversation topics by classified prompt count. Every response is {query, warnings, data}. data: {total_prompts, categories:[{category, prompts, share_percent}], total_categories, truncated}, with empty lists and false/zero counts omitted; group_by=department adds department to each row and computes share_percent within that department (empty department means unassigned). interval=auto|hour|day returns {total_prompts, points:[{time, category, prompts, share_percent}]} instead, with share_percent within each time bucket and only buckets that have data. Shares (0 to 100) describe classified prompts, not all requests; one winning category counts per classified prompt. Category IDs come from telemetry. No costs are attributed to categories and no prompt content is returned.", t.categories)
	return server
}
