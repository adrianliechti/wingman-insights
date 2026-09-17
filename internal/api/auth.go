package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

type contextKey string

const (
	userContextKey  contextKey = "auth.user"
	adminContextKey contextKey = "auth.admin"
)

func userFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userContextKey).(string)
	return v
}

// adminFromContext reports whether the caller holds the admin group and may
// see the org-wide dashboard in the SPA. It is set by the identity middleware.
func adminFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(adminContextKey).(bool)
	return v
}

// adminGroup is the JWT groups-claim value that enables the org-wide dashboard
// in the SPA and authorizes its endpoints after oauth2-proxy authenticates.
func adminGroup() string {
	return os.Getenv("INSIGHTS_ADMIN_GROUP")
}

func newAuthFromEnv(ctx context.Context) *oidc.IDTokenVerifier {
	if os.Getenv("INSIGHTS_AUTH_DISABLED") == "true" {
		return nil
	}

	issuer := os.Getenv("COMPANION_API_ISSUER")
	audience := os.Getenv("COMPANION_API_AUDIENCE")

	if issuer == "" || audience == "" {
		log.Fatal("oidc: COMPANION_API_ISSUER and COMPANION_API_AUDIENCE are required; set INSIGHTS_AUTH_DISABLED=true to disable auth")
	}

	provider, err := oidc.NewProvider(ctx, issuer)

	if err != nil {
		log.Fatalf("oidc: provider init: %v", err)
	}

	// No admin group means the SPA shows the personal view to everyone and no
	// caller may use the org-wide endpoints.
	if adminGroup() == "" {
		log.Print("auth: INSIGHTS_ADMIN_GROUP is unset; the SPA will show the personal view to all users and org-wide endpoints are disabled.")
	}

	return provider.Verifier(&oidc.Config{ClientID: audience})
}

// bearerToken extracts the raw JWT from either the Authorization: Bearer header
// or the X-Forwarded-Access-Token header set by an upstream oauth2-proxy. The
// Authorization header takes precedence so direct API callers are unaffected.
func bearerToken(r *http.Request) string {
	if raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && raw != "" {
		return raw
	}
	return strings.TrimSpace(r.Header.Get("X-Forwarded-Access-Token"))
}

// forwardedIdentity reads the identity headers set by oauth2-proxy after it
// authenticates the request. X-Forwarded-Groups is a comma-separated list.
// The proxy is the verification boundary for routes that use this middleware,
// so they must not be reachable without passing through that trusted proxy.
func forwardedIdentity(r *http.Request) (user string, groups []string, err error) {
	user = strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
	if user == "" {
		return "", nil, errMissingForwardedUser
	}

	for _, value := range r.Header.Values("X-Forwarded-Groups") {
		for _, group := range strings.Split(value, ",") {
			if group = strings.TrimSpace(group); group != "" {
				groups = append(groups, group)
			}
		}
	}
	return user, groups, nil
}

var errMissingForwardedUser = errors.New("missing X-Forwarded-User")

// withForwardedIdentity trusts oauth2-proxy to authenticate the request, then
// reads its forwarded identity headers to scope a response to that user.
func (h *Handler) withForwardedIdentity(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.verifier == nil {
			// Local development has no oauth2-proxy. Keep its behavior explicit
			// and consistent with the direct-token middleware.
			ctx := context.WithValue(r.Context(), userContextKey, "dev")
			ctx = context.WithValue(ctx, adminContextKey, os.Getenv("INSIGHTS_DEV_ADMIN") == "true")
			next(w, r.WithContext(ctx))
			return
		}

		oid, groups, err := forwardedIdentity(r)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, oid)
		ctx = context.WithValue(ctx, adminContextKey, isAdmin(groups))
		next(w, r.WithContext(ctx))
	}
}

// withForwardedAdmin uses the group headers supplied by oauth2-proxy to gate
// an org-wide endpoint. Like withForwardedIdentity, it relies on the proxy as
// the authentication boundary.
func (h *Handler) withForwardedAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.withForwardedIdentity(func(w http.ResponseWriter, r *http.Request) {
		if !adminFromContext(r.Context()) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var oid string
		var admin bool

		if h.verifier != nil {
			rawToken := bearerToken(r)

			if rawToken == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			token, err := h.verifier.Verify(r.Context(), rawToken)

			if err != nil {
				log.Printf("auth: %v", err)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			var claims struct {
				OID    string   `json:"oid"`
				Groups []string `json:"groups"`
			}

			if err := token.Claims(&claims); err != nil {
				log.Printf("auth: claims: %v", err)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			oid = claims.OID
			admin = isAdmin(claims.Groups)
		} else {
			// Auth disabled (INSIGHTS_AUTH_DISABLED=true): dev mode. Default the
			// developer to the personal-usage view, matching the product default;
			// set INSIGHTS_DEV_ADMIN=true to see the org-wide dashboard locally.
			oid = "dev"
			admin = os.Getenv("INSIGHTS_DEV_ADMIN") == "true"
		}

		ctx := context.WithValue(r.Context(), userContextKey, oid)
		ctx = context.WithValue(ctx, adminContextKey, admin)
		next(w, r.WithContext(ctx))
	}
}

// withAdmin wraps withAuth and additionally requires the caller to hold the
// admin group; a non-admin authenticated user gets 403. It gates the org-wide
// dashboard endpoints so a personal-usage-only user can't read them directly.
func (h *Handler) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if !adminFromContext(r.Context()) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// isAdmin reports whether the token's groups grant org-wide access. With no
// admin group configured, nobody is admin (personal-usage-only default).
// Otherwise the caller must carry the configured group.
func isAdmin(groups []string) bool {
	g := adminGroup()
	if g == "" {
		return false
	}
	for _, v := range groups {
		if v == g {
			return true
		}
	}
	return false
}
