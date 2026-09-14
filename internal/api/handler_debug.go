package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// debugTokenResponse reports exactly what token (if any) reached the
// application and how it would be evaluated, without enforcing auth itself —
// so it also works for the failing case (e.g. an audience mismatch) that
// prompted the need to inspect it.
type debugTokenResponse struct {
	HasAuthorizationHeader bool           `json:"hasAuthorizationHeader"`
	HasForwardedHeader     bool           `json:"hasForwardedHeader"`
	Source                 string         `json:"source,omitempty"` // "authorization" or "x-forwarded-access-token"
	Token                  string         `json:"token,omitempty"`
	Claims                 map[string]any `json:"claims,omitempty"`
	ClaimsError            string         `json:"claimsError,omitempty"`
	Verified               bool           `json:"verified"`
	VerifyError            string         `json:"verifyError,omitempty"`
}

// debugToken mirrors the token-selection logic in bearerToken so the caller
// can see which header won, decodes its claims WITHOUT verifying the
// signature (claims are attacker-controlled input here, purely for display),
// and separately reports whether it would actually pass this server's
// verifier — the same check withAuth performs.
func (h *Handler) debugToken(w http.ResponseWriter, r *http.Request) {
	authRaw, hasAuth := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	hasAuth = hasAuth && authRaw != ""
	forwardedRaw := strings.TrimSpace(r.Header.Get("X-Forwarded-Access-Token"))

	resp := debugTokenResponse{
		HasAuthorizationHeader: hasAuth,
		HasForwardedHeader:     forwardedRaw != "",
	}

	var raw string
	switch {
	case hasAuth:
		raw, resp.Source = authRaw, "authorization"
	case forwardedRaw != "":
		raw, resp.Source = forwardedRaw, "x-forwarded-access-token"
	default:
		writeJSON(w, resp)
		return
	}
	resp.Token = raw

	if claims, err := unverifiedJWTClaims(raw); err != nil {
		resp.ClaimsError = err.Error()
	} else {
		resp.Claims = claims
	}

	if h.verifier == nil {
		resp.VerifyError = "auth disabled (COMPANION_AUTH_DISABLED=true); token not verified"
	} else if _, err := h.verifier.Verify(r.Context(), raw); err != nil {
		resp.VerifyError = err.Error()
	} else {
		resp.Verified = true
	}

	writeJSON(w, resp)
}

// unverifiedJWTClaims decodes a JWT's payload segment without checking its
// signature, so an invalid-audience or expired token can still be inspected.
func unverifiedJWTClaims(raw string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT: expected 3 dot-separated segments")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}
