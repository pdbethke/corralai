// SPDX-License-Identifier: Elastic-2.0

// Package console serves the credentialed window onto a corralai brain: a thin
// reverse proxy that injects a bearer token and forwards to the brain's live
// swarm UI (/, /api/*, /events). The browser talks only to the console, so the
// token never reaches it.
//
// The same proxy backs two client apps:
//
//   - read-only (the observer): every non-GET method is refused locally, so a
//     viewer can watch but never act — the guarantee holds even if the token is
//     not actually scoped read-only.
//   - read-write (the admin console): writes such as POST /api/instruct flow
//     through, so the UI's action controls work.
package console

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// HealthPath is the console's own health endpoint: a real end-to-end check that
// the brain is reachable through the credentialed path the UI traffic takes.
const HealthPath = "/_console/health"

// New builds the reverse proxy to the brain at brainRaw, injecting token as a
// bearer credential on every forwarded request. When readOnly is true, non-GET
// methods are refused before they ever reach the brain.
func New(brainRaw, token string, readOnly bool) (http.Handler, error) {
	target, err := url.Parse(brainRaw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid brain URL %q (want e.g. https://brain.example)", brainRaw)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // flush each write immediately so /events (SSE) stays live
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)
		r.Host = target.Host                           // satisfy the brain's Host allowlist
		r.Header.Set("Authorization", "Bearer "+token) // overrides anything the browser sent
		r.Header.Del("Cookie")                         // browser cookies are meaningless upstream
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "console: upstream brain unreachable: "+err.Error(), http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		if err := probeUpstream(target, token); err != nil {
			http.Error(w, "unhealthy: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	if readOnly {
		mux.Handle("/", readOnlyGate(proxy))
	} else {
		mux.Handle("/", proxy)
	}
	return mux, nil
}

// readOnlyGate admits only safe (read) methods. Writes are refused before they
// reach the brain, so the console is read-only regardless of the token's scope.
func readOnlyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "this console is read-only: "+r.Method+" not allowed", http.StatusForbidden)
		}
	})
}

// probeUpstream hits the brain's /healthz with the bearer + the expected Host so
// the check exercises the same path the proxied UI traffic takes.
func probeUpstream(target *url.URL, token string) error {
	u := *target
	u.Path = strings.TrimRight(target.Path, "/") + "/healthz"
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Host = target.Host
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("brain /healthz returned %d", resp.StatusCode)
	}
	return nil
}

// Ping is the container HEALTHCHECK entrypoint: GET the console's own health
// endpoint and translate the result into a process exit code (0 healthy, 1 not).
func Ping(addr string) int {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + LocalDialHost(addr) + HealthPath)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// LocalDialHost turns a wildcard bind (0.0.0.0:8080 / :8080) into a dialable
// loopback host for self-checks and browser-open.
func LocalDialHost(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// OpenBrowser best-effort opens url in the platform browser.
func OpenBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "cmd", []string{"/c", "start"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start() // #nosec G204 -- corral opens URLs with the platform browser launcher (open/xdg-open/cmd); command is a constant per OS, not attacker-controlled; agent command execution is separately sandboxed (bwrap)
}
