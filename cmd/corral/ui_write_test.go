// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
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

// --repo names the checkout rechecks run against; runUI resolves it to an
// absolute path and prints it, so an operator can see what a verdict written
// through this server will actually run against.
func TestUIWritePrintsTheResolvedRepo(t *testing.T) {
	//surface: --repo
	var out, errb bytes.Buffer
	dir := t.TempDir()
	code := runUI([]string{"--write", "--addr", "127.0.0.1:8787", "--repo", dir, "--print-url"}, func(string) (sealReader, error) { return fakeSeal{}, nil }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), abs) {
		t.Errorf("stdout should name the resolved repo %q: %s", abs, out.String())
	}
	m := regexp.MustCompile(`http://127\.0\.0\.1:8787/#t=([0-9a-f]{64})`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no token URL in stdout: %s", out.String())
	}
	if strings.Count(out.String(), m[1]) != 1 || strings.Contains(errb.String(), m[1]) {
		t.Errorf("the token must still appear exactly once, in the URL")
	}
}

func testWriter(t *testing.T, addr string) *uiWriter {
	t.Helper()
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

// inProcessCLI runs `corral review …` in-process, so tests exercise the
// real CLI without a built binary.
func inProcessCLI(t *testing.T) uiCLI {
	return func(ctx context.Context, args ...string) (string, string, int, error) {
		if len(args) == 0 || args[0] != "review" {
			t.Fatalf("the UI ran an unexpected subcommand: %v", args)
		}
		var o, e bytes.Buffer
		code := runReview(args[1:], &o, &e)
		return o.String(), e.String(), code, nil
	}
}

func writeReq(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8787"
	r.Header.Set("Origin", "http://127.0.0.1:8787")
	r.Header.Set("Authorization", "Bearer tok")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func writerOver(t *testing.T, repo, ledger string) (*uiWriter, http.Handler) {
	w := testWriter(t, "127.0.0.1:8787")
	w.repo, w.ledgerDir, w.cli = repo, ledger, inProcessCLI(t)
	return w, uiHandlerWith(fakeSeal{}, ledger, w)
}

func TestUIAdjudicateWritesTheSameEntryTheCLIWould(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "test -f marker.txt"})
	_, h := writerOver(t, repo, ledger)
	reason := "real: $(rm -rf ~) stays text\nsecond line"
	body, _ := json.Marshal(map[string]string{"ref": hash + "#R1", "verdict": "confirm", "reason": reason})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, writeReq("/api/adjudicate", string(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	entries, _ := auditpush.ReadLedgerDir(ledger)
	last := entries[len(entries)-1]
	if last.Kind != auditpush.KindAdjudication || last.Adjudication == nil || last.Adjudication.Reason != reason || last.Adjudication.Verdict != auditpush.VerdictConfirmed {
		t.Fatalf("last entry %+v: want a confirmed adjudication carrying the reason verbatim", last)
	}
	if checks, err := auditpush.VerifyLedgerDir(ledger, nil); err != nil {
		t.Fatal(err)
	} else {
		for _, c := range checks {
			if c.Problem != "" {
				t.Fatalf("chain problem after a UI adjudication: %s", c.Problem)
			}
		}
	}
}

func TestUIRejectsARefThatCouldBeParsedAsAFlag(t *testing.T) {
	repo, ledger, _ := recheckFixture(t, map[string]string{"R1": "true"})
	_, h := writerOver(t, repo, ledger)
	for _, ref := range []string{"--push=md:x#R1", "abc#R1 --by x", "zz12zz12zz12#R1", "", "0123456789ab#1"} {
		for _, path := range []string{"/api/adjudicate", "/api/recheck"} {
			body, _ := json.Marshal(map[string]string{"ref": ref, "verdict": "confirm", "reason": "x"})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, writeReq(path, string(body)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s ref %q: status %d, want 400", path, ref, rec.Code)
			}
		}
	}
}

func TestUIAdjudicateRequiresAVerdictAndAReason(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "true"})
	_, h := writerOver(t, repo, ledger)
	for _, b := range []map[string]string{
		{"ref": hash + "#R1", "verdict": "maybe", "reason": "x"},
		{"ref": hash + "#R1", "verdict": "confirm", "reason": "   "},
	} {
		body, _ := json.Marshal(b)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, writeReq("/api/adjudicate", string(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", b, rec.Code)
		}
	}
}

func TestUIShowsACLIRefusalVerbatimAndChangesNothing(t *testing.T) {
	repo, ledger, _ := recheckFixture(t, map[string]string{"R1": "true"})
	_, h := writerOver(t, repo, ledger)
	before := dirDigest(t, ledger)
	body, _ := json.Marshal(map[string]string{"ref": "0123456789ab#R1", "verdict": "refute", "reason": "x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, writeReq("/api/adjudicate", string(body)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "corral review adjudicate") {
		t.Fatalf("status %d body %s: want 422 carrying the CLI's own message", rec.Code, rec.Body.String())
	}
	if dirDigest(t, ledger) != before {
		t.Fatal("a refused adjudication changed the ledger")
	}
}

func TestUISerializesConcurrentVerdicts(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "true", "R2": "true"})
	_, h := writerOver(t, repo, ledger)
	var wg sync.WaitGroup
	for _, id := range []string{"R1", "R2", "R1"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{"ref": hash + "#" + id, "verdict": "confirm", "reason": "r"})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, writeReq("/api/adjudicate", string(body)))
			if rec.Code != http.StatusOK {
				t.Errorf("%s: %d %s", id, rec.Code, rec.Body.String())
			}
		}(id)
	}
	wg.Wait()
	checks, err := auditpush.VerifyLedgerDir(ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 4 {
		t.Fatalf("%d entries, want 4 (review + three verdicts)", len(checks))
	}
	for _, c := range checks {
		if c.Problem != "" {
			t.Fatalf("chain forked under concurrent writes: %s", c.Problem)
		}
	}
}

func TestUIRecheckReturnsTheCLIsMeasurement(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "test -f marker.txt", "R2": "test -f gone.txt"})
	_, h := writerOver(t, repo, ledger)
	for id, want := range map[string]string{"R1": recheckStill, "R2": recheckGone} {
		body, _ := json.Marshal(map[string]string{"ref": hash + "#" + id})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, writeReq("/api/recheck", string(body)))
		var res recheckResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Outcome != want {
			t.Fatalf("%s: status %d body %s, want outcome %s", id, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestUICitedListsCommitsNamingTheReviewAndLabelsNothingFixed(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "true"})
	if out, err := exec.Command("git", "-C", repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "fix: the thing from review "+hash).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	_, h := writerOver(t, repo, ledger)
	r := httptest.NewRequest(http.MethodGet, "/api/cited?hash="+hash, nil)
	r.Host = "127.0.0.1:8787"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var cites []map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &cites) != nil || len(cites) != 1 {
		t.Fatalf("status %d body %s: want one citation", rec.Code, rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "\"fixed\"") {
		t.Error("the citation endpoint must not assert a fix")
	}
}
