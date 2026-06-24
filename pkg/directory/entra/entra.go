// Package entra implements directory.Directory against Microsoft Graph. It
// lists all users and application registrations in bulk, indexes every
// identifier each exposes (object id, UPN, mail, proxy/other addresses, app id),
// and serves lookups from an in-memory snapshot refreshed lazily on a TTL —
// so steady-state lookups cost nothing and Graph is hit at most once per
// refresh interval regardless of lookup volume.
package entra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"insights/pkg/directory"
)

// Microsoft public-cloud endpoints. Override via Config for sovereign clouds
// (e.g. graph.microsoft.us) or for tests.
const (
	defaultGraphBase = "https://graph.microsoft.com/v1.0"
	defaultLoginBase = "https://login.microsoftonline.com"

	// graphScope is the client-credentials scope: ".default" requests the
	// application permissions already consented for the app registration
	// (here User.Read.All and Application.Read.All).
	graphScope = "https://graph.microsoft.com/.default"
)

const (
	defaultRefreshInterval = 6 * time.Hour    // how long a snapshot is served before a refresh
	refreshTimeout         = 5 * time.Minute  // budget for a full users+apps refresh incl. throttling backoff
	minRefreshGap          = time.Minute      // floor between refresh attempts (throttles retries)
	httpTimeout            = 30 * time.Second // per-request (single page) ceiling
	pageSize               = 999              // Graph's maximum $top for directory objects

	maxRetries     = 5                      // per-request retries on throttling / transient errors
	maxPages       = 5000                   // backstop against a looping @odata.nextLink (~5M objects)
	retryBaseDelay = 500 * time.Millisecond // first backoff step; doubles each retry
	retryMaxDelay  = 30 * time.Second       // cap on our own exponential backoff
)

// Environment variables read by FromEnv.
const (
	envTenantID     = "INSIGHTS_ENTRA_TENANT_ID"
	envClientID     = "INSIGHTS_ENTRA_CLIENT_ID"
	envClientSecret = "INSIGHTS_ENTRA_CLIENT_SECRET"
)

// Config configures an Entra-backed directory. TenantID, ClientID and
// ClientSecret are required; the rest have sane defaults.
type Config struct {
	TenantID     string
	ClientID     string
	ClientSecret string

	// RefreshInterval is how long a loaded snapshot is served before the next
	// Lookup triggers a background refresh. Zero uses defaultRefreshInterval.
	RefreshInterval time.Duration

	// HTTPClient is used for all token and Graph requests. Zero uses a client
	// with a 30s timeout.
	HTTPClient *http.Client

	// GraphBaseURL and LoginBaseURL override the Microsoft endpoints. Zero uses
	// the public-cloud defaults.
	GraphBaseURL string
	LoginBaseURL string

	// Logf records background refresh failures. Zero uses log.Printf.
	Logf func(format string, args ...any)
}

// snapshot is an immutable index built from one full directory load. It is
// published via atomic.Pointer and never mutated after publication, so readers
// need no lock.
type snapshot struct {
	byKey    map[string]directory.Identity // NormalizeKey(alias) -> identity
	byCanon  map[string][]string           // NormalizeKey(canonical id) -> its alias keys
	loadedAt time.Time
}

// Directory is the Microsoft Graph-backed directory.Directory implementation.
type Directory struct {
	cfg  Config
	http *http.Client

	snap atomic.Pointer[snapshot]

	// access token cache, valid until tokExpiry.
	tokMu     sync.Mutex
	token     string
	tokExpiry time.Time

	// refresh single-flight + retry throttle.
	refMu       sync.Mutex
	refreshing  bool
	lastAttempt time.Time
}

var (
	_ directory.Directory = (*Directory)(nil)
	_ directory.Aliaser   = (*Directory)(nil)
)

// New validates cfg and returns a directory. It performs no I/O: the first
// directory load happens lazily on the first Lookup (or eagerly if you call
// Refresh). It errors only when required credentials are missing.
func New(cfg Config) (*Directory, error) {
	if cfg.TenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("entra: TenantID, ClientID and ClientSecret are required")
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = defaultRefreshInterval
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	return &Directory{cfg: cfg, http: cfg.HTTPClient}, nil
}

// FromEnv builds an Entra directory from INSIGHTS_ENTRA_TENANT_ID / _CLIENT_ID /
// _CLIENT_SECRET. ok is false when any are unset, so the caller can fall back to
// a noop directory.
func FromEnv() (d *Directory, ok bool) {
	d, err := New(Config{
		TenantID:     os.Getenv(envTenantID),
		ClientID:     os.Getenv(envClientID),
		ClientSecret: os.Getenv(envClientSecret),
	})
	if err != nil {
		return nil, false
	}
	return d, true
}

// Lookup resolves an identifier from the current snapshot. If no snapshot has
// loaded yet, or the loaded one is older than RefreshInterval, it kicks off a
// background refresh and serves whatever it currently has (a miss on cold
// start, the stale snapshot otherwise) without blocking.
func (d *Directory) Lookup(id string) (directory.Identity, bool) {
	s := d.snap.Load()
	if s == nil || time.Since(s.loadedAt) >= d.cfg.RefreshInterval {
		d.maybeRefresh()
	}
	if s == nil {
		return directory.Identity{}, false
	}
	idt, ok := s.byKey[directory.NormalizeKey(id)]
	return idt, ok
}

// Aliases returns every identifier that resolves to the same principal as id
// (including id's own canonical key), for alias-expanded filtering. It returns
// nil when id is unknown or no snapshot has loaded.
func (d *Directory) Aliases(id string) []string {
	s := d.snap.Load()
	if s == nil {
		return nil
	}
	key := directory.NormalizeKey(id)
	// Resolve to the canonical key first so any alias works as input.
	if idt, ok := s.byKey[key]; ok {
		key = directory.NormalizeKey(idt.ID)
	}
	return s.byCanon[key]
}

// maybeRefresh starts a background refresh unless one is already running or the
// previous attempt was too recent (throttling retries during an outage).
func (d *Directory) maybeRefresh() {
	d.refMu.Lock()
	if d.refreshing || time.Since(d.lastAttempt) < minRefreshGap {
		d.refMu.Unlock()
		return
	}
	d.refreshing = true
	d.lastAttempt = time.Now()
	d.refMu.Unlock()

	go func() {
		defer func() {
			d.refMu.Lock()
			d.refreshing = false
			d.refMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		if err := d.Refresh(ctx); err != nil {
			d.cfg.Logf("directory: entra refresh failed: %v", err)
		}
	}()
}

// Refresh performs a full directory load and atomically publishes a new
// snapshot. It is safe to call directly to warm the cache at startup; lookups
// otherwise drive it automatically.
func (d *Directory) Refresh(ctx context.Context) error {
	tok, err := d.accessToken(ctx)
	if err != nil {
		return fmt.Errorf("acquire token: %w", err)
	}
	byKey := make(map[string]directory.Identity)
	if err := d.loadUsers(ctx, tok, byKey); err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	if err := d.loadApps(ctx, tok, byKey); err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	// Build the reverse index (canonical id -> its alias keys) for Aliases.
	byCanon := make(map[string][]string, len(byKey))
	for key, idt := range byKey {
		ck := directory.NormalizeKey(idt.ID)
		byCanon[ck] = append(byCanon[ck], key)
	}
	d.snap.Store(&snapshot{byKey: byKey, byCanon: byCanon, loadedAt: time.Now()})
	return nil
}

// accessToken returns a cached client-credentials token, fetching a fresh one
// when none is cached or the current one is near expiry.
func (d *Directory) accessToken(ctx context.Context) (string, error) {
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	if d.token != "" && time.Now().Before(d.tokExpiry) {
		return d.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {d.cfg.ClientID},
		"client_secret": {d.cfg.ClientSecret},
		"scope":         {graphScope},
	}
	endpoint := d.loginBase() + "/" + d.cfg.TenantID + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tr struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode token response (%s): %w", resp.Status, err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		if tr.Error != "" {
			return "", fmt.Errorf("token endpoint: %s: %s", tr.Error, tr.ErrorDescription)
		}
		return "", fmt.Errorf("token endpoint: %s", resp.Status)
	}

	d.token = tr.AccessToken
	// Renew a minute early to avoid races against the hard expiry.
	d.tokExpiry = time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - time.Minute)
	return d.token, nil
}

// graphUser mirrors the $select-ed fields of a Graph user object. The address
// and username fields are indexed as lookup keys so an incoming user.id can be
// an object id, an email (UPN / mail / alias), or a bare username (the cloud
// mailNickname or the on-prem sAMAccountName) and still resolve to one identity.
type graphUser struct {
	ID                       string   `json:"id"`
	DisplayName              string   `json:"displayName"`
	UserPrincipalName        string   `json:"userPrincipalName"`
	Mail                     string   `json:"mail"`
	MailNickname             string   `json:"mailNickname"`
	OnPremisesSamAccountName string   `json:"onPremisesSamAccountName"`
	OtherMails               []string `json:"otherMails"`
	ProxyAddresses           []string `json:"proxyAddresses"`
}

func (d *Directory) loadUsers(ctx context.Context, tok string, dst map[string]directory.Identity) error {
	first := d.graphBase() + "/users?$select=id,displayName,userPrincipalName,mail,mailNickname,onPremisesSamAccountName,otherMails,proxyAddresses&$top=" + strconv.Itoa(pageSize)
	return fetchPaged(ctx, d, tok, first, func(users []graphUser) {
		for _, u := range users {
			idt := directory.Identity{
				ID:   u.ID,
				Name: u.DisplayName,
				Kind: directory.KindUser,
			}
			// Primary identifiers are authoritative and overwrite; secondary
			// aliases (extra emails, usernames) only fill gaps so they can't
			// shadow a primary key.
			put(dst, u.ID, idt, true)
			put(dst, u.UserPrincipalName, idt, true)
			put(dst, u.Mail, idt, true)
			put(dst, u.MailNickname, idt, false)
			put(dst, u.OnPremisesSamAccountName, idt, false)
			for _, m := range u.OtherMails {
				put(dst, m, idt, false)
			}
			for _, p := range u.ProxyAddresses {
				put(dst, stripScheme(p), idt, false)
			}
		}
	})
}

// graphApp mirrors the $select-ed fields of a Graph application object.
type graphApp struct {
	ID          string `json:"id"`    // directory object id
	AppID       string `json:"appId"` // client id, the stable public identifier
	DisplayName string `json:"displayName"`
}

func (d *Directory) loadApps(ctx context.Context, tok string, dst map[string]directory.Identity) error {
	first := d.graphBase() + "/applications?$select=id,appId,displayName&$top=" + strconv.Itoa(pageSize)
	return fetchPaged(ctx, d, tok, first, func(apps []graphApp) {
		for _, a := range apps {
			idt := directory.Identity{
				ID:   a.AppID,
				Name: a.DisplayName,
				Kind: directory.KindApplication,
			}
			// An app may surface in telemetry as either its client id or its
			// object id; index both.
			put(dst, a.AppID, idt, true)
			put(dst, a.ID, idt, true)
		}
	})
}

// fetchPaged walks a Graph collection from first, following @odata.nextLink
// until exhausted, and hands each page's items to consume. It is generic over
// the element type so users and applications share one correct paging loop.
// nextLink values are absolute and used verbatim, as Graph requires. maxPages
// backstops a malformed self-referential link.
func fetchPaged[T any](ctx context.Context, d *Directory, tok, first string, consume func([]T)) error {
	for next, pages := first, 0; next != ""; pages++ {
		if pages >= maxPages {
			return fmt.Errorf("paging exceeded %d pages", maxPages)
		}
		var page struct {
			Value    []T    `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := d.getJSON(ctx, tok, next, &page); err != nil {
			return err
		}
		consume(page.Value)
		next = page.NextLink
	}
	return nil
}

// getJSON performs an authenticated Graph GET and decodes the JSON body,
// retrying transient failures — HTTP 429 (throttling) and 5xx, plus transport
// errors — up to maxRetries with backoff that honours Retry-After. Other
// non-200 responses (e.g. 401/403) are returned immediately. The caller passes
// absolute URLs, including @odata.nextLink values, verbatim.
func (d *Directory) getJSON(ctx context.Context, tok, rawURL string, out any) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		suggested, retryable, err := d.tryGet(ctx, tok, rawURL, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt >= maxRetries {
			return lastErr
		}
		// Honour the server's Retry-After when given; otherwise back off
		// exponentially. ctx bounds the total wait.
		delay := suggested
		if delay <= 0 {
			delay = backoff(attempt)
		}
		if werr := sleepCtx(ctx, delay); werr != nil {
			return werr
		}
	}
}

// tryGet performs one Graph GET attempt. On 200 it decodes into out and returns
// a nil error. Otherwise it reports whether the failure is transient (worth
// retrying) and, for throttling, the server-suggested wait.
func (d *Directory) tryGet(ctx context.Context, tok, rawURL string, out any) (suggested time.Duration, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return 0, true, err // transport error — transient
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return 0, false, json.NewDecoder(resp.Body).Decode(out)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return retryAfter(resp.Header), true, fmt.Errorf("GET %s: %s: %s", rawURL, resp.Status, strings.TrimSpace(string(body)))
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return 0, false, fmt.Errorf("GET %s: %s: %s", rawURL, resp.Status, strings.TrimSpace(string(body)))
	}
}

// retryAfter parses a Retry-After header in delta-seconds form (what Graph
// sends), returning 0 when absent or unparseable so the caller falls back to
// exponential backoff. ctx — not a clamp here — bounds an over-large value.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// backoff returns an exponential delay for a zero-based retry attempt
// (500ms, 1s, 2s, …), capped at retryMaxDelay.
func backoff(attempt int) time.Duration {
	delay := retryBaseDelay << attempt
	if delay <= 0 || delay > retryMaxDelay { // <=0 guards the shift overflowing
		return retryMaxDelay
	}
	return delay
}

// sleepCtx waits for delay or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *Directory) graphBase() string {
	if d.cfg.GraphBaseURL != "" {
		return strings.TrimRight(d.cfg.GraphBaseURL, "/")
	}
	return defaultGraphBase
}

func (d *Directory) loginBase() string {
	if d.cfg.LoginBaseURL != "" {
		return strings.TrimRight(d.cfg.LoginBaseURL, "/")
	}
	return defaultLoginBase
}

// put indexes id under NormalizeKey(key). A non-empty key is skipped when it
// already holds a value and force is false, so authoritative (primary) keys win
// over secondary aliases regardless of load order.
func put(dst map[string]directory.Identity, key string, id directory.Identity, force bool) {
	k := directory.NormalizeKey(key)
	if k == "" {
		return
	}
	if _, exists := dst[k]; exists && !force {
		return
	}
	dst[k] = id
}

// stripScheme drops a leading "smtp:"/"SMTP:"-style prefix from a proxy
// address, leaving the bare address to index.
func stripScheme(p string) string {
	if i := strings.IndexByte(p, ':'); i >= 0 {
		return p[i+1:]
	}
	return p
}
