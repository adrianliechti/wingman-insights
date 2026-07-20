package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// signTokenAudArray signs a JWT whose aud claim is a JSON array ([]string).
// This exercises the multi-audience case that the handler currently does NOT support.
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

// TestWithAuthAudArrayUnsupported documents that the handler decodes aud as a
// plain string. A token where aud is a JSON array (the OIDC multi-audience
// case) fails claims decoding and is rejected with 401. This is expected
// current behaviour; if multi-audience tokens are needed, auth.go should
// decode aud as []string.
func TestWithAuthAudArrayUnsupported(t *testing.T) {
	k := newTestKey(t)
	raw := k.signTokenAudArray(t, []string{"app-id-1", "app-id-2"}, "some-oid")

	h := makeHandler(k.verifier)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	h.withAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for aud-as-array token, got %d", rr.Code)
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
