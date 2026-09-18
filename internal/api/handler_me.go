package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// meResponse tells the SPA who the caller is and whether they may see the
// org-wide dashboard. When Admin is false the UI shows only the personal-usage
// view (fed by /api/personal/*). User is the caller's identity as forwarded by
// oauth2-proxy (typically their email); Name is the caller's display name,
// preferring the "name" claim from the forwarded token and falling back to
// directory resolution.
type meResponse struct {
	User  string `json:"user"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	name := nameFromForwardedToken(r)
	if name == "" {
		name = h.store.ResolveName(user, "")
	}
	writeJSON(w, meResponse{
		User:  user,
		Name:  name,
		Admin: adminFromContext(r.Context()),
	})
}

// nameFromForwardedToken reads the "name" claim from the access token forwarded
// by oauth2-proxy (X-Forwarded-Access-Token). The proxy is the trust boundary
// for this route, so the payload is decoded without re-verifying the signature.
// Returns "" when no token is present or it carries no name.
func nameFromForwardedToken(r *http.Request) string {
	tok := bearerToken(r)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Name)
}
