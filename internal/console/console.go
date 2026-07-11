// SPDX-License-Identifier: Elastic-2.0

// Package console is the thin client half of the corralai daemon/client UI
// architecture: it fetches, signature-verifies and caches the daemon's
// versioned console bundle (internal/ui's BundleManifest), serves that
// bundle locally, and forwards only /api, /events and /mcp to the daemon
// with a server-side-injected bearer credential. The browser never sees the
// bearer — it only ever talks to this local console.
//
// The bundle is the trust anchor: fetchBundle refuses to cache or serve
// anything whose detached signature doesn't verify against the PINNED
// corralai release public key (ui.ConsoleReleasePubKeyHex), so a
// compromised or spoofed daemon cannot get arbitrary HTML/JS to run as this
// console. Every proxied /api|/events|/mcp request additionally passes a
// same-origin check plus a per-session secret (minted at construction, sent
// to the browser as an HttpOnly SameSite=Strict cookie set on the served
// entry document, and also accepted as a header for programmatic clients)
// before the bearer is ever attached — defense against a drive-by
// third-party page riding the console's injected credential (OWASP ASVS
// V13/V50 confused-deputy/CSRF controls).
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
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/ui"
)

// HealthPath is the console's own health endpoint: a real end-to-end check that
// the brain is reachable through the credentialed path the UI traffic takes.
const HealthPath = "/_console/health"

// consoleSessionHeader is the header the served bundle's own script must
// attach to /api|/events|/mcp requests, carrying the per-session secret
// minted at construction. It is NOT the bearer — it never authorizes
// anything at the daemon; it only proves the request came from a page this
// console itself served (same-origin + knows the secret), not a drive-by
// third party riding the console's server-side-injected bearer.
const consoleSessionHeader = "X-Corral-Console-Session"

// consoleSessionCookie carries the per-session secret to the browser so that
// transports which cannot set request headers (EventSource for /events, the
// WebSocket terminal) still prove same-origin provenance. SameSite=Strict +
// HttpOnly + the Origin/loopback-host gates make an ambient cookie safe here:
// a cross-site request neither carries the cookie nor passes the Origin check.
const consoleSessionCookie = "corral_console_session"

// New builds the console: a local bundle-host for the daemon's
// signature-verified UI bundle, plus a CSRF-guarded reverse proxy for
// /api, /events and /mcp. Same signature as before the daemon/client split;
// requires a valid bundle signature. See NewWithOptions for the dev-only
// --allow-unsigned-console escape hatch.
func New(brainRaw, token string, readOnly bool) (http.Handler, error) {
	return NewWithOptions(brainRaw, token, readOnly, false)
}

// NewWithOptions is New with the unsigned-bundle escape hatch exposed:
// allowUnsigned, when true, tolerates a daemon that serves no
// manifest.sig (dev only — wired by callers as --allow-unsigned-console).
// A signature that IS present but fails to verify is ALWAYS refused
// regardless of allowUnsigned — see fetchBundle.
func NewWithOptions(brainRaw, token string, readOnly, allowUnsigned bool) (http.Handler, error) {
	target, err := url.Parse(brainRaw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid brain URL %q (want e.g. https://brain.example)", brainRaw)
	}

	// Fail fast: the client needs the daemon reachable anyway, and this is
	// also where a poisoned/forged/unsigned bundle gets refused.
	dir, manifest, err := fetchBundle(brainRaw, token, defaultCacheRoot(), allowUnsigned)
	if err != nil {
		return nil, fmt.Errorf("console: fetch UI bundle: %w", err)
	}

	sessionSecret, err := newSessionSecret()
	if err != nil {
		return nil, fmt.Errorf("console: mint session secret: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // flush each write immediately so /events (SSE) stays live
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)
		r.Host = target.Host                           // satisfy the brain's Host allowlist
		r.Header.Set("Authorization", "Bearer "+token) // overrides anything the browser sent; injected server-side ONLY
		r.Header.Del("Cookie")                         // browser cookies are meaningless upstream
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "console: upstream brain unreachable: "+err.Error(), http.StatusBadGateway)
	}

	// The proxied surface: CSRF-guarded (checked BEFORE the bearer is ever
	// attached, i.e. before proxy.ServeHTTP runs) and, in read-only mode,
	// write-refusing.
	var apiHandler http.Handler = proxy
	if readOnly {
		apiHandler = readOnlyGate(apiHandler)
	}
	apiHandler = csrfGate(sessionSecret, apiHandler)

	mux := http.NewServeMux()
	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		if err := probeUpstream(target, token); err != nil {
			http.Error(w, "unhealthy: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/api/", apiHandler)
	mux.Handle("/events", apiHandler)
	mux.Handle("/mcp", apiHandler)
	mux.Handle("/mcp/", apiHandler)
	// Everything else: the locally cached, signature-verified bundle. The
	// daemon never serves "/" to this browser.
	mux.Handle("/", bundleHandler(dir, manifest, sessionSecret))
	// hostGate wraps the ENTIRE mux — including bundleHandler, which has no
	// CSRF gate of its own and injects the per-session secret into the
	// served entry document. Without this, a DNS-rebound Host (attacker
	// domain rebound to 127.0.0.1) would still be "same-origin" from the
	// browser's perspective and could read that secret via GET /, then ride
	// both csrfGate checks on the strength of it. See hostGate/loopbackHost.
	return hostGate(mux), nil
}

// hostGate refuses any request whose Host header is not a literal loopback
// name/IP with 403, before next runs. The console is a LOCAL thin client —
// reached only via 127.0.0.1/localhost on the operator's own machine — so
// this is the DNS-rebinding defense: a browser that's been rebound to
// evil.com -> 127.0.0.1 still sends Host: evil.com, and that's refused here
// before bundleHandler or the proxy ever sees the request.
func hostGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r) {
			http.Error(w, "console: request Host is not loopback — refused (DNS-rebinding guard)", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether r.Host's hostname (port and IPv6 brackets
// stripped) is "localhost" (case-insensitive) or a loopback IP
// (127.0.0.0/8, ::1).
func loopbackHost(r *http.Request) bool {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// csrfGate enforces the console's confused-deputy defenses on every
// proxied /api|/events|/mcp request: the Origin (or, absent that, Referer)
// must match the console's own local origin, AND the request must carry
// the per-session secret minted at construction. A drive-by third-party
// page that gets a victim's browser to hit http://127.0.0.1:PORT/api/...
// can neither guess the secret nor forge a same-origin Origin header, so it
// can't ride the server-side-injected bearer. Both checks happen before
// next (the proxy) ever runs — i.e. before the bearer is attached.
func csrfGate(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "console: request Origin/Referer does not match this console — refused", http.StatusForbidden)
			return
		}
		if !validSession(r, secret) {
			http.Error(w, "console: missing or invalid session credential — refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether the request's Origin (or Referer, if Origin is
// absent) host AND scheme match the console's own — i.e. this request
// originated from a page the console itself served, not a third-party
// site. Fails closed: no Origin and no Referer is refused, not admitted.
// The scheme check matters even with matching hosts: without it, an
// https:// Origin would be accepted for a plain http request, which is
// never how this local console is actually reached.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	reqScheme := "http"
	if r.TLS != nil {
		reqScheme = "https"
	}
	return u.Host == r.Host && u.Scheme == reqScheme
}

// validSession accepts EITHER the session header OR the session cookie —
// the header for programmatic/legacy clients, the cookie for the real SPA's
// transports (bare fetch(), EventSource, WebSocket) that can never set a
// custom header. Either path is sufficient; both are checked with the same
// constant-time discipline via constEq.
func validSession(r *http.Request, secret string) bool {
	if constEq(r.Header.Get(consoleSessionHeader), secret) {
		return true
	}
	if c, err := r.Cookie(consoleSessionCookie); err == nil && constEq(c.Value, secret) {
		return true
	}
	return false
}

// constEq reports whether got equals secret, in constant time once lengths
// match. The length check runs first (cheap, not secret-dependent) so
// subtle.ConstantTimeCompare only ever runs on equal-length inputs.
func constEq(got, secret string) bool {
	if got == "" || len(got) != len(secret) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// newSessionSecret mints a fresh, unguessable per-construction secret: 32
// bytes of crypto/rand, hex-encoded. It is NOT the bearer and is safe to
// hand to the browser.
func newSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// bundleHandler serves the locally cached, already signature-verified UI
// bundle out of dir. Every response re-verifies the file's sha256 against
// the (signature-verified) manifest at SERVE time — defense against
// tamper-after-fetch/TOCTOU of the on-disk cache. The entry document
// additionally gets the per-session secret set as an HttpOnly cookie (see
// consoleSessionCookie) so the SPA's transports that cannot set a custom
// header (EventSource, WebSocket) still carry it automatically — the bearer
// itself is never rendered or sent to the browser.
func bundleHandler(dir string, m ui.BundleManifest, sessionSecret string) http.Handler {
	assets := make(map[string]string, len(m.Assets)) // path -> sha256
	for _, a := range m.Assets {
		assets[a.Path] = a.SHA256
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = m.Entry
		}
		clean := path.Clean(p)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			http.NotFound(w, r)
			return
		}
		wantSHA, ok := assets[clean]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(clean))) // #nosec G304 -- clean was validated above to reject "..", absolute paths, and any path.Clean escape, and must additionally match a path key in the signature-verified manifest (the assets map) before this line runs; dir is this process's own cache directory, not attacker-controlled
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if sha256Hex(data) != wantSHA {
			// Cache tampered on disk after fetchBundle verified it — refuse
			// to serve rather than trust a file that no longer matches the
			// signed manifest.
			http.Error(w, "console: cached asset failed integrity re-check", http.StatusInternalServerError)
			return
		}
		if clean == m.Entry {
			// Set BEFORE writing the body: the cookie must be present on the
			// SAME response that serves the entry document, since a browser
			// fetching "/" and then immediately opening an EventSource or
			// making a fetch() call has no other opportunity to acquire it.
			// No Secure attribute: this console is reached over plain
			// http://127.0.0.1, and Secure would make the browser silently
			// drop the cookie — re-breaking the exact bug this fixes.
			http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is deliberately omitted: this console is reached ONLY over plain http://127.0.0.1/localhost (hostGate enforces loopback above), never TLS; a Secure cookie would be silently dropped by the browser and reintroduce the exact bug this fix closes. HttpOnly + SameSite=Strict are set; the confused-deputy/cross-origin threat is covered by sameOrigin + hostGate, not by Secure (which defends against network eavesdropping, not applicable on loopback)
				Name:     consoleSessionCookie,
				Value:    sessionSecret,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
		}
		ct := mime.TypeByExtension(path.Ext(clean))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data) // #nosec G705 -- data is a file just re-verified (sha256) against the signature-verified manifest above; nosniff+explicit Content-Type close the sniff-based XSS class this rule flags
	})
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
