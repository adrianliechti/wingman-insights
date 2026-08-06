package entra

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"insights/pkg/directory"
)

// fakeGraph serves the token, /users (two pages) and /applications endpoints
// the directory calls, and counts token requests so tests can assert the access
// token is cached across refreshes.
func fakeGraph(t *testing.T, tokenCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			tokenCalls.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-123",
				"expires_in":   3600,
			})

		case strings.HasSuffix(r.URL.Path, "/users"):
			if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
				t.Errorf("users request Authorization = %q, want Bearer tok-123", got)
			}
			if r.URL.Query().Get("page") == "2" {
				json.NewEncoder(w).Encode(map[string]any{
					"value": []map[string]any{
						{"id": "u2-objectid", "displayName": "Bob Builder", "userPrincipalName": "bob@contoso.com", "department": "abc", "officeLocation": "London"},
					},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{{
					"id":                "u1-objectid",
					"displayName":       "Alice Example",
					"userPrincipalName": "alice@contoso.com",
					"mail":              "alice.example@contoso.com",
					"otherMails":        []string{"alice.old@legacy.example"},
					"proxyAddresses":    []string{"SMTP:alice.example@contoso.com", "smtp:a.example@contoso.com"},
					"department":        "ab",
					"officeLocation":    "Zurich",
				}},
				// Absolute next-page link, as Graph returns it.
				"@odata.nextLink": "http://" + r.Host + "/v1.0/users?page=2",
			})

		case strings.HasSuffix(r.URL.Path, "/servicePrincipals"):
			json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					// The SP object id is the "oid" an app presents when calling;
					// appId is its stable client id.
					{"id": "sp-objectid", "appId": "app-clientid", "displayName": "Ingest Worker"},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
}

func testDirectory(t *testing.T, srv *httptest.Server) *Directory {
	t.Helper()
	d, err := New(Config{
		TenantID:     "test-tenant",
		ClientID:     "client",
		ClientSecret: "secret",
		GraphBaseURL: srv.URL + "/v1.0",
		LoginBaseURL: srv.URL,
		Logf:         func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestLookup(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := fakeGraph(t, &tokenCalls)
	defer srv.Close()

	d := testDirectory(t, srv)
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	tests := []struct {
		name     string
		id       string
		wantID   string
		wantKnd  directory.Kind
		wantName string
	}{
		{"user by object id", "u1-objectid", "u1-objectid", directory.KindUser, "Alice Example"},
		{"user by upn", "alice@contoso.com", "u1-objectid", directory.KindUser, "Alice Example"},
		{"user by upn case-insensitive", "ALICE@Contoso.com", "u1-objectid", directory.KindUser, "Alice Example"},
		{"user by mail", "alice.example@contoso.com", "u1-objectid", directory.KindUser, "Alice Example"},
		{"user by other mail", "alice.old@legacy.example", "u1-objectid", directory.KindUser, "Alice Example"},
		{"user by proxy alias", "a.example@contoso.com", "u1-objectid", directory.KindUser, "Alice Example"},
		{"user from second page", "u2-objectid", "u2-objectid", directory.KindUser, "Bob Builder"},
		{"user2 by upn", "bob@contoso.com", "u2-objectid", directory.KindUser, "Bob Builder"},
		{"app by client id", "app-clientid", "app-clientid", directory.KindApplication, "Ingest Worker"},
		{"app by service principal object id", "sp-objectid", "app-clientid", directory.KindApplication, "Ingest Worker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := d.Lookup(tc.id)
			if !ok {
				t.Fatalf("Lookup(%q) miss, want hit", tc.id)
			}
			if got.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tc.wantID)
			}
			if got.Kind != tc.wantKnd {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKnd)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
		})
	}

	if _, ok := d.Lookup("nobody@nowhere"); ok {
		t.Error("Lookup of unknown id returned a hit")
	}

	// A second refresh must reuse the cached access token.
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if n := tokenCalls.Load(); n != 1 {
		t.Errorf("token endpoint called %d times, want 1 (token should be cached)", n)
	}
}

func TestIdentityAttributes(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := fakeGraph(t, &tokenCalls)
	defer srv.Close()

	d := testDirectory(t, srv)
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	got, ok := d.Lookup("alice@contoso.com")
	if !ok {
		t.Fatal("Lookup(alice) miss")
	}
	if got.Department != "ab" || got.Location != "Zurich" {
		t.Errorf("Alice attrs = %q/%q, want ab/Zurich", got.Department, got.Location)
	}
}

func TestUsernameStripPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-123", "expires_in": 3600})
		case strings.HasSuffix(r.URL.Path, "/users"):
			json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{{"id": "u1", "displayName": "Ivo Zumbrunn", "userPrincipalName": "uivz@contoso.com"}},
			})
		case strings.HasSuffix(r.URL.Path, "/servicePrincipals"):
			json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d, err := New(Config{
		TenantID: "test-tenant", ClientID: "client", ClientSecret: "secret",
		GraphBaseURL: srv.URL + "/v1.0", LoginBaseURL: srv.URL,
		UsernameStripPrefix: "^[uec]",
		Logf:                func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, ok := d.Lookup("uivz@contoso.com")
	if !ok {
		t.Fatal("Lookup miss")
	}
	if got.Username != "ivz" {
		t.Errorf("Username = %q, want %q", got.Username, "ivz")
	}
}

func TestUsernameStripPrefixInvalidRegexp(t *testing.T) {
	if _, err := New(Config{
		TenantID: "t", ClientID: "c", ClientSecret: "s",
		UsernameStripPrefix: "(unterminated",
	}); err == nil {
		t.Error("want error for invalid UsernameStripPrefix regexp")
	}
}

func TestExport(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := fakeGraph(t, &tokenCalls)
	defer srv.Close()

	d := testDirectory(t, srv)
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	var buf bytes.Buffer
	if ok, err := directory.Export(d, &buf); err != nil || !ok {
		t.Fatalf("Export: ok=%v err=%v", ok, err)
	}
	// NDJSON: one record per line, decoded with a streaming decoder.
	byAlias := map[string]directory.Record{}
	dec := json.NewDecoder(&buf)
	for {
		var r directory.Record
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode: %v", err)
		}
		byAlias[r.Alias] = r
	}

	// Every alias of Alice points at one identity, carrying department/location.
	if r := byAlias["alice@contoso.com"]; r.ID != "u1-objectid" || r.Department != "ab" || r.Location != "Zurich" {
		t.Errorf("alice (by upn) record = %+v", r)
	}
	if r := byAlias["a.example@contoso.com"]; r.ID != "u1-objectid" {
		t.Errorf("alice (by proxy alias) record = %+v", r)
	}
	// Apps export too, keyed by their ids, with no department/location.
	if r := byAlias["app-clientid"]; r.ID != "app-clientid" || r.Kind != directory.KindApplication || r.Department != "" {
		t.Errorf("app record = %+v", r)
	}
}

// TestRetriesOnThrottle proves a 429 mid-paging is retried rather than aborting
// the whole refresh — the realistic failure when listing 20k+ objects.
func TestRetriesOnThrottle(t *testing.T) {
	var userCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-123", "expires_in": 3600})
		case strings.HasSuffix(r.URL.Path, "/users"):
			if userCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests) // first hit throttled
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{{"id": "u1", "displayName": "Alice", "userPrincipalName": "alice@contoso.com"}},
			})
		case strings.HasSuffix(r.URL.Path, "/servicePrincipals"):
			json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := testDirectory(t, srv)
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh should recover from a 429: %v", err)
	}
	if _, ok := d.Lookup("alice@contoso.com"); !ok {
		t.Fatal("user not resolved after a throttled retry")
	}
	if n := userCalls.Load(); n != 2 {
		t.Errorf("users endpoint hit %d times, want 2 (one 429 + one success)", n)
	}
}

func TestLazyRefreshOnLookup(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := fakeGraph(t, &tokenCalls)
	defer srv.Close()

	d := testDirectory(t, srv)

	// Cold: no snapshot yet, so the first lookup misses but triggers a load.
	if _, ok := d.Lookup("alice@contoso.com"); ok {
		t.Fatal("cold Lookup unexpectedly hit")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := d.Lookup("alice@contoso.com"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lazy refresh did not populate the directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New(Config{ClientID: "c", ClientSecret: "s"}); err == nil {
		t.Error("want error when TenantID is missing")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(envTenantID, "")
	t.Setenv(envClientID, "")
	t.Setenv(envClientSecret, "")
	if _, ok := FromEnv(); ok {
		t.Error("FromEnv with no env set should report ok=false")
	}

	t.Setenv(envTenantID, "tenant")
	t.Setenv(envClientID, "client")
	t.Setenv(envClientSecret, "secret")
	if _, ok := FromEnv(); !ok {
		t.Error("FromEnv with full env should report ok=true")
	}
}

func TestSMTPAddress(t *testing.T) {
	cases := []struct {
		in  string
		out string
		ok  bool
	}{
		{"SMTP:Alice@contoso.com", "Alice@contoso.com", true}, // primary, upper scheme
		{"smtp:a@contoso.com", "a@contoso.com", true},         // alias, lower scheme
		{"X500:/o=Exch/cn=Recipients/cn=x", "", false},        // non-SMTP, not indexed
		{"SIP:alice@contoso.com", "", false},                  // non-SMTP
		{"plain@contoso.com", "", false},                      // no scheme
	}
	for _, c := range cases {
		got, ok := smtpAddress(c.in)
		if got != c.out || ok != c.ok {
			t.Errorf("smtpAddress(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.out, c.ok)
		}
	}
}
