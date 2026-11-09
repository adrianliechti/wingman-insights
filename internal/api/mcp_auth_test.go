package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestMCPVerifierFromEnv(t *testing.T) {
	k := newTestKey(t)
	var issuerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			issuer := issuerURL + strings.TrimSuffix(r.URL.Path, "/.well-known/openid-configuration")
			fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":["RS256"]}`, issuer, issuerURL+"/keys")
			return
		}
		if r.URL.Path == "/keys" {
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: k.priv.Public(), KeyID: "test-key", Algorithm: "RS256", Use: "sig"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()
	issuerURL = provider.URL
	companionIssuer, mcpIssuer := issuerURL+"/companion", issuerURL+"/mcp"

	sign := func(issuer, audience string, groups []string, expiry time.Duration) string {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"iss": issuer, "aud": audience, "oid": "alice", "groups": groups,
			"exp": time.Now().Add(expiry).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k.priv}, (&jose.SignerOptions{}).WithHeader("kid", "test-key"))
		if err != nil {
			t.Fatal(err)
		}
		signed, err := signer.Sign(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := signed.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	for _, tc := range []struct {
		name, issuer, audience, wantIssuer, wantAudience string
	}{
		{"defaults", "", "", companionIssuer, "companion-aud"},
		{"both overrides", mcpIssuer, "mcp-aud", mcpIssuer, "mcp-aud"},
		{"issuer only", mcpIssuer, "", mcpIssuer, "companion-aud"},
		{"audience only", "", "mcp-aud", companionIssuer, "mcp-aud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INSIGHTS_AUTH_DISABLED", "false")
			t.Setenv("COMPANION_API_ISSUER", companionIssuer)
			t.Setenv("COMPANION_API_AUDIENCE", "companion-aud")
			t.Setenv("INSIGHTS_MCP_ISSUER", tc.issuer)
			t.Setenv("INSIGHTS_MCP_AUDIENCE", tc.audience)
			t.Setenv("INSIGHTS_ADMIN_GROUP", "insights_admin")
			h := NewHandler(nil)
			verify, err := h.MCPVerifierFromEnv(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, token := range []struct {
				name, issuer, audience string
				groups                 []string
				expiry                 time.Duration
				wantError, wantAdmin   bool
			}{
				{"admin", tc.wantIssuer, tc.wantAudience, []string{"insights_admin"}, time.Hour, false, true},
				{"other group", tc.wantIssuer, tc.wantAudience, []string{"other"}, time.Hour, false, false},
				{"wrong issuer", issuerURL + "/wrong", tc.wantAudience, []string{"insights_admin"}, time.Hour, true, false},
				{"wrong audience", tc.wantIssuer, "wrong-aud", []string{"insights_admin"}, time.Hour, true, false},
				{"expired", tc.wantIssuer, tc.wantAudience, []string{"insights_admin"}, -time.Hour, true, false},
			} {
				t.Run(token.name, func(t *testing.T) {
					r := httptest.NewRequest("POST", "/mcp", nil)
					r.Header.Set("Authorization", "Bearer "+sign(token.issuer, token.audience, token.groups, token.expiry))
					user, admin, err := verify(r)
					if (err != nil) != token.wantError || admin != token.wantAdmin || (!token.wantError && user != "alice") {
						t.Fatalf("user=%q admin=%t err=%v", user, admin, err)
					}
				})
			}
			// The MCP overrides must not change companion authentication.
			r := httptest.NewRequest("GET", "/companion/usage", nil)
			r.Header.Set("Authorization", "Bearer "+sign(companionIssuer, "companion-aud", []string{"insights_admin"}, time.Hour))
			if _, _, err := h.VerifyBearer(r); err != nil {
				t.Fatalf("companion verifier changed: %v", err)
			}
		})
	}

	t.Run("disabled companion with explicit MCP settings", func(t *testing.T) {
		t.Setenv("INSIGHTS_AUTH_DISABLED", "true")
		t.Setenv("INSIGHTS_MCP_ISSUER", mcpIssuer)
		t.Setenv("INSIGHTS_MCP_AUDIENCE", "mcp-aud")
		t.Setenv("INSIGHTS_ADMIN_GROUP", "insights_admin")
		h := NewHandler(nil)
		verify, err := h.MCPVerifierFromEnv(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+sign(mcpIssuer, "mcp-aud", []string{"insights_admin"}, time.Hour))
		if user, admin, err := verify(r); err != nil || user != "alice" || !admin {
			t.Fatalf("user=%q admin=%t err=%v", user, admin, err)
		}
		if _, _, err := h.VerifyBearer(r); err == nil {
			t.Fatal("companion auth unexpectedly enabled")
		}
	})
}

func TestMCPVerifierConfigurationErrors(t *testing.T) {
	for _, tc := range []struct{ name, issuer, audience string }{
		{"missing audience", "https://issuer.example.com", ""},
		{"missing issuer", "", "mcp-aud"},
		{"discovery failure", ":invalid", "mcp-aud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPANION_API_ISSUER", "")
			t.Setenv("COMPANION_API_AUDIENCE", "")
			t.Setenv("INSIGHTS_MCP_ISSUER", tc.issuer)
			t.Setenv("INSIGHTS_MCP_AUDIENCE", tc.audience)
			verify, err := makeHandler(nil).MCPVerifierFromEnv(context.Background())
			if err == nil || verify != nil {
				t.Fatalf("invalid configuration returned verifier=%v err=%v", verify != nil, err)
			}
		})
	}
}
