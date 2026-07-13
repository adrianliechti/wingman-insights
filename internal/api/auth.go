package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

type ctxKeyOID struct{}

func userFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyOID{}).(string)
	return v
}

// newVerifierFromEnv returns an OIDC token verifier for the Azure AD tenant
// when INSIGHTS_ENTRA_TENANT_ID and INSIGHTS_ENTRA_CLIENT_ID are set, nil otherwise.
func newVerifierFromEnv(ctx context.Context) *oidc.IDTokenVerifier {
	tenantID := os.Getenv("INSIGHTS_ENTRA_TENANT_ID")
	clientID := os.Getenv("INSIGHTS_ENTRA_CLIENT_ID")
	if tenantID == "" || clientID == "" {
		return nil
	}

	issuer := fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", tenantID)

	provider, err := oidc.NewProvider(ctx, issuer)

	if err != nil {
		log.Fatalf("oidc: provider init: %v", err)
	}

	return provider.Verifier(&oidc.Config{ClientID: clientID})
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var oid string

		if h.verifier != nil {
			rawToken, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

			if !ok || rawToken == "" {
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
		}

		next(w, r.WithContext(context.WithValue(r.Context(), ctxKeyOID{}, oid)))
	}
}
