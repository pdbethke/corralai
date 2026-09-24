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
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
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

// uiRefRE is the only shape a finding ref may take before it becomes an
// argv element. The CLI parses flags anywhere in its arguments, so a ref
// like "--push=md:x#R1" would otherwise be read as a flag.
var uiRefRE = regexp.MustCompile(`^[0-9a-f]{12,64}#R[0-9]+$`)

// uiHexRE bounds the hash /api/cited greps for.
var uiHexRE = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func readBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

// recheck runs `corral review recheck --json` and returns its measurement.
// could-not-run is a 200: it is a result the page shows, not a failure.
func (u *uiWriter) recheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref string `json:"ref"`
	}
	if err := readBody(r, &in); err != nil || !uiRefRE.MatchString(in.Ref) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ref must look like <hash>#R<n>"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, errOut, code, err := u.cli(ctx, "review", "recheck", u.ledgerDir, in.Ref, "--repo", u.repo, "--json")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if code != 0 && code != 3 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": strings.TrimSpace(errOut)})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, out)
}

// adjudicate runs `corral review adjudicate`. Serialized: the chain has
// one head, and two verdicts written at once would both try to link to it.
func (u *uiWriter) adjudicate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref     string `json:"ref"`
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if err := readBody(r, &in); err != nil || !uiRefRE.MatchString(in.Ref) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ref must look like <hash>#R<n>"})
		return
	}
	flag := map[string]string{"confirm": "--confirm", "refute": "--refute"}[in.Verdict]
	if flag == "" || strings.TrimSpace(in.Reason) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a verdict (confirm or refute) and a reason are both required"})
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	out, errOut, code, err := u.cli(r.Context(), "review", "adjudicate", u.ledgerDir, in.Ref, flag, "--reason", in.Reason)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if code != 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": strings.TrimSpace(errOut)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
}

// cited lists commits in the repo whose message names a review hash. It is
// a citation, not a verification: the page labels it that way.
func (u *uiWriter) cited(w http.ResponseWriter, r *http.Request) {
	h := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hash")))
	if !uiHexRE.MatchString(h) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash must be 12-64 hex characters"})
		return
	}
	// #nosec G204,G702 -- fixed argv; repo is the operator's --repo, h is validated hex (uiHexRE), never a shell
	out, err := exec.CommandContext(r.Context(), "git", "-C", u.repo, "log", "--format=%h\t%s", "--fixed-strings", "--grep="+h[:12]).Output()
	if err != nil {
		writeJSON(w, http.StatusOK, []map[string]string{})
		return
	}
	cites := []map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if c, s, ok := strings.Cut(line, "\t"); ok {
			cites = append(cites, map[string]string{"commit": c, "subject": s})
		}
	}
	writeJSON(w, http.StatusOK, cites)
}
