package api

import (
	"context"
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

// adminFromContext reports whether the authenticated caller holds the admin
// group and may see the org-wide dashboard. It is set by withAuth.
func adminFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(adminContextKey).(bool)
	return v
}

// adminGroup is the JWT groups-claim value that grants access to the org-wide
// dashboard. When unset, no authenticated caller is treated as admin, so
// everyone sees only the personal-usage view (secure default — grant admin
// explicitly by configuring the group).
func adminGroup() string {
	return os.Getenv("COMPANION_ADMIN_GROUP")
}

func newAuthFromEnv(ctx context.Context) *oidc.IDTokenVerifier {
	if os.Getenv("COMPANION_AUTH_DISABLED") == "true" {
		return nil
	}

	issuer := os.Getenv("COMPANION_API_ISSUER")
	audience := os.Getenv("COMPANION_API_AUDIENCE")

	if issuer == "" || audience == "" {
		log.Fatal("oidc: COMPANION_API_ISSUER and COMPANION_API_AUDIENCE are required; set COMPANION_AUTH_DISABLED=true to disable auth")
	}

	provider, err := oidc.NewProvider(ctx, issuer)

	if err != nil {
		log.Fatalf("oidc: provider init: %v", err)
	}

	// No admin group means nobody is admin: every authenticated caller sees
	// only their personal usage. That's the secure default, but it also means
	// the org-wide dashboard is unreachable until a group is configured, so
	// make the choice visible at startup.
	if adminGroup() == "" {
		log.Print("auth: COMPANION_ADMIN_GROUP is unset; all users see only personal usage and the org-wide dashboard is disabled. Set it to grant admins the full dashboard.")
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
			// Auth disabled (COMPANION_AUTH_DISABLED=true): dev mode. Default the
			// developer to the personal-usage view, matching the product default;
			// set COMPANION_DEV_ADMIN=true to see the org-wide dashboard locally.
			oid = "dev"
			admin = os.Getenv("COMPANION_DEV_ADMIN") == "true"
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
