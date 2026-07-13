// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFindOpenPRQueryEscaping proves the open-PR lookup URL-escapes the head
// query param instead of raw string-concatenating it. A branch/ref containing
// query metacharacters (&, space) must round-trip to the forge intact — an
// unescaped "&" would split the query, dropping part of the ref and injecting a
// spurious parameter.
func TestFindOpenPRQueryEscaping(t *testing.T) {
	var gotHead, gotState string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHead = r.URL.Query().Get("head")
		gotState = r.URL.Query().Get("state")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	p := newTestGitHubProvider(srv.URL, "tok")
	head := "feature/a & b"
	if _, err := p.rc.rcFindOpenPR(context.Background(), "o", "r", head); err != nil {
		t.Fatalf("rcFindOpenPR: %v", err)
	}
	if want := "o:" + head; gotHead != want {
		t.Fatalf("head param mangled: got %q, want %q", gotHead, want)
	}
	if gotState != "open" {
		t.Fatalf("state param = %q, want %q", gotState, "open")
	}
}

// TestListOpenPRsQueryEscaping proves rcListOpenPRs URL-escapes the base
// query param instead of raw string-concatenating it (the batch-2 escape fix
// at rcFindOpenPR missed this call site). A base branch containing query
// metacharacters (&, space) must round-trip to the forge intact.
func TestListOpenPRsQueryEscaping(t *testing.T) {
	var gotBase, gotState, gotPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBase = r.URL.Query().Get("base")
		gotState = r.URL.Query().Get("state")
		gotPerPage = r.URL.Query().Get("per_page")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	p := newTestGitHubProvider(srv.URL, "tok")
	base := "release/2.0 & main"
	if _, err := p.rc.rcListOpenPRs(context.Background(), "o", "r", base); err != nil {
		t.Fatalf("rcListOpenPRs: %v", err)
	}
	if gotBase != base {
		t.Fatalf("base param mangled: got %q, want %q", gotBase, base)
	}
	if gotState != "open" {
		t.Fatalf("state param = %q, want %q", gotState, "open")
	}
	if gotPerPage != "100" {
		t.Fatalf("per_page param = %q, want %q", gotPerPage, "100")
	}
}
