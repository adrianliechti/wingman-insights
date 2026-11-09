package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// MCPVerifierFromEnv uses MCP-specific OIDC settings when supplied, otherwise
// reuses the companion verifier. Each unset setting defaults to its companion
// equivalent. Authorization always uses the shared INSIGHTS_ADMIN_GROUP.
func (h *Handler) MCPVerifierFromEnv(ctx context.Context) (func(*http.Request) (string, bool, error), error) {
	issuer := strings.TrimSpace(os.Getenv("INSIGHTS_MCP_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("INSIGHTS_MCP_AUDIENCE"))
	if issuer == "" && audience == "" {
		return h.VerifyBearer, nil
	}
	if issuer == "" {
		issuer = os.Getenv("COMPANION_API_ISSUER")
	}
	if audience == "" {
		audience = os.Getenv("COMPANION_API_AUDIENCE")
	}
	if issuer == "" || audience == "" {
		return nil, fmt.Errorf("mcp: issuer and audience are required; set INSIGHTS_MCP_ISSUER and INSIGHTS_MCP_AUDIENCE or their COMPANION_API equivalents")
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("mcp: OIDC provider init: %w", err)
	}
	auth := &Handler{verifier: provider.Verifier(&oidc.Config{ClientID: audience})}
	return auth.VerifyBearer, nil
}
