package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

type contextKey string

const userContextKey contextKey = "auth.user"

func userFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userContextKey).(string)
	return v
}

func newAuthFromEnv(ctx context.Context) (*oidc.IDTokenVerifier, []string) {
	tenantID := os.Getenv("INSIGHTS_ENTRA_TENANT_ID")

	if tenantID == "" {
		return nil, nil
	}

	var allowed []string
	for a := range strings.SplitSeq(os.Getenv("INSIGHTS_API_ALLOWED_AUDIENCES"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			allowed = append(allowed, a)
		}
	}

	issuer := fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", tenantID)

	provider, err := oidc.NewProvider(ctx, issuer)

	if err != nil {
		log.Fatalf("oidc: provider init: %v", err)
	}

	// Skip the built-in single-audience check; we validate aud manually below.
	verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return verifier, allowed
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
				OID      string `json:"oid"`
				Audience string `json:"aud"`
			}

			if err := token.Claims(&claims); err != nil {
				log.Printf("auth: claims: %v", err)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			if !slices.Contains(h.audiences, claims.Audience) {
				log.Printf("auth: audience not allowed: %v", claims.Audience)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			oid = claims.OID
		} else {
			// Auth disabled (no INSIGHTS_ENTRA_TENANT_ID): there is no verified
			// placeholder so local dev works without configuring Entra.
			oid = "dev"
		}

		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, oid)))
	}
}
