// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The spec's promise (§5): the app and the public page are the same pages.
// Static and live both call Render, and this pins it byte for byte.
func TestLiveAndStaticServeTheSameBytes(t *testing.T) {
	s := buildOK(append(manyScans(2), reviewEntry("dddddddddddd", 0)))
	s.Generated = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	out := t.TempDir()
	if err := WriteStatic(out, s); err != nil {
		t.Fatal(err)
	}
	h := Handler(func() (Site, error) { return s, nil })
	for _, path := range s.Paths() {
		want, err := os.ReadFile(filepath.Join(out, path))
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != string(want) {
			t.Fatalf("%s: live (%d) differs from static", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() != read(t, filepath.Join(out, "index.html")) {
		t.Fatal("/ must serve index.html")
	}
}

func TestLiveModeRefusesPathsItDidNotRender(t *testing.T) {
	s := buildOK(manyScans(1))
	h := Handler(func() (Site, error) { return s, nil })
	for _, p := range []string{"/entry/../../etc/passwd", "/entry/zzzz.html", "/entry/ffffffffffff.html", "/secret.txt", "/audits-0.html", "/audits-x.html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d, want 404", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/index.html", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to a page: got %d, want 405", rec.Code)
	}
}

// A read failure is shown as one: never an empty, reassuring record.
func TestLiveModeReportsALoadFailure(t *testing.T) {
	h := Handler(func() (Site, error) { return Site{}, errors.New("disk on fire") })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Could not read the ledger: disk on fire") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
