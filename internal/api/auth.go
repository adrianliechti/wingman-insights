package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

	// No admin group means the SPA shows the personal view to everyone and no
	// caller may use the org-wide endpoints.
	if adminGroup() == "" {
		log.Print("auth: COMPANION_ADMIN_GROUP is unset; the SPA will show the personal view to all users and org-wide endpoints are disabled.")
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

// forwardedIdentity reads the identity that oauth2-proxy has already
// authenticated and forwarded. It deliberately does not verify the JWT: the
// proxy is the verification boundary for routes that use this middleware.
// Consequently, it must only be used on routes that cannot be reached without
// passing through that trusted proxy.
func forwardedIdentity(r *http.Request) (oid string, groups []string, err error) {
	raw := strings.TrimSpace(r.Header.Get("X-Forwarded-Access-Token"))
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[1] == "" {
		return "", nil, errInvalidForwardedToken
	}

	payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
	if decodeErr != nil {
		return "", nil, errInvalidForwardedToken
	}

	var claims struct {
		OID    string   `json:"oid"`
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.OID == "" {
		return "", nil, errInvalidForwardedToken
	}
	return claims.OID, claims.Groups, nil
}

var errInvalidForwardedToken = errors.New("missing or malformed X-Forwarded-Access-Token")

// withForwardedIdentity trusts oauth2-proxy to authenticate the request, then
// decodes its forwarded access token only to scope a response to that user.
func (h *Handler) withForwardedIdentity(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.verifier == nil {
			// Local development has no oauth2-proxy. Keep its behavior explicit
			// and consistent with the direct-token middleware.
			ctx := context.WithValue(r.Context(), userContextKey, "dev")
			ctx = context.WithValue(ctx, adminContextKey, os.Getenv("COMPANION_DEV_ADMIN") == "true")
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

// withForwardedAdmin uses the group claims supplied by oauth2-proxy to gate
// an org-wide endpoint. Like withForwardedIdentity, it relies on the proxy as
// the JWT verification boundary.
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
