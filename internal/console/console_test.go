// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// isolatedCacheDir points os.UserCacheDir() (via XDG_CACHE_HOME) at a fresh
// temp dir for the duration of the test, so New/NewWithOptions — which
// always fetch into defaultCacheRoot() — never touch the real machine's
// cache.
func isolatedCacheDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func mustNewOptions(t *testing.T, brainURL, token string, readOnly, allowUnsigned bool) http.Handler {
	t.Helper()
	isolatedCacheDir(t)
	h, err := NewWithOptions(brainURL, token, readOnly, allowUnsigned)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return h
}

// fetchIndexCookie GETs "/" off con and extracts the *http.Cookie the entry
// document's response sets carrying the per-session secret (consoleSessionCookie)
// — the mechanism transports that can't set headers (EventSource, WebSocket)
// actually rely on. Fails the test if no such cookie is set.
func fetchIndexCookie(t *testing.T, con http.Handler) (body string, cookie *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	// Host must be a literal loopback form to pass hostGate — a real browser
	// against this console always is (127.0.0.1/localhost), so the fixture
	// mirrors that rather than httptest's "example.com" default.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8080"
	con.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status %d, body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	for _, c := range rec.Result().Cookies() {
		if c.Name == consoleSessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("served entry document response set no %q cookie (Set-Cookie headers: %v)", consoleSessionCookie, rec.Result().Header["Set-Cookie"])
	}
	return body, cookie
}

// fetchIndexAndSecret is fetchIndexCookie, returning just the secret value —
// the shape most existing (header-path) tests want.
func fetchIndexAndSecret(t *testing.T, con http.Handler) (body, secret string) {
	t.Helper()
	body, cookie := fetchIndexCookie(t, con)
	return body, cookie.Value
}

// apiRequest builds a request against con at target (the console's own
// origin, e.g. "http://127.0.0.1:8080/api/ping" — req.Host is derived from
// it, mimicking what a real browser request against that origin looks
// like), with originHeader as the literal Origin header value (which a
// hostile page can set to anything) and secret as the session header.
func apiRequest(method, target, originHeader, secret string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader("{}"))
	if originHeader != "" {
		req.Header.Set("Origin", originHeader)
	}
	if secret != "" {
		req.Header.Set(consoleSessionHeader, secret)
	}
	return req
}

func TestNewServesCachedBundleEntry(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", true, false)

	body, secret := fetchIndexAndSecret(t, con)
	if !strings.Contains(body, "hello console") {
		t.Errorf("served body missing expected entry content: %s", body)
	}
	if secret == "" {
		t.Error("session secret was empty")
	}
}

func TestServedPageNeverContainsBearer(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "super-secret-bearer-token", true, false)

	body, _ := fetchIndexAndSecret(t, con)
	if strings.Contains(body, "super-secret-bearer-token") {
		t.Fatal("served index.html contains the bearer token")
	}
}

func TestAPIProxiedWithValidOriginAndSession(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)
	_, secret := fetchIndexAndSecret(t, con)

	const origin = "http://127.0.0.1:8080"
	req := apiRequest(http.MethodGet, origin+"/api/ping", origin, secret)
	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ping: status %d, body=%s", rec.Code, rec.Body.String())
	}
	if d.lastAuth != "Bearer tok123" {
		t.Fatalf("upstream Authorization = %q, want Bearer tok123", d.lastAuth)
	}
	if d.apiHits != 1 {
		t.Fatalf("apiHits = %d, want 1", d.apiHits)
	}
}

func TestAPIRefusedWithoutSessionOrForeignOrigin(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)
	_, secret := fetchIndexAndSecret(t, con)
	const origin = "http://127.0.0.1:8080"

	cases := []struct {
		name   string
		origin string
		secret string
	}{
		{"no origin no session", "", ""},
		{"valid origin, no session", origin, ""},
		{"valid origin, wrong session", origin, "0000000000000000000000000000000000000000000000000000000000000000"},
		{"foreign origin, valid session", "http://evil.example", secret},
		{"no origin, valid session", "", secret},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := d.apiHits
			req := apiRequest(http.MethodGet, origin+"/api/ping", c.origin, c.secret)
			rec := httptest.NewRecorder()
			con.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (CSRF refusal)", rec.Code)
			}
			if d.apiHits != before {
				t.Fatalf("request reached the brain (hits %d -> %d); CSRF refusal must happen before proxying", before, d.apiHits)
			}
		})
	}
}

func TestLoopbackHostAllowsRealLoopbackForms(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", true, false)

	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		t.Run(host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
			req.Host = host
			rec := httptest.NewRecorder()
			con.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("Host %q was refused by the loopback gate: %d %s", host, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestReboundHostRefusedBeforeSecretLeaks is the DNS-rebinding regression
// test: a request whose Host header is a rebound non-loopback name (what a
// browser sends after evil.com's DNS is rebound to 127.0.0.1) must be
// refused by the console's own Host gate BEFORE bundleHandler ever runs —
// otherwise the injected per-session secret becomes readable same-origin by
// the attacker's page, defeating the CSRF gate downstream.
func TestReboundHostRefusedBeforeSecretLeaks(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", true, false)

	const reboundHost = "evil.com:8080"
	req := httptest.NewRequest(http.MethodGet, "http://"+reboundHost+"/", nil)
	req.Host = reboundHost
	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET / with rebound Host %q: status %d, want 403", reboundHost, rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == consoleSessionCookie {
			t.Fatal("response to a rebound Host leaked the session-secret cookie")
		}
	}
}

func TestReboundHostRefusedOnAPIRoute(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)
	_, secret := fetchIndexAndSecret(t, con)

	const reboundHost = "evil.com"
	req := apiRequest(http.MethodPost, "http://"+reboundHost+"/api/instruct", "http://"+reboundHost, secret)
	req.Host = reboundHost
	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/instruct with rebound Host %q: status %d, want 403", reboundHost, rec.Code)
	}
	if d.apiHits != 0 {
		t.Fatalf("request reached the brain (%d hits); the host gate must refuse before proxying", d.apiHits)
	}
}

func TestSameOriginRequiresMatchingScheme(t *testing.T) {
	// Host matches, but the Origin claims https while the request itself is
	// plain http (r.TLS == nil) — sameOrigin must not admit this: it would
	// let a scheme-confused Origin (e.g. an https page whose host happens to
	// collide with the console's Host string) ride as same-origin.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/ping", nil)
	req.Header.Set("Origin", "https://127.0.0.1:8080")
	if sameOrigin(req) {
		t.Fatal("sameOrigin admitted an Origin whose scheme (https) does not match the request's scheme (http)")
	}
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", true, false) // read-only
	_, secret := fetchIndexAndSecret(t, con)
	const origin = "http://127.0.0.1:8080"

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := apiRequest(m, origin+"/api/ping", origin, secret)
		rec := httptest.NewRecorder()
		con.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403 (read-only)", m, rec.Code)
		}
	}
	if d.apiHits != 0 {
		t.Fatalf("a write reached the brain (%d hits); read-only must refuse before proxying", d.apiHits)
	}
}

func TestReadWriteForwardsWrites(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false) // read-write (admin console)
	_, secret := fetchIndexAndSecret(t, con)
	const origin = "http://127.0.0.1:8080"

	req := apiRequest(http.MethodPost, origin+"/api/ping", origin, secret)
	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/ping in read-write mode: status %d, want 200 (forwarded)", rec.Code)
	}
	if d.apiHits != 1 {
		t.Fatalf("write did not reach the brain (%d hits); read-write must forward", d.apiHits)
	}
}

func TestVersionBumpCausesRefetch(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	isolatedCacheDir(t)

	con1, err := NewWithOptions(srv.URL, "tok123", true, false)
	if err != nil {
		t.Fatalf("NewWithOptions v1: %v", err)
	}
	body1, _ := fetchIndexAndSecret(t, con1)
	if !strings.Contains(body1, "hello console") {
		t.Fatalf("v1 body missing expected content: %s", body1)
	}

	d.setVersion("v2", []fakeAsset{
		{"index.html", []byte("<html><head></head><body>hello v2 console</body></html>")},
		{"app.js", []byte("console.log('app v2')")},
	})

	con2, err := NewWithOptions(srv.URL, "tok123", true, false)
	if err != nil {
		t.Fatalf("NewWithOptions v2: %v", err)
	}
	body2, _ := fetchIndexAndSecret(t, con2)
	if !strings.Contains(body2, "hello v2 console") {
		t.Fatalf("v2 body missing expected content: %s", body2)
	}
}

func TestHealthReflectsUpstream(t *testing.T) {
	d := newFakeDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", true, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	req.Host = "127.0.0.1:8080"
	con.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health with brain up: status %d, want 200", rec.Code)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	isolatedCacheDir(t)
	for _, bad := range []string{"", "not-a-url", "brain.example" /* no scheme */} {
		if _, err := New(bad, "tok", true); err == nil {
			t.Fatalf("New(%q) = nil error, want rejection", bad)
		}
	}
}

func TestNewRefusesUnsignedBundleByDefault(t *testing.T) {
	d := newFakeDaemon(t)
	d.setSigMode("missing")
	srv := d.server(t)
	isolatedCacheDir(t)

	if _, err := New(srv.URL, "tok123", true); err == nil {
		t.Fatal("New succeeded against an unsigned daemon, want refusal")
	}
	if _, err := NewWithOptions(srv.URL, "tok123", true, true); err != nil {
		t.Fatalf("NewWithOptions with allowUnsigned=true: %v", err)
	}
}

func TestLocalDialHost(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8080":   "127.0.0.1:8080",
		":8080":          "127.0.0.1:8080",
		"127.0.0.1:9000": "127.0.0.1:9000",
	}
	for in, want := range cases {
		if got := LocalDialHost(in); got != want {
			t.Fatalf("LocalDialHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Part B: the real end-to-end regression test ---
//
// Every test above proves the CSRF gate's own logic is sound using a
// SYNTHETIC bundle and a request that manually sets consoleSessionHeader.
// That's exactly the blind spot that let the real bug through three prior
// task-level reviews: the actual SPA (EventSource, WebSocket, bare fetch())
// never sends that header at all. These tests instead drive the REAL,
// production internal/ui/web/ bundle (newRealBundleDaemon — real index.html,
// signed with the actual committed dev signature) through the console's own
// entry-document response, and issue follow-up requests carrying ONLY the
// cookie the browser would actually have received — no manually-set session
// header anywhere below.

// TestRealEntryDocSetsSessionCookie is the Part B "cookie is set on entry-doc
// load" check: GET / (loopback Host) against the REAL served bundle must set
// a Set-Cookie for consoleSessionCookie that is HttpOnly, SameSite=Strict,
// Path=/, and NOT Secure (the console is plain http:// — Secure would
// silently suppress the cookie and re-break the cockpit).
func TestRealEntryDocSetsSessionCookie(t *testing.T) {
	d := newRealBundleDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)

	body, cookie := fetchIndexCookie(t, con)
	if !strings.Contains(body, "CorralAI") {
		t.Fatalf("served entry document doesn't look like the real internal/ui/web/index.html: %s", body[:min(200, len(body))])
	}
	if cookie.Value == "" {
		t.Fatal("session cookie value is empty")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", cookie.Path)
	}
	if cookie.Secure {
		t.Error("session cookie is Secure — this console is plain http:// and Secure would suppress it, re-breaking the cockpit")
	}
}

// TestCookieOnlyRequestPassesGate is the Part B killer test: a browser-shaped
// request — same-origin Origin, loopback Host, the session cookie the real
// served entry document set, and CRUCIALLY no consoleSessionHeader at all —
// must NOT be refused by csrfGate, for both /api/state (the SPA's plain
// fetch() calls) and /events (EventSource, which physically cannot set a
// custom header). Before the fix (header-only validSession, no cookie ever
// set) this fails with 403 on both.
func TestCookieOnlyRequestPassesGate(t *testing.T) {
	d := newRealBundleDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)
	_, cookie := fetchIndexCookie(t, con)

	const origin = "http://127.0.0.1:8080"
	for _, path := range []string{"/api/state", "/events"} {
		t.Run(path, func(t *testing.T) {
			before := d.apiHits
			req := httptest.NewRequest(http.MethodGet, origin+path, nil)
			req.Header.Set("Origin", origin)
			req.AddCookie(cookie)
			// Deliberately NOT setting consoleSessionHeader — the real SPA
			// (bare fetch()/EventSource) never does.
			rec := httptest.NewRecorder()
			con.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("GET %s cookie-only (no session header): status 403, want passed through to upstream; body=%s", path, rec.Body.String())
			}
			if d.apiHits != before+1 {
				t.Fatalf("GET %s cookie-only: apiHits %d -> %d, want +1 (request should have reached the fake upstream)", path, before, d.apiHits)
			}
		})
	}
}

// TestCookieOnlyRequestStillFailsClosed proves the cookie addition didn't
// weaken csrfGate's other two defenses: a cross-origin request carrying the
// valid cookie is still refused (sameOrigin still bites), and a request with
// neither cookie nor header is still refused.
func TestCookieOnlyRequestStillFailsClosed(t *testing.T) {
	d := newRealBundleDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)
	_, cookie := fetchIndexCookie(t, con)
	const origin = "http://127.0.0.1:8080"

	t.Run("valid cookie, foreign origin", func(t *testing.T) {
		before := d.apiHits
		req := httptest.NewRequest(http.MethodGet, origin+"/api/state", nil)
		req.Header.Set("Origin", "http://evil.com")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		con.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (cross-origin must still be refused even with a valid cookie)", rec.Code)
		}
		if d.apiHits != before {
			t.Fatalf("request reached the brain (hits %d -> %d); cross-origin must be refused before proxying", before, d.apiHits)
		}
	})

	t.Run("no cookie, no header, valid origin", func(t *testing.T) {
		before := d.apiHits
		req := httptest.NewRequest(http.MethodGet, origin+"/api/state", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		con.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (no credential at all must still be refused)", rec.Code)
		}
		if d.apiHits != before {
			t.Fatalf("request reached the brain (hits %d -> %d); missing credential must be refused before proxying", before, d.apiHits)
		}
	})
}

// TestSessionCookieNeverReachesUpstream confirms the ordering the fix must
// not disturb: proxy.Director strips the Cookie header AFTER csrfGate has
// already validated it, so the daemon itself never sees the console's
// session cookie (or any browser cookie) even on a request the gate admits.
func TestSessionCookieNeverReachesUpstream(t *testing.T) {
	d := newRealBundleDaemon(t)
	srv := d.server(t)
	con := mustNewOptions(t, srv.URL, "tok123", false, false)
	_, cookie := fetchIndexCookie(t, con)

	const origin = "http://127.0.0.1:8080"
	req := httptest.NewRequest(http.MethodGet, origin+"/api/state", nil)
	req.Header.Set("Origin", origin)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state: status %d, want 200", rec.Code)
	}
	if d.lastAuth != "Bearer tok123" {
		t.Fatalf("upstream Authorization = %q, want the injected bearer — proxying must still work", d.lastAuth)
	}
	// The fake upstream doesn't echo the Cookie header back, but proxy.go's
	// Director unconditionally calls r.Header.Del("Cookie") — this test's
	// value is documentary/regression: if that Del is ever moved BEFORE
	// csrfGate runs, TestCookieOnlyRequestPassesGate above starts failing
	// (the gate would see no cookie either), catching the ordering bug at
	// this test rather than only in production.
}
