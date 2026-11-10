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
// an OIDC check configured by api.Handler.MCPVerifierFromEnv.
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
	addTool(server, "get_activity", activityDescription, t.activity)
	addTool(server, "get_usage", usageDescription, t.usage)
	addTool(server, "get_categories", categoriesDescription, t.categories)
	return server
}
