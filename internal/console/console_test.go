// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeBrain records what the console forwards and serves the brain's routes.
type fakeBrain struct {
	lastAuth string
	lastHost string
	hits     atomic.Int32 // proxied (non-health) requests that reached upstream
	healthy  bool
}

func (b *fakeBrain) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !b.healthy {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		b.lastAuth = r.Header.Get("Authorization")
		b.lastHost = r.Host
		w.Write([]byte("swarm"))
	})
	return mux
}

func mustNew(t *testing.T, brainURL, token string, readOnly bool) http.Handler {
	t.Helper()
	h, err := New(brainURL, token, readOnly)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestInjectsBearerAndHost(t *testing.T) {
	brain := &fakeBrain{healthy: true}
	up := httptest.NewServer(brain.handler())
	defer up.Close()
	con := mustNew(t, up.URL, "tok123", true)

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.Header.Set("Authorization", "Bearer browser-supplied-evil") // must be overridden
	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state: status %d, want 200", rec.Code)
	}
	if brain.lastAuth != "Bearer tok123" {
		t.Fatalf("upstream Authorization = %q, want injected %q", brain.lastAuth, "Bearer tok123")
	}
	if want := mustHost(t, up.URL); brain.lastHost != want {
		t.Fatalf("upstream Host = %q, want brain host %q", brain.lastHost, want)
	}
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	brain := &fakeBrain{healthy: true}
	up := httptest.NewServer(brain.handler())
	defer up.Close()
	con := mustNew(t, up.URL, "tok123", true)

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		con.ServeHTTP(rec, httptest.NewRequest(m, "/api/instruct", strings.NewReader("{}")))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403 (read-only)", m, rec.Code)
		}
	}
	if n := brain.hits.Load(); n != 0 {
		t.Fatalf("a write reached the brain (%d hits); read-only must refuse before proxying", n)
	}
}

func TestReadWriteForwardsWrites(t *testing.T) {
	brain := &fakeBrain{healthy: true}
	up := httptest.NewServer(brain.handler())
	defer up.Close()
	con := mustNew(t, up.URL, "tok123", false) // read-write (admin console)

	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/instruct", strings.NewReader("{}")))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/instruct in read-write mode: status %d, want 200 (forwarded)", rec.Code)
	}
	if n := brain.hits.Load(); n != 1 {
		t.Fatalf("write did not reach the brain (%d hits); read-write must forward", n)
	}
}

func TestHealthReflectsUpstream(t *testing.T) {
	brain := &fakeBrain{healthy: true}
	up := httptest.NewServer(brain.handler())
	defer up.Close()
	con := mustNew(t, up.URL, "tok123", true)

	rec := httptest.NewRecorder()
	con.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health with brain up: status %d, want 200", rec.Code)
	}

	brain.healthy = false
	rec = httptest.NewRecorder()
	con.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health with brain down: status %d, want 503", rec.Code)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"", "not-a-url", "brain.example" /* no scheme */} {
		if _, err := New(bad, "tok", true); err == nil {
			t.Fatalf("New(%q) = nil error, want rejection", bad)
		}
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

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
