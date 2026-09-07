// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// Review 8377ae6320cc (a Claude Code reviewer on cmd/corral, Codex
// verifying, 2026-09-07): seven doors where what a person or a machine was
// shown was not what the record said. The three reproductions are kept
// here inverted; the four code-read findings get the tests they lacked.

// R1 + R2 — `seal --repo --json` says what the text table says: "audited"
// is the scan's own time (never the push time under that name), "pushed"
// is the push time, and the caveat and its flags ride along.
func TestSealJSONSaysWhatTheTableSays(t *testing.T) {
	audited := time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)
	pushed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	row := sealRow{Repo: "r", Path: "a.go", ParentSHA256: "deadbeef", KillRate: 0.5, TS: pushed, AuditedAt: &audited}
	var buf bytes.Buffer
	emitSealStateJSON([]sealState{{Path: "a.go", State: "live", Row: &row}}, &buf)
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if aud, _ := got[0]["audited"].(string); aud != audited.Format(time.RFC3339) {
		t.Fatalf(`"audited" must be the scan's own time %s, got %q`, audited.Format(time.RFC3339), aud)
	}
	if p, _ := got[0]["pushed"].(string); p != pushed.Format(time.RFC3339) {
		t.Fatalf(`"pushed" must be the push time, got %q`, p)
	}
	// No recorded audit time: "audited" is null, never the push time.
	row.AuditedAt = nil
	buf.Reset()
	emitSealStateJSON([]sealState{{Path: "a.go", State: "live", Row: &row}}, &buf)
	got = nil
	_ = json.Unmarshal(buf.Bytes(), &got)
	if got[0]["audited"] != nil {
		t.Fatalf(`with no recorded audit time "audited" must be null, got %v`, got[0]["audited"])
	}
	if !strings.Contains(row.auditedLabel(), "(push)") {
		t.Fatalf("the text door must mark the push time as such: %q", row.auditedLabel())
	}

	// The caveats.
	row = sealRow{Repo: "r", Path: "a.go", KillRate: 1.0, Survivors: 5, TS: pushed,
		TestWriterFailed: true, PoolTestUnsound: true, BaselineFailed: true}
	buf.Reset()
	emitSealStateJSON([]sealState{{Path: "a.go", State: "live", Row: &row}}, &buf)
	got = nil
	_ = json.Unmarshal(buf.Bytes(), &got)
	if cv, _ := got[0]["caveat"].(string); cv != row.caveat() || cv == "" {
		t.Fatalf(`"caveat" must be the table's word %q, got %q`, row.caveat(), cv)
	}
	for _, k := range []string{"test_writer_failed", "pool_test_unsound", "baseline_failed"} {
		if v, _ := got[0][k].(bool); !v {
			t.Errorf("%s must be carried as true", k)
		}
	}
}

// retractedReviewLedger is a ledger with one review, retracted, and one
// adjudication of it (which goes with it).
func retractedReviewLedger(t *testing.T) (dir, hash, reason string) {
	t.Helper()
	dir = t.TempDir()
	r := review.Review{Repo: "r", Commit: "0123456789abcdef0123456789abcdef01234567", Scope: "cmd/corral", ReviewerModel: "some-model",
		Findings: []review.Finding{{ID: "R1", Claim: "a claim that was retracted", Declared: "CODE-READ", Tier: "CODE-READ", File: "cmd/corral/seal.go"}}}
	if _, err := auditpush.WriteReview(dir, r, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := auditpush.ReadLedgerDir(dir)
	hash = entries[0].Hash
	if _, err := auditpush.WriteAdjudication(dir, hash+"#R1", auditpush.VerdictConfirmed, "p", "real", nil); err != nil {
		t.Fatal(err)
	}
	reason = "the reviewer hallucinated the whole finding"
	if _, err := auditpush.WriteRetraction(dir, hash, reason, nil); err != nil {
		t.Fatal(err)
	}
	return dir, hash, reason
}

// R3 — `corral ui` shows the chain, marks what does not stand (of every
// kind), and lists only standing reviews.
func TestUIHonoursARetractedReview(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "no-key"))
	dir, hash, reason := retractedReviewLedger(t)
	u, err := readUILedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Reviews) != 0 {
		t.Fatalf("a retracted review rendered as live: %+v", u.Reviews)
	}
	marked, adjMarked := false, false
	for _, e := range u.Entries {
		if e.Hash == hash && strings.Contains(e.Note, "RETRACTED: "+reason) {
			marked = true
		}
		if e.Kind == auditpush.KindAdjudication && strings.Contains(e.Note, "RETRACTED") {
			adjMarked = true
		}
	}
	if !marked || !adjMarked {
		t.Fatalf("the chain must mark the retracted review and its adjudication: %+v", u.Entries)
	}
	if len(u.Entries) != 3 {
		t.Fatalf("the chain view still lists every entry: %d", len(u.Entries))
	}
}

// R5 — `review show` announces a withdrawn review before printing it;
// `review plan` does not count it as coverage.
func TestShowAndPlanHonourARetractedReview(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "no-key"))
	dir, hash, reason := retractedReviewLedger(t)
	var out, errb bytes.Buffer
	if code := runReview([]string{"show", dir, hash}, &out, &errb); code != 0 {
		t.Fatalf("show: %d %s", code, errb.String())
	}
	s := out.String()
	if !strings.HasPrefix(s, "RETRACTED ") || !strings.Contains(s, reason) || !strings.Contains(s, "does not stand") {
		t.Fatalf("show must announce the retraction first:\n%s", s)
	}
	if !strings.Contains(s, "a claim that was retracted") {
		t.Fatalf("show must still print what was withdrawn:\n%s", s)
	}
	// The adjudication went with the review: not shown as standing.
	if strings.Contains(s, "confirmed by p") {
		t.Fatalf("an adjudication of a retracted review does not stand:\n%s", s)
	}

	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "cmd", "corral", "seal.go"), "package main\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	out.Reset()
	errb.Reset()
	if code := runReview([]string{"plan", "--repo", root, "--ledger", dir, "--limit", "10"}, &out, &errb); code != 0 {
		t.Fatalf("plan: %d %s", code, errb.String())
	}
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "cmd/corral ") && !strings.Contains(l, "never reviewed") {
			t.Fatalf("a retracted review counted as coverage:\n%s", l)
		}
	}
	if !strings.Contains(out.String(), "0 review(s)") {
		t.Fatalf("plan must count no standing review:\n%s", out.String())
	}
}

// R6 — "signed into" is said only for an envelope this run wrote; a stale
// envelope beside the statement is removed, not announced.
func TestAttestSaysSignedOnlyForItsOwnEnvelope(t *testing.T) {
	// No key anywhere: HOME is empty and both variables are unset.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", "")
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	p := filepath.Join(t.TempDir(), "review.json")
	stale := dsseEnvelopePathFor(p)
	if err := os.WriteFile(stale, []byte("{\"leftover\":true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := review.Review{Repo: "r", Commit: "c", Scope: "s", ReviewerModel: "m"}
	sha, envPath, signErr, err := writeReviewStatement(p, r)
	if err != nil || sha == "" {
		t.Fatalf("plain statement: %v", err)
	}
	if signErr == nil || envPath != "" {
		t.Fatalf("with no key there is no envelope of ours: envPath=%q signErr=%v", envPath, signErr)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale envelope must not survive the statement it does not belong to")
	}
	if !strings.Contains(signErr.Error(), "no local signing key") {
		t.Fatalf("the reason must be said by name: %v", signErr)
	}
	// With a key: the envelope written is this run's, and it replaces a
	// stale one.
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	if err := os.WriteFile(stale, []byte("{\"leftover\":true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, envPath, signErr, err = writeReviewStatement(p, r)
	if err != nil || signErr != nil || envPath != stale {
		t.Fatalf("with a key the envelope is written at %s: envPath=%q signErr=%v err=%v", stale, envPath, signErr, err)
	}
	if b, _ := os.ReadFile(stale); strings.Contains(string(b), "leftover") {
		t.Fatalf("the stale envelope was announced as this run's")
	}
}

// R7 — `scans show -h` reaches usage.
func TestScansShowHelpReachesUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := runScansShow([]string{"-h"}, func(string) (scansReader, error) { t.Fatal("must not open a store"); return nil, nil }, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "usage: corral scans show") || !strings.Contains(errb.String(), "-evidence") {
		t.Fatalf("-h must print usage and the flags: exit %d\n%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "is not a scan id") {
		t.Fatalf("-h was read as a scan id:\n%s", errb.String())
	}
	// Flags after the id still parse.
	errb.Reset()
	code = runScansShow([]string{"7", "--json"}, func(string) (scansReader, error) { return nil, os.ErrNotExist }, &out, &errb)
	if code != 1 || strings.Contains(errb.String(), "is not a scan id") {
		t.Fatalf("id then flags: exit %d\n%s", code, errb.String())
	}
}

// R4 — the generated CLI reference never documents a verb with the error
// it produces as the body: a section whose body is an unknown-subcommand
// error is a verb the dispatcher does not have.
func TestDocsCLIReferenceDocumentsNoVerbThatDoesNotExist(t *testing.T) {
	for _, p := range []string{"../../docs/cli/corral.md", "../../site/src/content/docs/docs/cli/corral.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"unknown subcommand", "is not a scan id"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s documents a verb by its error (%q) — it does not exist", p, bad)
			}
		}
	}
}

// --fail-on reproduced is the merge gate's switch: exit 3 when a finding
// that reproduced stands after the run, the entry written first either
// way; a review whose reproductions all fell exits 0.
func TestReviewFailOnReproducedIsAGateAfterTheRecord(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	reply := `{"opinion":"o","findings":[{"claim":"Add subtracts","tier":"REPRODUCED","file":"pkg/a.go","line":3,"severity":"high","script":"grep -q 'a - b' pkg/a.go"}],"sound":["go.mod","a","b"]}`
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) { return cannedReviewer{reply: reply}, nil }
	ledger := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "m", "--ledger", ledger, "--fail-on", "reproduced"}, &out, &errb)
	if code != reviewExitStanding || !strings.Contains(out.String(), "gate: a REPRODUCED finding stands — exit 3") {
		t.Fatalf("a standing reproduced finding must exit %d: got %d\n%s%s", reviewExitStanding, code, out.String(), errb.String())
	}
	if entries, _ := auditpush.ReadLedgerDir(ledger); len(entries) != 1 {
		t.Fatalf("the entry is written before the gate fires: %d entries", len(entries))
	}
	// Without the switch, the same review exits 0 — a review is not a gate
	// unless asked to be.
	out.Reset()
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "m", "--no-ledger"}, &out, &errb); code != 0 {
		t.Fatalf("off by default: %d", code)
	}
	// A reproduction that does not hold does not fire the gate.
	reply = `{"opinion":"o","findings":[{"claim":"go.mod is missing","tier":"REPRODUCED","file":"go.mod","line":1,"severity":"low","script":"test ! -f go.mod"}],"sound":["a","b","c"]}`
	out.Reset()
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "m", "--no-ledger", "--fail-on", "reproduced"}, &out, &errb); code != 0 {
		t.Fatalf("a demoted finding must not fire the gate: %d\n%s", code, out.String())
	}
	errb.Reset()
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "m", "--no-ledger", "--fail-on", "everything"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "is not a tier") {
		t.Fatalf("an unknown tier is refused by name: %d %s", code, errb.String())
	}
}
