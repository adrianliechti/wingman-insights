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

const userContextKey contextKey = "auth.user"

func userFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userContextKey).(string)
	return v
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
				OID string `json:"oid"`
			}

			if err := token.Claims(&claims); err != nil {
				log.Printf("auth: claims: %v", err)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			oid = claims.OID
		} else {
			// Auth disabled (COMPANION_AUTH_DISABLED=true): dev mode.
			oid = "dev"
		}

		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, oid)))
	}
}
