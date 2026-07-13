// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/netguard"
)

// init allowlists the loopback addresses httptest.NewServer binds to (127.0.0.1
// and ::1), so this package's many httptest-backed forge tests keep dialing
// their local test servers under the SSRF dial-guard. forgeGuard is a package
// var precisely so tests can substitute this allowlist; production defaults to
// deny (see the CORRALAI_FORGE_ALLOWED_HOSTS-driven init in provider.go).
func init() {
	forgeGuard = netguard.NewGuard([]string{"127.0.0.1", "::1", "localhost"})
}

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

// TestForgeClient_BlocksLinkLocalRedirect confirms the forge client's dial
// guard refuses a redirect to a link-local/cloud-metadata address (SSRF).
// CheckRedirect only strips auth headers on a cross-host hop — it never
// refused the dial itself, so a forge (or an open redirect on the forge) could
// point the client at 169.254.169.254 and it would connect unguarded. The
// forge test server itself is on 127.0.0.1, which the package init() above
// allowlists, so only the redirect target is exercised against the guard's
// default-deny policy.
func TestForgeClient_BlocksLinkLocalRedirect(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer forge.Close()

	req, _ := http.NewRequest("GET", forge.URL, nil)
	_, err := forgeHTTPClient.Do(req)
	if err == nil {
		t.Fatal("expected forgeHTTPClient to refuse a redirect to a link-local address")
	}
	if !strings.Contains(err.Error(), "SSRF guard") {
		t.Fatalf("expected the SSRF dial-guard to have blocked the redirect, got a different error: %v", err)
	}
}
