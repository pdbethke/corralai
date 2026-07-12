// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestForgeClient_StripsTokenOnCrossHostRedirect confirms that forgeHTTPClient
// strips the GitLab-style PRIVATE-TOKEN header (which Go's redirect handling
// does NOT strip on its own, unlike Authorization) when a redirect crosses to
// a different host. This prevents a forge redirect or open-redirect from
// exfiltrating the token to an attacker-controlled host.
func TestForgeClient_StripsTokenOnCrossHostRedirect(t *testing.T) {
	var gotToken string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(200)
	}))
	defer attacker.Close()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusFound) // cross-host redirect
	}))
	defer forge.Close()
	req, _ := http.NewRequest("GET", forge.URL, nil)
	req.Header.Set("PRIVATE-TOKEN", "secret-gitlab-token")
	_, _ = forgeHTTPClient.Do(req)
	if gotToken == "secret-gitlab-token" {
		t.Fatal("PRIVATE-TOKEN leaked across a cross-host redirect")
	}
}

// TestForgeClient_PreservesTokenOnSameHostRedirect guards against over-stripping:
// a same-host redirect (e.g. a path normalization redirect from the forge
// itself) must still carry the auth header, or every same-host redirect would
// silently become an unauthenticated request.
func TestForgeClient_PreservesTokenOnSameHostRedirect(t *testing.T) {
	var gotToken string
	mux := http.NewServeMux()
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(200)
	})
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	forge := httptest.NewServer(mux)
	defer forge.Close()

	req, _ := http.NewRequest("GET", forge.URL+"/start", nil)
	req.Header.Set("PRIVATE-TOKEN", "secret-gitlab-token")
	_, _ = forgeHTTPClient.Do(req)
	if gotToken != "secret-gitlab-token" {
		t.Fatal("PRIVATE-TOKEN was stripped on a same-host redirect (over-stripping)")
	}
}

// TestForgeClient_StopsAfterTenRedirects confirms the redirect cap kicks in
// instead of following redirects forever.
func TestForgeClient_StopsAfterTenRedirects(t *testing.T) {
	var hitCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	forge := httptest.NewServer(mux)
	defer forge.Close()

	req, _ := http.NewRequest("GET", forge.URL+"/loop", nil)
	_, err := forgeHTTPClient.Do(req)
	if err == nil {
		t.Fatal("expected an error after exceeding the redirect cap, got nil")
	}
	if hitCount > 11 {
		t.Fatalf("redirect cap did not stop the client in time: got %d hits", hitCount)
	}
}
