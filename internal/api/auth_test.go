package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

const testIssuer = "https://issuer.example.com"

// testKey holds an RSA key pair and the oidc verifier built from its public key.
type testKey struct {
	priv     *rsa.PrivateKey
	verifier *oidc.IDTokenVerifier
}

func newTestKey(t *testing.T) *testKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{priv.Public()}}
	verifier := oidc.NewVerifier(testIssuer, ks, &oidc.Config{
		SkipClientIDCheck: true,
	})
	return &testKey{priv: priv, verifier: verifier}
}

// signToken signs a minimal JWT with the given audience, oid and expiry offset.
func (k *testKey) signToken(t *testing.T, aud, oid string, expOffset time.Duration) string {
	t.Helper()
	now := time.Now()
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"oid":%q,"iat":%d,"exp":%d}`,
		testIssuer, aud, oid,
		now.Unix(),
		now.Add(expOffset).Unix(),
	)
	key := jose.SigningKey{Algorithm: jose.RS256, Key: k.priv}
	opts := &jose.SignerOptions{}
	opts.WithHeader(jose.HeaderKey("kid"), "test-key")
	signer, err := jose.NewSigner(key, opts)
	if err != nil {
		t.Fatalf("jose.NewSigner: %v", err)
	}
	sig, err := signer.Sign([]byte(payload))
	if err != nil {
		t.Fatalf("signer.Sign: %v", err)
	}
	raw, err := sig.CompactSerialize()
	if err != nil {
		t.Fatalf("CompactSerialize: %v", err)
	}
	return raw
}

// signTokenAudArray signs a JWT whose aud claim is a JSON array ([]string),
// exercising the OIDC multi-audience case.
func (k *testKey) signTokenAudArray(t *testing.T, audiences []string, oid string) string {
	t.Helper()
	now := time.Now()
	audJSON := `[`
	for i, a := range audiences {
		if i > 0 {
			audJSON += ","
		}
		audJSON += fmt.Sprintf("%q", a)
	}
	audJSON += `]`
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%s,"oid":%q,"iat":%d,"exp":%d}`,
		testIssuer, audJSON, oid,
		now.Unix(),
		now.Add(time.Hour).Unix(),
	)
	key := jose.SigningKey{Algorithm: jose.RS256, Key: k.priv}
	opts := &jose.SignerOptions{}
	opts.WithHeader(jose.HeaderKey("kid"), "test-key")
	signer, err := jose.NewSigner(key, opts)
	if err != nil {
		t.Fatalf("jose.NewSigner: %v", err)
	}
	sig, err := signer.Sign([]byte(payload))
	if err != nil {
		t.Fatalf("signer.Sign: %v", err)
	}
	raw, err := sig.CompactSerialize()
	if err != nil {
		t.Fatalf("CompactSerialize: %v", err)
	}
	return raw
}

// makeHandler builds a Handler with a given verifier.
// The store is nil — withAuth never touches it.
func makeHandler(v *oidc.IDTokenVerifier) *Handler {
	return &Handler{verifier: v}
}

// signTokenGroups signs a JWT carrying an oid and a groups array, for
// exercising admin-group authorization.
func (k *testKey) signTokenGroups(t *testing.T, aud, oid string, groups []string) string {
	t.Helper()
	now := time.Now()
	groupsJSON := "["
	for i, g := range groups {
		if i > 0 {
			groupsJSON += ","
		}
		groupsJSON += fmt.Sprintf("%q", g)
	}
	groupsJSON += "]"
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"oid":%q,"groups":%s,"iat":%d,"exp":%d}`,
		testIssuer, aud, oid, groupsJSON,
		now.Unix(),
		now.Add(time.Hour).Unix(),
	)
	key := jose.SigningKey{Algorithm: jose.RS256, Key: k.priv}
	opts := &jose.SignerOptions{}
	opts.WithHeader(jose.HeaderKey("kid"), "test-key")
	signer, err := jose.NewSigner(key, opts)
	if err != nil {
		t.Fatalf("jose.NewSigner: %v", err)
	}
	sig, err := signer.Sign([]byte(payload))
	if err != nil {
		t.Fatalf("signer.Sign: %v", err)
	}
	raw, err := sig.CompactSerialize()
	if err != nil {
		t.Fatalf("CompactSerialize: %v", err)
	}
	return raw
}

// recordingHandler is an http.HandlerFunc that records the OID from context.
func recordingHandler(oidOut *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*oidOut = userFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}
}

// --- withAuth tests ---

func TestWithAuthDisabled(t *testing.T) {
	// verifier == nil means no tenant configured; every request should pass
	// with oid == "dev".
	var gotOID string
	h := makeHandler(nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.withAuth(recordingHandler(&gotOID))(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if gotOID != "dev" {
		t.Errorf("oid = %q, want %q", gotOID, "dev")
	}
}

func TestWithAuthAudiences(t *testing.T) {
	k := newTestKey(t)

	cases := []struct {
		name     string
		tokenAud string
		wantCode int
		wantOID  string
	}{
		{
			name:     "client id matches",
			tokenAud: "app-id-1",
			wantCode: http.StatusOK,
			wantOID:  "user-oid-1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := k.signToken(t, tc.tokenAud, tc.wantOID, time.Hour)

			var gotOID string
			h := makeHandler(k.verifier)
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			h.withAuth(recordingHandler(&gotOID))(rr, req)

			if rr.Code != tc.wantCode {
				t.Fatalf("want %d, got %d", tc.wantCode, rr.Code)
			}
			if tc.wantCode == http.StatusOK && gotOID != tc.wantOID {
				t.Errorf("oid = %q, want %q", gotOID, tc.wantOID)
			}
		})
	}
}

func TestWithAuthRejects(t *testing.T) {
	k := newTestKey(t)
	wrongKey := newTestKey(t) // different key pair — signature won't verify

	validRaw := k.signToken(t, "app-id-1", "some-oid", time.Hour)
	wrongKeySigned := wrongKey.signToken(t, "app-id-1", "some-oid", time.Hour)
	expiredRaw := k.signToken(t, "app-id-1", "some-oid", -time.Hour)

	h := makeHandler(k.verifier)

	cases := []struct {
		name     string
		setupReq func(r *http.Request)
	}{
		{
			name:     "missing token",
			setupReq: func(r *http.Request) {},
		},
		{
			name:     "malformed Bearer value",
			setupReq: func(r *http.Request) { r.Header.Set("Authorization", "Bearer not.a.jwt") },
		},
		{
			name:     "wrong signing key",
			setupReq: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+wrongKeySigned) },
		},
		{
			name:     "expired token",
			setupReq: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+expiredRaw) },
		},
		{
			name: "Bearer prefix missing",
			setupReq: func(r *http.Request) {
				r.Header.Set("Authorization", validRaw) // no "Bearer " prefix
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			tc.setupReq(req)
			var gotOID string
			h.withAuth(recordingHandler(&gotOID))(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", rr.Code)
			}
		})
	}
}

// TestWithAuthAudArray verifies a token whose aud claim is a JSON array (the
// OIDC multi-audience case) is accepted. The handler decodes only the oid
// claim and leaves audience validation to the oidc verifier, so an array aud
// verifies and the request passes with the token's oid.
func TestWithAuthAudArray(t *testing.T) {
	k := newTestKey(t)
	wantOID := "some-oid"
	raw := k.signTokenAudArray(t, []string{"app-id-1", "app-id-2"}, wantOID)

	var gotOID string
	h := makeHandler(k.verifier)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	h.withAuth(recordingHandler(&gotOID))(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 for aud-as-array token, got %d", rr.Code)
	}
	if gotOID != wantOID {
		t.Errorf("oid = %q, want %q", gotOID, wantOID)
	}
}

func TestWithAuthOIDPropagated(t *testing.T) {
	k := newTestKey(t)
	wantOID := "azure-object-id-abc123"
	raw := k.signToken(t, "my-app", wantOID, time.Hour)

	var gotOID string
	h := makeHandler(k.verifier)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	h.withAuth(func(w http.ResponseWriter, r *http.Request) {
		gotOID = userFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if gotOID != wantOID {
		t.Errorf("oid = %q, want %q", gotOID, wantOID)
	}
}

// oauth2-proxy has already authenticated the request. Proxied routes read its
// forwarded identity headers and do not locally verify a token.
func TestWithForwardedIdentity(t *testing.T) {
	t.Setenv("INSIGHTS_ADMIN_GROUP", "admins")
	h := makeHandler(newTestKey(t).verifier)

	var gotOID string
	var gotAdmin bool
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/personal/usage", nil)
	req.Header.Set("X-Forwarded-User", "forwarded-user")
	req.Header.Set("X-Forwarded-Groups", " users, admins ")
	h.withForwardedIdentity(func(w http.ResponseWriter, r *http.Request) {
		gotOID = userFromContext(r.Context())
		gotAdmin = adminFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if gotOID != "forwarded-user" {
		t.Errorf("oid = %q, want %q", gotOID, "forwarded-user")
	}
	if !gotAdmin {
		t.Error("admin = false, want true")
	}
}

func TestWithForwardedIdentityRequiresUserHeader(t *testing.T) {
	h := makeHandler(newTestKey(t).verifier)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/personal/usage", nil)
	req.Header.Set("X-Forwarded-Groups", "admins")
	h.withForwardedIdentity(recordingHandler(new(string)))(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestWithForwardedAdmin(t *testing.T) {
	t.Setenv("INSIGHTS_ADMIN_GROUP", "admins")
	h := makeHandler(newTestKey(t).verifier)
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	for _, tc := range []struct {
		name   string
		groups []string
		want   int
	}{
		{name: "configured group passes", groups: []string{"admins"}, want: http.StatusOK},
		{name: "other group is forbidden", groups: []string{"users"}, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/filters", nil)
			req.Header.Set("X-Forwarded-User", "forwarded-user")
			req.Header.Set("X-Forwarded-Groups", strings.Join(tc.groups, ","))
			h.withForwardedAdmin(ok)(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("want %d, got %d", tc.want, rr.Code)
			}
		})
	}
}

func TestAPIMeRequiresForwardedIdentityNotAdminGroup(t *testing.T) {
	t.Setenv("INSIGHTS_ADMIN_GROUP", "admins")
	h := makeHandler(newTestKey(t).verifier)
	mux := http.NewServeMux()
	h.Register(mux)

	for _, tc := range []struct {
		name   string
		groups []string
		want   int
	}{
		{name: "admin group", groups: []string{"admins"}, want: http.StatusOK},
		{name: "non-admin group", groups: []string{"users"}, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			req.Header.Set("X-Forwarded-User", "forwarded-user")
			req.Header.Set("X-Forwarded-Groups", strings.Join(tc.groups, ","))
			mux.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("want %d, got %d", tc.want, rr.Code)
			}
		})
	}
}

// --- bearerToken tests ---

func TestBearerToken(t *testing.T) {
	cases := []struct {
		name      string
		auth      string
		forwarded string
		want      string
	}{
		{
			name: "Authorization Bearer header",
			auth: "Bearer my-token",
			want: "my-token",
		},
		{
			name:      "X-Forwarded-Access-Token fallback",
			forwarded: "forwarded-token",
			want:      "forwarded-token",
		},
		{
			name:      "Authorization takes precedence over X-Forwarded",
			auth:      "Bearer direct-token",
			forwarded: "forwarded-token",
			want:      "direct-token",
		},
		{
			name: "neither header present",
			want: "",
		},
		{
			name: "Authorization without Bearer prefix",
			auth: "Basic somebase64",
			want: "",
		},
		{
			name:      "X-Forwarded-Access-Token with surrounding whitespace",
			forwarded: "  trimmed-token  ",
			want:      "trimmed-token",
		},
		{
			name: "empty Bearer value",
			auth: "Bearer ",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Access-Token", tc.forwarded)
			}
			got := bearerToken(r)
			if got != tc.want {
				t.Errorf("bearerToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- userFromContext tests ---

func TestUserFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), userContextKey, "alice")
	if got := userFromContext(ctx); got != "alice" {
		t.Errorf("userFromContext = %q, want %q", got, "alice")
	}
}

func TestUserFromContextMissing(t *testing.T) {
	if got := userFromContext(context.Background()); got != "" {
		t.Errorf("userFromContext on empty ctx = %q, want empty", got)
	}
}

// --- admin group / withAdmin tests ---

func TestIsAdmin(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		groups []string
		want   bool
	}{
		{"no group configured: nobody admin", "", nil, false},
		{"no group configured, has groups: still not admin", "", []string{"x"}, false},
		{"configured, member", "admins", []string{"other", "admins"}, true},
		{"configured, not member", "admins", []string{"users"}, false},
		{"configured, no groups", "admins", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INSIGHTS_ADMIN_GROUP", tc.env)
			if got := isAdmin(tc.groups); got != tc.want {
				t.Errorf("isAdmin(%v) with env %q = %v, want %v", tc.groups, tc.env, got, tc.want)
			}
		})
	}
}

// TestWithAuthAdminPropagated verifies the admin flag derived from the groups
// claim reaches the handler context.
func TestWithAuthAdminPropagated(t *testing.T) {
	t.Setenv("INSIGHTS_ADMIN_GROUP", "admins")
	k := newTestKey(t)
	h := makeHandler(k.verifier)

	cases := []struct {
		name   string
		groups []string
		want   bool
	}{
		{"member is admin", []string{"admins"}, true},
		{"non-member is not admin", []string{"users"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := k.signTokenGroups(t, "my-app", "oid-1", tc.groups)
			var gotAdmin bool
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			h.withAuth(func(w http.ResponseWriter, r *http.Request) {
				gotAdmin = adminFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rr.Code)
			}
			if gotAdmin != tc.want {
				t.Errorf("admin = %v, want %v", gotAdmin, tc.want)
			}
		})
	}
}

// TestWithAdminGate verifies withAdmin returns 403 for a non-admin token, 200
// for an admin token, and lets everyone through when auth is disabled.
func TestWithAdminGate(t *testing.T) {
	t.Setenv("INSIGHTS_ADMIN_GROUP", "admins")
	k := newTestKey(t)
	h := makeHandler(k.verifier)
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	t.Run("admin passes", func(t *testing.T) {
		raw := k.signTokenGroups(t, "my-app", "oid-1", []string{"admins"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		h.withAdmin(ok)(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rr.Code)
		}
	})

	t.Run("non-admin forbidden", func(t *testing.T) {
		raw := k.signTokenGroups(t, "my-app", "oid-1", []string{"users"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		h.withAdmin(ok)(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rr.Code)
		}
	})

	t.Run("missing token unauthorized", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		h.withAdmin(ok)(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rr.Code)
		}
	})

	t.Run("auth disabled defaults to non-admin (personal only)", func(t *testing.T) {
		t.Setenv("INSIGHTS_DEV_ADMIN", "")
		noAuth := makeHandler(nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		noAuth.withAdmin(ok)(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rr.Code)
		}
	})

	t.Run("auth disabled with INSIGHTS_DEV_ADMIN passes as admin", func(t *testing.T) {
		t.Setenv("INSIGHTS_DEV_ADMIN", "true")
		noAuth := makeHandler(nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		noAuth.withAdmin(ok)(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rr.Code)
		}
	})
}
