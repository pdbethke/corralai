// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// --write on a non-loopback address is refused outright: the token gate
// is a local-operator boundary and means nothing on a network.
func TestUIWriteRefusesANonLoopbackAddress(t *testing.T) {
	var out, errb bytes.Buffer
	code := runUI([]string{"--write", "--addr", "0.0.0.0:8787", "--print-url"}, func(string) (sealReader, error) { return fakeSeal{}, nil }, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "loopback") {
		t.Errorf("stderr should say why: %s", errb.String())
	}
}

// The URL carries the token in the fragment, and the token appears nowhere
// else in what the command prints.
func TestUIWritePrintsATokenURLAndNothingElseCarriesTheToken(t *testing.T) {
	var out, errb bytes.Buffer
	code := runUI([]string{"--write", "--addr", "127.0.0.1:8787", "--print-url"}, func(string) (sealReader, error) { return fakeSeal{}, nil }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	m := regexp.MustCompile(`http://127\.0\.0\.1:8787/#t=([0-9a-f]{64})`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no token URL in stdout: %s", out.String())
	}
	if strings.Count(out.String(), m[1]) != 1 || strings.Contains(errb.String(), m[1]) {
		t.Errorf("the token must appear exactly once, in the URL")
	}
}

func testWriter(t *testing.T, addr string) *uiWriter {
	t.Helper()
	//surface: --repo
	hosts, err := uiAllowedHosts(addr)
	if err != nil {
		t.Fatal(err)
	}
	return &uiWriter{token: "tok", hosts: hosts, repo: t.TempDir()}
}

func post(target, host, origin, auth string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader("{}"))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestUIWriteGuardsEveryWrite(t *testing.T) {
	h := uiHandlerWith(fakeSeal{}, "", testWriter(t, "127.0.0.1:8787"))
	for _, tc := range []struct {
		name string
		req  *http.Request
		want int
	}{
		{"no token", post("/api/whoami", "127.0.0.1:8787", "http://127.0.0.1:8787", ""), http.StatusUnauthorized},
		{"wrong token", post("/api/whoami", "127.0.0.1:8787", "http://127.0.0.1:8787", "Bearer nope"), http.StatusUnauthorized},
		{"foreign host", post("/api/whoami", "evil.example:8787", "http://evil.example:8787", "Bearer tok"), http.StatusMisdirectedRequest},
		{"no origin", post("/api/whoami", "127.0.0.1:8787", "", "Bearer tok"), http.StatusForbidden},
		{"foreign origin", post("/api/whoami", "127.0.0.1:8787", "http://evil.example", "Bearer tok"), http.StatusForbidden},
		{"GET is not a write", httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/whoami", nil), http.StatusMethodNotAllowed},
		{"allowed", post("/api/whoami", "127.0.0.1:8787", "http://127.0.0.1:8787", "Bearer tok"), http.StatusOK},
		{"localhost alias, any case", post("/api/whoami", "LOCALHOST:8787", "http://LOCALHOST:8787", "Bearer tok"), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tc.req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "tok\"") {
				t.Errorf("a response body carried the token")
			}
		})
	}
}

func TestUIWriteAllowsIPv6Loopback(t *testing.T) {
	h := uiHandlerWith(fakeSeal{}, "", testWriter(t, "[::1]:8787"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post("/api/whoami", "[::1]:8787", "http://[::1]:8787", "Bearer tok"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// Reads keep working without a token in write mode — but only for the
// allowed hosts; read-only mode is unchanged and has no Host check at all.
func TestUIWriteModeReadsNeedNoTokenButDoNeedTheHost(t *testing.T) {
	h := uiHandlerWith(fakeSeal{}, "", testWriter(t, "127.0.0.1:8787"))
	ok := httptest.NewRequest(http.MethodGet, "/api/seal", nil)
	ok.Host = "127.0.0.1:8787"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("read in write mode: %d", rec.Code)
	}
	bad := httptest.NewRequest(http.MethodGet, "/api/seal", nil)
	bad.Host = "rebound.example:8787"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign Host read in write mode: %d, want 421", rec.Code)
	}
	rec = httptest.NewRecorder()
	uiHandler(fakeSeal{}, "").ServeHTTP(rec, bad) // read-only mode: unchanged
	if rec.Code != http.StatusOK {
		t.Fatalf("read-only mode must not add a Host check: %d", rec.Code)
	}
}

// Write routes do not exist at all in read-only mode.
func TestUIReadOnlyModeHasNoWriteRoutes(t *testing.T) {
	rec := httptest.NewRecorder()
	uiHandler(fakeSeal{}, "").ServeHTTP(rec, post("/api/whoami", "127.0.0.1:8787", "http://127.0.0.1:8787", "Bearer tok"))
	if rec.Code == http.StatusOK {
		t.Fatal("read-only mode answered a write route")
	}
}
