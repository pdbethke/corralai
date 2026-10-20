// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
)

const localEntryOrigin = "https://example.test/acme/widget.git"

// localEntryFixture is a committed one-file Go module with an origin, a
// recorded mutant set its own test kills, and a metered fake critic seat — so
// a real `certify --local` over it runs the jail, makes model calls nobody
// pays for (the critic, a claude-json agent script), and finishes. The writer
// is never called: the dev suite kills every mutant, so there is no survivor
// to write a test for. Returns the repo, the set, and the critic's call log.
func localEntryFixture(t *testing.T) (root, setPath, callLog string) {
	t.Helper()
	noKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	t.Setenv("CORRALAI_STATE_DIR", t.TempDir())
	root = t.TempDir()
	a := "package pkg\n\nfunc A() int { return 1 }\n"
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/tel\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), a)
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() != 1 {\n\t\tt.Fatal(\"A\")\n\t}\n}\n")
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("remote", "add", "origin", localEntryOrigin)
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "fixture", "--no-gpg-sign")
	setPath = filepath.Join(t.TempDir(), "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {ParentSHA256: shaOf(a), Mutants: []adequacy.RecordedMutant{{ID: "m1", Replace: "package pkg\n\nfunc A() int { return 0 }\n"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	callLog = wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
	return root, setPath, callLog
}

// runLocalEntryFixture runs certify --local over the fixture and returns the
// exit code and both streams.
func runLocalEntryFixture(t *testing.T, root, setPath string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{
		"--repo-dir", root, "--code", "pkg/a.go", "--goal", "A returns 1",
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "fakec",
		"--mutants", setPath, "--quiet",
	}, extra...)
	args = append(args, "--", "go", "test", "./...")
	var out, errb bytes.Buffer
	code := runCertifyLocal(args, &out, &errb)
	return code, out.String(), errb.String()
}

// localRunFinished fails the test unless the run reached a verdict: 0 or 3
// (the one-mutant exam is too small to certify, which is needs-review).
func localRunFinished(t *testing.T, code int, out, errs string) {
	t.Helper()
	if code != 0 && code != 3 {
		t.Fatalf("the run did not reach a verdict: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
}

func scanEntriesIn(t *testing.T, dir string) []auditpush.LedgerEntry {
	t.Helper()
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return auditpush.ScanEntries(entries)
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.Count(string(raw), "\n")
}

// A --local run writes ONE complete, signed entry to the machine ledger: the
// subject git names, the calls it measured — the same calls the critic's
// script saw, token for token — and the seats' scorecard rows. It verifies.
func TestLocalRunWritesMachineLedgerEntry(t *testing.T) {
	root, setPath, callLog := localEntryFixture(t)
	ml := withMachineLedger(t)
	code, out, errs := runLocalEntryFixture(t, root, setPath)
	localRunFinished(t, code, out, errs)

	entries := scanEntriesIn(t, ml)
	if len(entries) != 1 {
		t.Fatalf("machine ledger holds %d scan entries, want 1\nstdout:\n%s\nstderr:\n%s", len(entries), out, errs)
	}
	e := entries[0]
	b := e.Bundle
	head := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	if b.Scan.Repo != localEntryOrigin || b.Scan.Commit != head || b.Scan.RepoIdentity != subjectIdentityGit {
		t.Errorf("subject = %q@%q (%q), want %q@%q (git)", b.Scan.Repo, b.Scan.Commit, b.Scan.RepoIdentity, localEntryOrigin, head)
	}
	if b.Scan.PushedBy != auditpush.PushedByCertify || b.Scan.Substrate != substrateJail {
		t.Errorf("pushed_by %q substrate %q, want %q and %q", b.Scan.PushedBy, b.Scan.Substrate, auditpush.PushedByCertify, substrateJail)
	}
	if b.Scan.TreeDirty == nil || *b.Scan.TreeDirty {
		t.Errorf("tree_dirty = %v, want a measured false for a clean checkout", b.Scan.TreeDirty)
	}
	if len(b.Files) != 1 || b.Files[0].Path != "pkg/a.go" {
		t.Fatalf("file rows = %+v, want the one audited file", b.Files)
	}
	if len(b.Mutants) == 0 {
		t.Errorf("no mutant rows: the run graded one mutant")
	}
	if len(b.Seats) == 0 {
		t.Errorf("no seat rows: the run measured its seats and the entry dropped them")
	}

	// Calls equal what was actually spent: the critic script logged every
	// call it answered, and each answer reported 7 in / 3 out.
	n := countLines(t, callLog)
	if n == 0 || len(b.Calls) == 0 {
		t.Fatalf("the critic made %d call(s) and the entry carries %d call row(s); want both non-zero", n, len(b.Calls))
	}
	var calls int
	var in, outTok int64
	for _, c := range b.Calls {
		calls += c.Calls
		in += c.InputTokens
		outTok += c.OutputTokens
	}
	if calls != n || in != int64(7*n) || outTok != int64(3*n) {
		t.Errorf("call rows sum to %d calls %d/%d tokens, want %d calls %d/%d (the script's own log)", calls, in, outTok, n, 7*n, 3*n)
	}
	if b.Scan.ModelCalls != int64(n) || b.Scan.InputTokens != int64(7*n) || b.Scan.OutputTokens != int64(3*n) {
		t.Errorf("scan header totals %d calls %d/%d, want %d %d/%d", b.Scan.ModelCalls, b.Scan.InputTokens, b.Scan.OutputTokens, n, 7*n, 3*n)
	}

	if e.Signature == "" || e.KeyID == "" {
		t.Errorf("the entry is unsigned (keyid %q) although a certify key is configured", e.KeyID)
	}
	want := "ledger: " + e.File + " in " + ml + " (signed by " + e.KeyID + ")"
	if !strings.Contains(out, want) {
		t.Errorf("stdout does not name the entry; want %q in:\n%s", want, out)
	}
	var vout, verr bytes.Buffer
	if c := runVerifyAttest([]string{"--ledger", ml}, &vout, &verr); c != 0 {
		t.Errorf("verify --ledger: exit %d\n%s%s", c, vout.String(), verr.String())
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// treeListing is every path under root outside .git, with its size and
// mode, so a file the run added, removed or rewrote shows as a difference.
func treeListing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, fmt.Sprintf("%s %s %s %d", rel, info.Mode(), info.ModTime(), info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// --local never writes into the checkout: not its entry (that goes to the
// machine ledger), not a vendor directory, not a stray file. The tree and git's
// view of it are the same after the run as before.
func TestLocalRunNeverWritesCheckout(t *testing.T) {
	root, setPath, _ := localEntryFixture(t)
	withMachineLedger(t)
	beforeTree := treeListing(t, root)
	beforeStatus := gitOut(t, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")
	code, out, errs := runLocalEntryFixture(t, root, setPath)
	localRunFinished(t, code, out, errs)
	if after := treeListing(t, root); strings.Join(after, "\n") != strings.Join(beforeTree, "\n") {
		t.Errorf("the run changed the checkout:\nbefore:\n%s\nafter:\n%s", strings.Join(beforeTree, "\n"), strings.Join(after, "\n"))
	}
	if after := gitOut(t, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignored"); after != beforeStatus {
		t.Errorf("git status changed:\nbefore:\n%s\nafter:\n%s", beforeStatus, after)
	}
}

// --no-ledger writes no machine-ledger entry and says so.
func TestLocalNoLedgerFlag(t *testing.T) {
	root, setPath, _ := localEntryFixture(t)
	ml := withMachineLedger(t)
	code, out, errs := runLocalEntryFixture(t, root, setPath, "--no-ledger")
	localRunFinished(t, code, out, errs)
	if n := len(scanEntriesIn(t, ml)); n != 0 {
		t.Errorf("--no-ledger wrote %d machine-ledger entr(ies)", n)
	}
	if !strings.Contains(out, "ledger: --no-ledger") {
		t.Errorf("--no-ledger must say no entry was written; stdout:\n%s", out)
	}
}

// --ledger <dir> writes there as well as to the machine ledger — the same
// entry in both; --ledger naming the machine ledger itself writes it once;
// and --ledger inside --repo-dir is refused before any work, because --local
// never writes into the checkout.
func TestLocalLedgerFlagWritesBoth(t *testing.T) {
	root, setPath, _ := localEntryFixture(t)
	ml := withMachineLedger(t)
	other := filepath.Join(t.TempDir(), "other-ledger")
	code, out, errs := runLocalEntryFixture(t, root, setPath, "--ledger", other)
	localRunFinished(t, code, out, errs)
	inML, inOther := scanEntriesIn(t, ml), scanEntriesIn(t, other)
	if len(inML) != 1 || len(inOther) != 1 {
		t.Fatalf("entries: machine ledger %d, --ledger %d; want 1 and 1\nstdout:\n%s\nstderr:\n%s", len(inML), len(inOther), out, errs)
	}
	if inML[0].ScanUID == "" || inML[0].Bundle.Scan.StartedAt == nil || inOther[0].Bundle.Scan.StartedAt == nil ||
		!inML[0].Bundle.Scan.StartedAt.Equal(*inOther[0].Bundle.Scan.StartedAt) || len(inML[0].Bundle.Calls) != len(inOther[0].Bundle.Calls) {
		t.Errorf("the two entries are not the same run")
	}
	if !strings.Contains(out, "in "+other+" (") || !strings.Contains(out, "in "+ml+" (") {
		t.Errorf("stdout must name both entries:\n%s", out)
	}

	// --ledger AT the machine ledger: one entry, not two.
	code, out, errs = runLocalEntryFixture(t, root, setPath, "--ledger", ml)
	localRunFinished(t, code, out, errs)
	if n := len(scanEntriesIn(t, ml)); n != 2 {
		t.Errorf("machine ledger holds %d entries after a second run naming it as --ledger, want 2 (one per run)", n)
	}

	// --ledger inside the checkout: refused, exit 2, nothing written there.
	inside := filepath.Join(root, ".corral", "ledger")
	code, out, errs = runLocalEntryFixture(t, root, setPath, "--ledger", inside)
	if code != 2 || !strings.Contains(errs, "--ledger") {
		t.Errorf("--ledger inside --repo-dir: exit %d, want 2 with a refusal\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(root, ".corral")); err == nil {
		t.Errorf("a refused --ledger still created %s", inside)
	}
}

// The entry's file row is the ledger file row of the wrapped verdict, field
// for field — the walk TestBundleIsTheLedgerRowForRow runs for --repo, over
// the same rich verdict — and the run's telemetry and model calls reach the
// entry with the file's path stamped on them.
func TestLocalEntryFieldsSurvive(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package p\n\nfunc F() int { return 1 }\n")
	v := richAuditedVerdict()
	v.Lang = "go"
	v.ModelsByRole = map[string]string{advpool.RoleTestWriter: "w", advpool.RoleMutantGenerator: "m"}
	v.ModelCalls = []advpool.ModelCall{{Role: advpool.RoleTestWriter, Model: "w", Calls: 2, InputTokens: 70, OutputTokens: 30}}
	tel := advpool.RunTelemetry{Seats: []advpool.BugCatchObservation{{Role: advpool.RoleMutantGenerator, Model: "m"}}}
	in := localAuditInput{repoDir: dir, codePath: "a.go", writerModel: "w", mutantModel: "m", criticModel: "off"}
	started := time.Now().Add(-time.Minute)

	b, err := localEntryBundle(in, v, tel, started, localRunFacts{})
	if err != nil {
		t.Fatal(err)
	}
	want := buildScanFileRows([]reposcan.FileResult{localFileResult(in, v, tel, time.Now())}, nil, reposcan.CoverageMap{}, "", dir, io.Discard)
	if len(want) != 1 || len(b.Files) != 1 {
		t.Fatalf("file rows: ledger %d, entry %d; want 1 and 1", len(want), len(b.Files))
	}
	if b.Files[0].Path != "a.go" || b.Files[0].Disposition != "audited" {
		t.Errorf("entry file row = %s/%s, want a.go audited", b.Files[0].Path, b.Files[0].Disposition)
	}
	assertFileRowMapped(t, want[0], b.Files[0])

	if len(b.Calls) != 1 || b.Calls[0].Path != "a.go" || b.Calls[0].Calls != 2 {
		t.Errorf("call rows = %+v, want the verdict's one test-writer row on a.go", b.Calls)
	}
	if b.Scan.ModelCalls != 2 || b.Scan.InputTokens != 70 || b.Scan.OutputTokens != 30 {
		t.Errorf("scan totals %d %d/%d, want 2 70/30", b.Scan.ModelCalls, b.Scan.InputTokens, b.Scan.OutputTokens)
	}
	if len(b.Seats) != 1 || b.Seats[0].Path != "a.go" {
		t.Errorf("seat rows = %+v, want one stamped with a.go", b.Seats)
	}
	if len(b.Mutants) != 3 {
		t.Errorf("mutant rows = %d, want the verdict's three", len(b.Mutants))
	}
	if b.Scan.StartedAt == nil || !b.Scan.StartedAt.Equal(started) {
		t.Errorf("started_at = %v, want %v", b.Scan.StartedAt, started)
	}
	if b.Scan.RepoIdentity != subjectIdentityNone || b.Scan.Repo != "" || b.Scan.TreeDirty != nil {
		t.Errorf("a non-repository dir must record repo %q identity %q tree_dirty %v; want \"\", none, nil", b.Scan.Repo, b.Scan.RepoIdentity, b.Scan.TreeDirty)
	}
}

// The checkout guard sees through spellings: a path inside the checkout is
// inside however it is written (absolute, through a link to the checkout, the
// checkout itself), and a sibling whose name merely starts with the
// checkout's is not.
func TestLedgerInsideCheckout(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{
		filepath.Join(repo, ".corral", "ledger"): true,
		repo:                                     true,
		filepath.Join(link, "ledger"):            true,
		filepath.Join(base, "repo2", "ledger"):   false,
		filepath.Join(base, "elsewhere"):         false,
	} {
		if got := ledgerInsideCheckout(dir, repo); got != want {
			t.Errorf("ledgerInsideCheckout(%s, %s) = %v, want %v", dir, repo, got, want)
		}
	}
	if ledgerInsideCheckout(filepath.Join(repo, "x"), "") {
		t.Error("with no --repo-dir there is no checkout to be inside")
	}
}
