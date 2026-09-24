// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// uiWriter is what `corral ui --write` adds to the read-only page: the
// launch token, the Host values the server answers to, and the one way it
// writes — by running corral's own subcommands.
//
// WHY A SUBPROCESS AND NOT A FUNCTION CALL. The defect this repository's
// review rounds keep finding is a rule held at one door and not at another.
// A browser that adjudicated through its own code path would be a new door
// with its own copy of every rule; running `corral review adjudicate` keeps
// one implementation, reached from two places.
//
// THE HONEST LIMIT. The token narrows "who can write" from any local
// process to whoever launched this server. An agent that launches
// `corral ui --write` itself, or reads the launching terminal, can write —
// no weaker than today, when any local agent can run `corral review
// adjudicate`, and no proof of a human either. Opening the browser widens
// that further: the token-bearing URL is passed to the opener (and possibly
// a cold-started browser) as a command-line argument, so any other local
// process can read it from /proc/<pid>/cmdline or `ps` while that process
// runs. `--no-open` avoids that; printing the URL to a terminal does not
// have the same exposure. See docs/design/corral-ui-review-loop.md.
type uiWriter struct {
	token     string
	hosts     map[string]bool // lower-cased Host header values this server answers to
	repo      string          // the checkout rechecks run against (absolute)
	ledgerDir string
	cli       uiCLI
	mu        sync.Mutex // one writer at a time: the chain has one head
}

// uiCLI runs one corral subcommand and reports what it printed and how it
// exited. A non-zero exit is a RESULT (code), not an error; err is only for
// "could not start it at all".
type uiCLI func(ctx context.Context, args ...string) (stdout, stderr string, code int, err error)

// corralSubprocess runs this same binary with args. No shell is involved:
// every element of args reaches the child as exactly one argv entry.
func corralSubprocess(ctx context.Context, args ...string) (string, string, int, error) {
	self, err := os.Executable()
	if err != nil {
		return "", "", -1, err
	}
	// #nosec G204 -- this binary itself; argv is built by the UI from validated refs and a verdict, never passed to a shell
	cmd := exec.CommandContext(ctx, self, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return out.String(), errb.String(), -1, err
	}
	return out.String(), errb.String(), 0, nil
}

// newUIToken is 256 bits from crypto/rand, hex-encoded. It lives in memory
// only: never written to disk, gone when the process exits.
func newUIToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating the launch token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// uiAllowedHosts is the set of Host header values the server answers to in
// write mode: the address it is bound to and localhost on the same port.
// Anything else is a DNS-rebinding attempt or a mistake, and is refused.
func uiAllowedHosts(addr string) (map[string]bool, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("--addr %q: %w", addr, err)
	}
	if port == "" || port == "0" {
		return nil, fmt.Errorf("--addr %q: --write needs an explicit port", addr)
	}
	return map[string]bool{
		strings.ToLower(net.JoinHostPort(host, port)): true,
		"localhost:" + port:                           true,
	}, nil
}

func (u *uiWriter) hostOK(r *http.Request) bool {
	return u.hosts[strings.ToLower(r.Host)]
}

// authorize is the gate every write passes. 0 means allowed.
func (u *uiWriter) authorize(r *http.Request) (int, string) {
	if !u.hostOK(r) {
		return http.StatusMisdirectedRequest, "this server does not answer to that Host"
	}
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed, "writes are POST"
	}
	if o := r.Header.Get("Origin"); o == "" || !strings.EqualFold(o, "http://"+r.Host) {
		return http.StatusForbidden, "cross-origin write refused"
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(u.token)) != 1 {
		return http.StatusUnauthorized, "no valid launch token — open the URL `corral ui --write` printed"
	}
	return 0, ""
}

// guard wraps a write handler with authorize.
func (u *uiWriter) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if code, msg := u.authorize(r); code != 0 {
			writeJSON(w, code, map[string]string{"error": msg})
			return
		}
		next(w, r)
	}
}

// uiOpenBrowser opens url in the operator's browser. A variable so tests
// never launch one.
var uiOpenBrowser = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	// #nosec G204 -- fixed opener; url is the loopback URL this process built
	return exec.Command(name, url).Start()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
