// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// corral has no default models, so every fixture that reaches role resolution
// must name its own herd. These are the TEST's models, not product defaults.
const (
	testHerdWriter = "claude-sonnet-5"
	testHerdMutant = "claude-sonnet-5"
)

// gitCmd returns a helper that runs a git command in dir for a test fixture,
// skipping (not failing) the test when git is unusable in this environment —
// diff scoping's own logic is unit-tested regardless; these fixtures only
// need a working git to exist.
func gitCmd(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	return func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unusable here (%v): %s", err, out)
		}
	}
}

// gitRevParseHead returns dir's current commit SHA, captured at the moment
// of the call. The literal ref "HEAD" is not usable as a fixture's baseRef:
// by the time a later commit runs, "HEAD" resolves to THAT commit rather
// than the one intended as the base, and a diff against it would always be
// empty. Resolving to a concrete SHA up front pins the intended base.
func gitRevParseHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git rev-parse unusable here: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestChangedFilesListsOnlyWhatMoved proves changedFiles reports exactly the
// paths that differ from baseRef — the bound diff scoping rests on.
func TestChangedFilesListsOnlyWhatMoved(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "a.go"), "package p\n")
	mustWrite(t, filepath.Join(root, "b.go"), "package p\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	mustWrite(t, filepath.Join(root, "a.go"), "package p // changed\n")
	gitRun("add", "a.go")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	got, err := changedFiles(root, base)
	if err != nil {
		t.Fatalf("changedFiles: %v", err)
	}
	if len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("changed = %v, want [a.go]", got)
	}
}

// TestChangedFilesIsRelativeToRepoRootWhenRepoIsASubdirectory is Gap 3:
// `git diff --name-only` emits paths relative to the REPOSITORY root
// regardless of cwd, while reposcan.Enumerate produces paths relative to
// --repo. Point --repo at a subdirectory of a git repo (a package inside a
// monorepo) and, uncorrected, the two path frames never intersect — every
// candidate falls out as not-selected, blaming selection rather than the
// path mismatch.
func TestChangedFilesIsRelativeToRepoRootWhenRepoIsASubdirectory(t *testing.T) {
	top := t.TempDir()
	gitRun := gitCmd(t, top)
	sub := filepath.Join(top, "svc")
	mustWrite(t, filepath.Join(top, "README.md"), "root file\n")
	mustWrite(t, filepath.Join(sub, "a.go"), "package p\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, top)

	mustWrite(t, filepath.Join(sub, "a.go"), "package p // changed\n")
	gitRun("add", "svc/a.go")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	got, err := changedFiles(sub, base)
	if err != nil {
		t.Fatalf("changedFiles: %v", err)
	}
	// Relative to --repo (sub), not the git repository root: "a.go", never
	// "svc/a.go" — the latter would never match anything reposcan.Enumerate
	// produced under sub.
	if len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("changed = %v, want [a.go] (relative to --repo, not the git root)", got)
	}
}

// TestChangedFilesUsesAThreeDotRange is Gap 4a, an adjudicated fix to code
// the plan itself specified: two-dot `git diff <base>` compares trees
// directly, so once base has advanced past the branch point, files changed
// only ON BASE are reported as changed too and get audited — over-scoping,
// which is expensive (an audit costs ~84 full test-suite runs per file), not
// merely untidy. `<base>...HEAD` compares against the merge base, which is
// what "what this PR changed" means: here, a feature branch only ever
// touched a.go, but base's OWN tip moved on and touched b.go after the
// branch point — the diff must report only a.go.
func TestChangedFilesUsesAThreeDotRange(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "a.go"), "package p\n")
	mustWrite(t, filepath.Join(root, "b.go"), "package p\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	mainBranch := strings.TrimSpace(func() string {
		out, err := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			t.Skipf("git rev-parse --abbrev-ref unusable here: %v", err)
		}
		return string(out)
	}())

	gitRun("checkout", "-q", "-b", "feature")
	mustWrite(t, filepath.Join(root, "a.go"), "package p // feature change\n")
	gitRun("add", "a.go")
	gitRun("commit", "-q", "-m", "feature change", "--no-gpg-sign")

	gitRun("checkout", "-q", mainBranch)
	mustWrite(t, filepath.Join(root, "b.go"), "package p // base advanced\n")
	gitRun("add", "b.go")
	gitRun("commit", "-q", "-m", "base advanced", "--no-gpg-sign")

	gitRun("checkout", "-q", "feature")

	got, err := changedFiles(root, mainBranch)
	if err != nil {
		t.Fatalf("changedFiles: %v", err)
	}
	if len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("changed = %v, want [a.go] — b.go changed only on base after the branch point, and must not be reported as part of this PR's diff", got)
	}
}

// TestChangedFilesSurfacesGitStderr is Gap 4b: cmd.Output() with a bare %w
// wrap surfaced a bad ref to the operator as "exit status 128", discarding
// git's own explanation entirely. exec.ExitError.Stderr is already
// populated by cmd.Output(); it must be included in the returned error.
func TestChangedFilesSurfacesGitStderr(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("commit", "-q", "--allow-empty", "-m", "x", "--no-gpg-sign")

	_, err := changedFiles(root, "no-such-ref-at-all")
	if err == nil {
		t.Fatal("want an error for an unresolvable ref")
	}
	msg := err.Error()
	// The bare pre-fix form was exactly this, with nothing after it: the
	// %w wrap alone renders as "exit status <n>", no git detail attached.
	bareForm := "git diff against no-such-ref-at-all...HEAD: exit status 128"
	// Not asserting on git's own wording ("unknown revision", "bad
	// revision", ...): git localizes that text under a non-C locale, and
	// pinning LC_ALL here would test the test environment more than the
	// code. What matters is that SOMETHING beyond the bare exit code made
	// it into the error.
	if !strings.Contains(msg, "no-such-ref-at-all") {
		t.Errorf("error must name the bad ref: %v", err)
	}
	if strings.HasSuffix(strings.TrimRight(msg, "\n"), bareForm) {
		t.Errorf("error looks like the bare %%w form with no git detail appended: %v", err)
	}
}

// TestChangedFilesRejectsBaseRefStartingWithDash: baseRef is not passed to
// `git diff` behind a `--` separator, so a baseRef that starts with `-` is a
// legal-looking git OPTION, not a ref — and `git check-ref-format
// 'refs/heads/-evil'` exits 0, so a branch actually named that way is a
// valid ref an attacker fully controls (this is exactly --diff-base's
// documented threat model: a pull_request_target workflow passing
// `diff-base: ${{ github.head_ref }}`, where head_ref is the PR author's own
// branch name). `--output=<path>` makes git WRITE to an attacker-chosen path
// on the runner instead of comparing anything — confirmed by hand:
// `git diff --name-only --relative '--output=/tmp/x...HEAD'` creates
// /tmp/x...HEAD. changedFiles must refuse a leading-dash baseRef outright
// rather than ever handing it to git as an argument.
func TestChangedFilesRejectsBaseRefStartingWithDash(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("commit", "-q", "--allow-empty", "-m", "x", "--no-gpg-sign")

	sentinel := filepath.Join(t.TempDir(), "should-never-exist")
	badRef := "--output=" + sentinel
	_, err := changedFiles(root, badRef)
	if err == nil {
		t.Fatal("want an error for a baseRef starting with '-' — it must never reach git as a bare argument")
	}
	if !strings.Contains(err.Error(), badRef) {
		t.Errorf("error should name the rejected baseRef: %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatal("git actually executed --output and wrote a file — the leading-dash baseRef reached git as an option")
	}
}

// TestCertifyRepoDiffBaseBoundsTheJobSet: a repo where two files are
// candidates (both goaled, both paired with a test) but only one changed —
// the scan must emit one job, and must NOT rank or apply --top on this path,
// because in a PR the diff IS the bound.
func TestCertifyRepoDiffBaseBoundsTheJobSet(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg // changed\n")
	gitRun("add", "pkg/a.go")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	// Both files are named in the goals map, so the bound demonstrably comes
	// from the diff and not from which files happen to have a hand-written
	// goal.
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic", "pkg/b.go": "must not panic either"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals, "--dry-run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "1 job(s)") {
		t.Errorf("diff scoping did not bound the job set:\n%s", out.String())
	}
	if strings.Contains(out.String(), "ranked by") {
		t.Errorf("ranking ran on the diff path, where the diff is the bound:\n%s", out.String())
	}
	// The unchanged candidate must be ACCOUNTED, never silently dropped.
	if !strings.Contains(out.String(), reposcan.ReasonNotSelected) {
		t.Errorf("the unchanged candidate must be accounted as %s:\n%s", reposcan.ReasonNotSelected, out.String())
	}
}

// TestCertifyRepoDiffBaseEmptyScopeExitsZero is Gap 2: the most common PR in
// existence (here, one that changed nothing under the goal-eligible surface)
// legitimately has nothing in scope, and that must exit 0 — not read as a
// failed audit in CI. This is a real (non-dry-run) run: --substrate workspace
// is used so the assertion does not depend on bwrap being available on the
// test host, and it never reaches Execute because zero jobs are emitted.
func TestCertifyRepoDiffBaseEmptyScopeExitsZero(t *testing.T) {
	// The provider preflight demands a key regardless of scope (it is a
	// scan-wide fact checked before selection is even known); a zero-scope
	// scan never actually calls a model, so a placeholder value is enough.
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)
	// Nothing changes after base: the diff scope is empty.

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, want 0 for an empty diff scope: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "0 job(s)") {
		t.Errorf("want 0 jobs for an empty diff scope:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "NOTHING IN SCOPE") {
		t.Errorf("want the distinct empty-scope line, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "COULD-NOT-GRADE") {
		t.Errorf("an empty scope must not be reported as a grading failure:\n%s", out.String())
	}
}

// TestCertifyRepoDiffBaseNonEmptyScopeNothingGradableExitsNonZero is the
// other half of Gap 2: files WERE in scope (one candidate changed) and none
// of them could be graded — that is a real failure to report, and must stay
// exit 1, never silently read as green because the scan happened to be
// diff-scoped. The check command is `-- false`, which fails the baseline
// deterministically without ever reaching an LLM call; --substrate workspace
// avoids depending on bwrap on the test host.
func TestCertifyRepoDiffBaseNonEmptyScopeNothingGradableExitsNonZero(t *testing.T) {
	// The provider preflight demands a key before the audit runs, even though
	// the `-- false` baseline fails before any model would be called.
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg // changed\n")
	gitRun("add", "pkg/a.go")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace, "--", "false",
	}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (nothing graded out of a non-empty scope): stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "1 job(s)") {
		t.Errorf("want 1 job (the changed candidate was in scope):\n%s", out.String())
	}
	if !strings.Contains(out.String(), "COULD-NOT-GRADE") {
		t.Errorf("want the could-not-grade line, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "NOTHING IN SCOPE") {
		t.Errorf("a non-empty scope must not print the empty-scope line:\n%s", out.String())
	}
}

// TestCertifyRepoCacheDBAloneIsByteIdentical: naming --cache-db changes
// nothing about a scan's output, and on a dry run — which derives no goal
// and runs no selection pass — no cache file appears at the path named.
// The dry-run fixture below runs the enumerate/select/emit accounting path
// without a jail or a model call.
func TestCertifyRepoCacheDBAloneIsByteIdentical(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	baseArgs := []string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"}

	var outWithout, errWithout bytes.Buffer
	codeWithout := runCertifyRepo(baseArgs, &outWithout, &errWithout)

	dsn := filepath.Join(t.TempDir(), "would-be-cache.duckdb")
	argsWithDBFlag := append(append([]string{}, baseArgs...), "--cache-db", dsn)
	var outWithDBFlag, errWithDBFlag bytes.Buffer
	codeWithDBFlag := runCertifyRepo(argsWithDBFlag, &outWithDBFlag, &errWithDBFlag)

	if codeWithout != codeWithDBFlag {
		t.Fatalf("exit code changed by merely naming --cache-db: %d vs %d", codeWithout, codeWithDBFlag)
	}
	if outWithout.String() != outWithDBFlag.String() {
		t.Fatalf("stdout changed by merely naming --cache-db:\n--- baseline\n%s\n--- with --cache-db\n%s", outWithout.String(), outWithDBFlag.String())
	}
	if errWithout.String() != errWithDBFlag.String() {
		t.Fatalf("stderr changed by merely naming --cache-db:\n--- baseline\n%s\n--- with --cache-db\n%s", errWithout.String(), errWithDBFlag.String())
	}
	if _, statErr := os.Stat(dsn); !os.IsNotExist(statErr) {
		t.Fatalf("a dry run created a cache file at %s (stat err=%v)", dsn, statErr)
	}
}

// TestCertifyRepoRecordFailsOpen proves the FAIL-OPEN contract: an
// unopenable --record-db (a path inside a directory that does not exist)
// prints a loud failure line on stderr, but the scan's own exit code is
// UNCHANGED from the same scan run without --record at all — a full disk or
// a busy DuckDB file must never red-build a CI merge gate over bookkeeping.
// Both properties are asserted; asserting only one would not pin the
// property (a bug that changed the exit code AND still printed the line
// would slip past a test that checked only the stderr line, and vice
// versa).
//
// The fixture is the SAME shape as
// TestCertifyRepoDiffBaseNonEmptyScopeNothingGradableExitsNonZero: one
// candidate is in scope (diff-base sees a real change) and its check
// command is `-- false`, so the baseline fails deterministically and the
// scan exits 1 (COULD-NOT-GRADE) — chosen deliberately over the
// empty-diff-scope shape (which exits 0) because a fixture whose baseline
// exit code is already 0 cannot distinguish "the exit code was preserved"
// from "the record block returned 0 regardless" (an implementation that
// hard-coded `return 0` after a failed write would still pass against a
// 0-baseline fixture — the precise inversion that would silently turn a
// real failing merge gate green). No jail or real model call is reached
// either: `-- false` fails the baseline before any LLM would be invoked.
func TestCertifyRepoRecordFailsOpen(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg // changed\n")
	gitRun("add", "pkg/a.go")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	// flagArgs and checkArgv (everything after "--") are split BEFORE flag
	// parsing (splitCertifyArgs): --ledger must be inserted before the
	// "--", never appended after it, or it becomes an argument to the
	// "false" check command instead of a flag to this command.
	flagArgs := []string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace,
	}
	checkCmd := []string{"--", "false"}

	var outNoLedger, errNoLedger bytes.Buffer
	wantCode := runCertifyRepo(append(append(append([]string{}, flagArgs...), "--no-ledger"), checkCmd...), &outNoLedger, &errNoLedger)
	if wantCode != 1 {
		t.Fatalf("fixture precondition failed: baseline run exited %d, want 1 (COULD-NOT-GRADE) — this test cannot tell a preserved exit code from a hard-coded 0 unless the baseline itself is non-zero: stdout=%s stderr=%s", wantCode, outNoLedger.String(), errNoLedger.String())
	}

	// A ledger directory under a path that is a regular FILE: the entry
	// writer's MkdirAll must fail, and nothing can be written there.
	blocker := filepath.Join(root, "blocker")
	mustWrite(t, blocker, "not a directory\n")
	badDir := filepath.Join(blocker, "ledger")
	ledgerArgs := append(append([]string{}, flagArgs...), "--ledger", badDir)
	ledgerArgs = append(ledgerArgs, checkCmd...)

	var out, errb bytes.Buffer
	code := runCertifyRepo(ledgerArgs, &out, &errb)

	if code != wantCode {
		t.Fatalf("a failed ledger write changed the exit code: got %d, want %d (identical to the same scan with --no-ledger)", code, wantCode)
	}
	if !strings.Contains(errb.String(), "writing the ledger entry to "+badDir) {
		t.Errorf("want a loud fail-open line on stderr, got: %q", errb.String())
	}
	if entries, _ := auditpush.ReadLedgerDir(badDir); len(entries) != 0 {
		t.Errorf("an unwritable ledger directory has %d entries", len(entries))
	}
}

// TestCertifyRepoRecordRoundTripsReportedFiles proves the CLI actually wires
// the report's own accounting into the ledger: every file the printed
// report excluded, with its printed reason, is readable back from
// scan_files with a matching disposition and reason. The fixture uses the
// empty-diff-scope shape again (no jail, no model call) — it produces three
// distinct exclusion reasons (no-language, no-paired-test, not-selected)
// with zero audited files, which is enough to prove the wiring: the
// audited-row shape (KillRate, Survivors, evidence "proven") is already
// pinned directly against scanstore.Record by
// TestRecordRoundTripsEveryDisposition in internal/scanstore — this test's
// job is only to prove certify_repo_record.go hands the report's rows to it
// correctly, not to re-prove scanstore's own contract.
func TestCertifyRepoRecordRoundTripsReportedFiles(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)
	// Nothing changes after base: a.go (a candidate) is excluded
	// not-selected; b.go has no paired test; README.md has no language.

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace,
		"--ledger", ledgerDir,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("the ledger write must not fail on a writable directory, stderr=%q", errb.String())
	}

	re := regexp.MustCompile(`excluded (\S+) \((\S+)\)`)
	matches := re.FindAllStringSubmatch(out.String(), -1)
	if len(matches) < 3 {
		t.Fatalf("fixture did not print the exclusion lines this test needs (want >= 3, got %d):\n%s", len(matches), out.String())
	}

	// Read back through the VIEW, as any DuckDB reader would.
	db, err := auditpush.LoadDir(ledgerDir)
	if err != nil {
		t.Fatalf("load the ledger: %v", err)
	}
	defer db.Close()

	for _, m := range matches {
		path, wantReason := m[1], m[2]
		var disposition, gotReason string
		row := db.QueryRow(`SELECT disposition, reason FROM corral_audits WHERE path = ?`, path)
		if err := row.Scan(&disposition, &gotReason); err != nil {
			t.Fatalf("no corral_audits row for %s (printed as excluded with reason %s): %v", path, wantReason, err)
		}
		if disposition != "rejected" {
			t.Errorf("%s recorded with disposition %q, want %q", path, disposition, "rejected")
		}
		if gotReason != wantReason {
			t.Errorf("%s recorded with reason %q, want %q (from the printed report)", path, gotReason, wantReason)
		}
	}
}

// TestBuildScanFileRowsCarriesProvenMissed proves buildScanFileRows hands
// advpool.Verdict.ProvenMissed through into the scanstore.File row for an
// audited file, unit-level (no LLM call, no jail) — the direct fix for the
// gap this whole change closes: `certify --repo` computed ProvenMissed on a
// real converged verdict and then discarded it before it ever reached the
// ledger.
func TestBuildScanFileRowsCarriesProvenMissed(t *testing.T) {
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "src/flask/cli.py", Lang: "python"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.467, Survivors: 16, MutantsTotal: 30, ProvenMissed: 7, DevScored: true}},
	}
	rows := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", "", io.Discard)
	if len(rows) != 1 {
		t.Fatalf("buildScanFileRows returned %d rows, want 1", len(rows))
	}
	var got scanstore.File = rows[0]
	if got.ProvenMissed != 7 {
		t.Errorf("row.ProvenMissed = %d, want 7", got.ProvenMissed)
	}
}

// TestBuildScanFileRowsCarriesPoolTestUnsound mirrors the above for the F2
// fix's new diagnosis: a compiling authored test whose report never
// genuinely graded must reach the ledger row distinctly from
// TestWriterFailed.
func TestBuildScanFileRowsCarriesPoolTestUnsound(t *testing.T) {
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "unsound.py", Lang: "python"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, Survivors: 4, MutantsTotal: 10, PoolTestUnsound: true, DevScored: true}},
	}
	rows := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", "", io.Discard)
	if len(rows) != 1 {
		t.Fatalf("buildScanFileRows returned %d rows, want 1", len(rows))
	}
	if !rows[0].PoolTestUnsound {
		t.Error("row.PoolTestUnsound = false, want true")
	}
}

// TestBuildScanFileRowsCarriesConcurrency proves buildScanFileRows hands
// advpool.Verdict.Concurrency through into the scanstore.File row for an
// audited file — the ledger's half of "every reader says how many trees
// scored the file, or why one".
func TestBuildScanFileRowsCarriesConcurrency(t *testing.T) {
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "src/flask/cli.py", Lang: "python"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.467, Survivors: 16, MutantsTotal: 30, DevScored: true,
				Concurrency: advpool.Concurrency{Trees: 6}}},
		{Job: reposcan.Job{Path: "downgraded.py", Lang: "python"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, Survivors: 4, MutantsTotal: 10, DevScored: true,
				Concurrency: advpool.Concurrency{Trees: 1, Note: "suite is not concurrency-safe: baseline failed under 3"}}},
	}
	rows := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", "", io.Discard)
	if len(rows) != 2 {
		t.Fatalf("buildScanFileRows returned %d rows, want 2", len(rows))
	}
	byPath := map[string]scanstore.File{}
	for _, r := range rows {
		byPath[r.Path] = r
	}
	if got := byPath["src/flask/cli.py"]; got.Trees != 6 || got.ConcurrencyNote != "" {
		t.Errorf("row = %+v, want Trees 6, no note", got)
	}
	if got := byPath["downgraded.py"]; got.Trees != 1 || got.ConcurrencyNote != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("row = %+v, want the downgrade note preserved", got)
	}
	if rows[0].TestWriterFailed {
		t.Error("row.TestWriterFailed = true, want false — the test DID compile")
	}
}

// TestCertifyRepoRecordCoversExecutionStageRejections proves the fix for
// the review finding that every EXECUTION-stage rejection (prep-failed,
// baseline-failed, flaky-baseline, suite-ignores-file, executor-error,
// cancelled) used to have NO row at all: reposcan.Aggregate tallies an
// ungradable FileResult only into rep.Ungradable (a map[reason]int with no
// per-file path), so a file the scan actually selected, emitted a job for,
// and ran a check command against — the MOST EXPENSIVE rejections in the
// product — was invisible to "why did file X get skipped on scan N", the
// exact question internal/scanstore's package doc promises this ledger
// answers.
//
// The fixture: one real candidate (pkg/a.go, paired with pkg/a_test.go,
// goaled, and IN diff scope) whose check command is `-- false`, so its
// baseline fails deterministically (ReasonBaselineFailed) — no jail, no
// real model call, since a failed baseline short-circuits before any LLM
// audit runs. Plus one enumerate-level exclusion (README.md, no-language)
// to prove the fix didn't regress the OTHER source this function reads
// from.
//
// Two things are asserted directly, both requested by the review:
//  1. pkg/a.go — the file the scan actually worked on — has a scan_files
//     row, disposition "rejected", reason "baseline-failed".
//  2. count(scan_files) for this scan reconciles with scans.total_files:
//     before this fix, total_files counted every file on disk (4:
//     a.go, a_test.go, README.md, goals.json) while scan_files held only
//     the 3 rows this function could see (a_test.go and README.md from
//     Enumerate, goals.json is not a repo file at all so it was never a
//     row) — a.go itself, the one file actually graded, was the missing
//     row. This test would have failed before the fix: querying
//     scan_files for pkg/a.go would have returned sql.ErrNoRows.
func TestCertifyRepoRecordCoversExecutionStageRejections(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg // changed\n")
	gitRun("add", "pkg/a.go")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace,
		"--ledger", ledgerDir,
		"--", "false",
	}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (COULD-NOT-GRADE): stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "COULD-NOT-GRADE") {
		t.Fatalf("fixture precondition failed: want the baseline-failed shape:\n%s", out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("the ledger write must not fail on a writable directory, stderr=%q", errb.String())
	}

	db, err := auditpush.LoadDir(ledgerDir)
	if err != nil {
		t.Fatalf("load the ledger: %v", err)
	}
	defer db.Close()

	var scanUID string
	var totalFiles int
	if err := db.QueryRow(`SELECT scan_uid, total_files FROM corral_scans ORDER BY ts DESC LIMIT 1`).Scan(&scanUID, &totalFiles); err != nil {
		t.Fatalf("select the recorded scan header: %v", err)
	}

	var disposition, reason string
	row := db.QueryRow(`SELECT disposition, reason FROM corral_audits WHERE scan_uid = ? AND path = ?`, scanUID, "pkg/a.go")
	if err := row.Scan(&disposition, &reason); err != nil {
		t.Fatalf("pkg/a.go — the ONE file this scan actually ran a check command against — has no corral_audits row: %v (this is the exact regression the review caught: execution-stage rejections were recorded nowhere)", err)
	}
	if disposition != "rejected" || reason != reposcan.ReasonBaselineFailed {
		t.Errorf("pkg/a.go recorded as disposition=%q reason=%q, want rejected/%s", disposition, reason, reposcan.ReasonBaselineFailed)
	}

	// On disk: pkg/a.go, pkg/a_test.go, README.md, goals.json = 4 files, and
	// goals.json IS inside the walked repo tree (it lives at root, same as
	// the source files) — Enumerate classifies it no-language same as
	// README.md, so it's an ordinary enumerate-level exclusion, not
	// special-cased. cands = 1 (pkg/a.go); enumExcl = 3 (a_test.go is-test,
	// README.md no-language, goals.json no-language); total_files =
	// cands + enumExcl = 4. Pinned directly (not just cross-checked against
	// rowCount below) so a future drift in this arithmetic fails loudly
	// here instead of silently — this exact comment previously claimed 3,
	// which was wrong, and nothing caught it because the only assertion was
	// the reconciliation check, which passes for any matching pair.
	if totalFiles != 4 {
		t.Fatalf("corral_scans.total_files = %d, want 4 (fixture precondition — see the comment above)", totalFiles)
	}

	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM corral_audits WHERE scan_uid = ?`, scanUID).Scan(&rowCount); err != nil {
		t.Fatalf("count corral_audits: %v", err)
	}
	// This scan now records exactly 4 rows: a.go (rejected/baseline-failed),
	// a_test.go (rejected/is-test), README.md (rejected/no-language),
	// goals.json (rejected/no-language).
	if rowCount != totalFiles {
		t.Errorf("count(scan_files) = %d, scans.total_files = %d — the ledger's header and detail disagree; before this fix rowCount was 2 (a_test.go, README.md) against total_files 3, silently missing pkg/a.go", rowCount, totalFiles)
	}
}

// TestCertifyRepoRecordTopReflectsTheEffectiveBound proves the fix for a
// provenance overclaim the review caught: with --goals given and no
// EXPLICIT --top, `limit` is forced to 0 (unbounded — a hand-written goals
// map has already chosen the surface, see the comment above the else
// branch in runCertifyRepo) while *topFlag stays its default (25). Before
// the fix, scans.top recorded *topFlag — 25 — even though the scan applied
// NO bound at all; a reader of the ledger could not tell that row apart
// from a genuine top-25 scan. 0 is already --top's own sentinel for
// "unbounded" (its help text: "0 or --all = every candidate"), so the fix
// records the EFFECTIVE limit, which is already 0 in this exact shape —
// no schema change needed, just recording the right number.
//
// The fixture: 3 goaled, paired candidates and no --top given at all —
// mirrors TestCertifyRepoGoalsFileIsNotBoundedByTheDefaultTop's own
// report-level assertion ("3 job(s)"), but checks the LEDGER's scans.top
// column instead of stdout.
func TestCertifyRepoRecordTopReflectsTheEffectiveBound(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	goalsMap := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(root, "pkg", n+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", n+"_test.go"), "package pkg\n")
		goalsMap["pkg/"+n+".go"] = "must not panic"
	}
	goalsJSON, err := json.Marshal(goalsMap)
	if err != nil {
		t.Fatal(err)
	}
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, string(goalsJSON))
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "3 job(s)") {
		t.Fatalf("fixture precondition failed: want all 3 goaled candidates unbounded (no --top given):\n%s", out.String())
	}

	// --dry-run writes no entry (it audits nothing), so this test drives a
	// real (--substrate workspace, no jail/model-call-needed) run instead —
	// baseline fails deterministically via `-- false`, so nothing is ever
	// actually audited, but the SELECTION (all 3, unbounded) already
	// happened before any job ran, which is what the entry's top records.
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	var out2, errb2 bytes.Buffer
	code2 := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--substrate", substrateWorkspace,
		"--ledger", ledgerDir, "--", "false",
	}, &out2, &errb2)
	if code2 != 1 {
		t.Fatalf("exit %d, want 1 (COULD-NOT-GRADE): stdout=%s stderr=%s", code2, out2.String(), errb2.String())
	}
	if errb2.Len() != 0 {
		t.Fatalf("the ledger write must not fail on a writable directory, stderr=%q", errb2.String())
	}

	entries, err := auditpush.ReadLedgerDir(ledgerDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ledger entries = %d (err %v), want exactly 1", len(entries), err)
	}
	if top := entries[0].Bundle.Scan.Top; top != 0 {
		t.Errorf("the entry's top = %d, want 0 (unbounded — this scan applied no --top bound at all; %d would positively assert a top-%d scan that never happened, indistinguishable from a real one)", top, defaultScanTop, defaultScanTop)
	}
}

// TestCertifyRepoRecordUngradableEvidenceReflectsWhetherTheCheckRan proves
// the fix for a review finding that every ungradable execution-stage
// rejection was stamped evidence="proven", including prep-failed and
// cancelled — the two reasons that mean the check command NEVER RAN at
// all (prep-failed returns before localExecutor.Execute ever reaches
// l.newBaseline; cancelled is written by reposcan.Scan itself before
// ex.Execute is even called). "proven" is the label this table's
// defensibility rests on ("corral actually executed something"), and
// stamping it on a file nothing ever ran against is exactly the overclaim
// evidence exists to prevent. This pins the unit directly (ungradableEvidence),
// which is easier to drive across all 6 ungradable reasons than a full CLI
// fixture for each one (prep-failed and cancelled specifically are hard to
// reach through the public CLI without faking jail/context-cancellation
// conditions) — the OTHER four reasons ARE covered end-to-end by
// TestCertifyRepoRecordCoversExecutionStageRejections (baseline-failed).
func TestCertifyRepoRecordUngradableEvidenceReflectsWhetherTheCheckRan(t *testing.T) {
	neverRan := []string{reposcan.ReasonPrepFailed, reposcan.ReasonCancelled}
	for _, reason := range neverRan {
		if got := ungradableEvidence(reason); got != "" {
			t.Errorf("ungradableEvidence(%q) = %q, want \"\" (the check command never ran for this reason)", reason, got)
		}
	}
	ran := []string{reposcan.ReasonBaselineFailed, reposcan.ReasonFlakyBaseline, reposcan.ReasonSuiteIgnoresFile, reposcan.ReasonExecutorError}
	for _, reason := range ran {
		if got := ungradableEvidence(reason); got != "proven" {
			t.Errorf("ungradableEvidence(%q) = %q, want \"proven\" (the check command ran at least once for this reason)", reason, got)
		}
	}
}

// TestCertifyRepoRecordPreflightOverlayRoundTrips proves --preflight's
// finding actually reaches the ledger: preflight_state reads back
// "executed" for a file the instrumented suite touched, "not-executed" for
// a file it never touched, and "" for a path the pre-flight never measured
// at all (a _test.go file, and go.mod — neither is a language-detected
// SOURCE file the pre-flight instruments).
//
// This needs no jail, no API key and no real model call: the empty-diff-
// scope shape (nothing changed since base) emits ZERO jobs, but the
// pre-flight instruments every enumerated SOURCE file independent of
// --diff-base (see runPreflight's own doc comment) — so a real
// `go test ./...` DOES run, over a tiny two-function fixture module, on
// --substrate workspace (no bwrap needed). It needs only the go toolchain
// this test suite already depends on to build itself.
func TestCertifyRepoRecordPreflightOverlayRoundTrips(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	// b.go has no paired test — enumerate-level ReasonNoPairedTest, but it
	// IS a language-detected source file, so the pre-flight still
	// instruments it (see enumeratedSourcePaths): it must read back
	// "not-executed", never absent, since nothing in this fixture ever
	// calls Sub.
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n\nfunc Sub(a, b int) int { return a - b }\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)
	// Nothing changes after base: the diff scope is empty (0 jobs), but the
	// pre-flight still instruments the whole enumerated source set.

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace, "--preflight",
		"--ledger", ledgerDir,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("the ledger write must not fail on a writable directory, stderr=%q", errb.String())
	}
	if !strings.Contains(out.String(), "file(s) executed at least once") {
		t.Fatalf("fixture precondition failed: --preflight did not actually run (need a working go toolchain):\n%s", out.String())
	}

	db, err := auditpush.LoadDir(ledgerDir)
	if err != nil {
		t.Fatalf("load the ledger: %v", err)
	}
	defer db.Close()

	for _, tc := range []struct{ path, want string }{
		{"pkg/a.go", "executed"},
		{"pkg/b.go", "not-executed"},
		{"pkg/a_test.go", ""}, // a test file — never a pre-flight subject
		{"go.mod", ""},        // not a language-detected source file
	} {
		var got string
		if err := db.QueryRow(`SELECT preflight_state FROM corral_audits WHERE path = ?`, tc.path).Scan(&got); err != nil {
			t.Fatalf("%s: no corral_audits row: %v", tc.path, err)
		}
		if got != tc.want {
			t.Errorf("%s: preflight_state = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestCertifyRepoRejectsFlagsAfterDashDash proves the fix for a silent-gate
// bug: splitCertifyArgs (shared with `certify --local`) splits on the
// first literal "--" with no idea which flags belong to `certify --repo`,
// so a flag placed AFTER "--" by mistake used to be silently handed to the
// check command as a plain argument instead of ever being parsed — no
// error, no warning. For most flags that's confusing; for --min-kill-rate
// it is dangerous: `-- pytest -q --min-kill-rate 0.5` used to run pytest
// with "--min-kill-rate 0.5" as ordinary (ignored) arguments, apply NO
// threshold at all, and let CI go green on a repo the threshold would have
// failed. This must be a hard exit-2 usage error, never a warning (a
// warning scrolls past in CI, and the failure mode is a gate that
// silently never runs) — checked for both --min-kill-rate (the dangerous
// one) and --ledger (where the record goes).
func TestCertifyRepoRejectsFlagsAfterDashDash(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"min-kill-rate", []string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--", "pytest", "-q", "--min-kill-rate", "0.5"}},
		{"ledger", []string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--", "pytest", "-q", "--ledger", "/tmp/x"}},
		// --timeout is new: it joins the same guard automatically (names
		// come from fs.VisitAll, not a hardcoded list — see
		// TestCheckArgvNoFlagCollisionDerivesNamesFromTheFlagSet) and this
		// pins that it actually does.
		{"timeout", []string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--", "pytest", "-q", "--timeout", "5m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := runCertifyRepo(tc.args, &out, &errb)
			if code != 2 {
				t.Fatalf("exit %d, want 2 (usage error): stdout=%s stderr=%s", code, out.String(), errb.String())
			}
			if !strings.Contains(errb.String(), "--"+tc.name) {
				t.Errorf("stderr must name the offending flag %q, got: %q", "--"+tc.name, errb.String())
			}
		})
	}
}

// TestCertifyRepoRejectsStrayPositionalAfterRecordPath is the OTHER door
// the same silent-no-gate bug walks in through — the review's finding that
// TestCertifyRepoRejectsFlagsAfterDashDash's own `--` fix (checkArgvNoFlagCollision)
// does NOT close. A BOOL flag handed a value the way a string flag takes
// one — `--preflight tape.json --min-kill-rate abc` — leaves "tape.json"
// as an unconsumed positional. Go's flag.Parse stops at the first non-flag
// argument and returns — --min-kill-rate is never even LOOKED at, let
// alone validated or applied, and nothing says so: no error (the CONTROL
// case below, `--min-kill-rate abc` alone, IS caught — "abc" is not a
// number), no warning, the scan runs with the merge gate silently absent.
// fs.NArg() > 0 after a clean Parse is what catches this, independent of
// which flag or typo produced the stray token.
func TestCertifyRepoRejectsStrayPositionalAfterABoolFlag(t *testing.T) {
	root := t.TempDir()

	// Control: --min-kill-rate alone with a bad value IS caught today —
	// this pins that the bug is specific to the stray-positional shape,
	// not a general regression in --min-kill-rate validation.
	var controlOut, controlErr bytes.Buffer
	controlCode := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--min-kill-rate", "abc"}, &controlOut, &controlErr)
	if controlCode != 2 {
		t.Fatalf("control case: exit %d, want 2 (--min-kill-rate abc alone must already be rejected): stdout=%s stderr=%s", controlCode, controlOut.String(), controlErr.String())
	}

	// The bug: same bad --min-kill-rate value, but preceded by a bool flag
	// given the STRING-flag way.
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run",
		"--preflight", "tape.json", "--min-kill-rate", "abc",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage error: a stray positional must not silently swallow --min-kill-rate): stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "tape.json") {
		t.Errorf("stderr must name the stray positional %q, got: %q", "tape.json", errb.String())
	}
}

// TestCertifyRepoAcceptsLegitimateCheckArgvWithUnrelatedFlags is the other
// half of the same fix: a real check command carrying flags that are NOT
// certify --repo's own must run completely untouched — a false positive
// here would break every real invocation that gives -- <cmd> at all
// (pytest, go test, and every other test runner's own flag surface).
func TestCertifyRepoAcceptsLegitimateCheckArgvWithUnrelatedFlags(t *testing.T) {
	root := t.TempDir()
	for _, tc := range [][]string{
		{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--", "pytest", "-q", "-x", "--tb=short"},
		{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--", "go", "test", "./...", "-count=1"},
	} {
		var out, errb bytes.Buffer
		code := runCertifyRepo(tc, &out, &errb)
		if code != 0 {
			t.Fatalf("args %v: exit %d, want 0 (a legitimate check command must not be rejected): stdout=%s stderr=%s", tc, code, out.String(), errb.String())
		}
	}
}

// TestCheckArgvNoFlagCollisionDerivesNamesFromTheFlagSet pins the
// mechanism directly, without a full CLI invocation: names come from
// fs.VisitAll (every flag ever registered, set or not), not a hardcoded
// list, so a flag added to runCertifyRepo tomorrow is covered
// automatically.
func TestCheckArgvNoFlagCollisionDerivesNamesFromTheFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("frobnicate", "", "made up for this test")

	if err := checkArgvNoFlagCollision(fs, []string{"--frobnicate", "x"}); err == nil {
		t.Fatal("want an error for a flag defined on fs, even one this test just invented")
	}
	if err := checkArgvNoFlagCollision(fs, []string{"-frobnicate=x"}); err == nil {
		t.Fatal("want an error for the single-dash, =value spelling too")
	}
	if err := checkArgvNoFlagCollision(fs, []string{"--unrelated", "-q", "plain-arg"}); err != nil {
		t.Fatalf("want no error for tokens that are not fs's own flags: %v", err)
	}
}

// TestCertifyRepoRejectsUnknownSubstrate proves an unrecognized --substrate
// value is a usage error (exit 2), never a silent fall-through to the jail
// default — a run that quietly used the wrong substrate while claiming the
// other is exactly the accountability failure this branch closes.
func TestCertifyRepoRejectsUnknownSubstrate(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--substrate", "docker", "--dry-run"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage error) for an unrecognized substrate; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "docker") {
		t.Errorf("stderr should name the bad value: %q", errb.String())
	}
}

// TestCertifyRepoAcceptsKnownSubstrateValues proves both real substrate names
// are accepted flag values.
func TestCertifyRepoAcceptsKnownSubstrateValues(t *testing.T) {
	root := t.TempDir()
	for _, s := range []string{substrateJail, substrateWorkspace} {
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--substrate", s, "--dry-run"}, &out, &errb)
		if code != 0 {
			t.Fatalf("--substrate %s: exit %d, stderr=%s", s, code, errb.String())
		}
	}
}

// TestCertifyRepoRejectsOutOfRangeMinKillRate proves --min-kill-rate is
// validated at flag-parse time, before enumeration even runs: an out-of-range
// value is a usage error (exit 2), matching how --substrate already rejects
// an unknown value, never a silent clamp or a threshold nothing can breach.
func TestCertifyRepoRejectsOutOfRangeMinKillRate(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"1.5", "-0.1", "2", "-1"} {
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--min-kill-rate", bad, "--dry-run"}, &out, &errb)
		if code != 2 {
			t.Fatalf("--min-kill-rate %s: exit %d, want 2 (usage error); stdout=%s stderr=%s", bad, code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), bad) {
			t.Errorf("--min-kill-rate %s: stderr should name the bad value: %q", bad, errb.String())
		}
	}
}

// TestCertifyRepoRejectsUnparseableMinKillRate proves a non-numeric value is
// also a usage error, not a silent fall-through to "unset".
func TestCertifyRepoRejectsUnparseableMinKillRate(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--min-kill-rate", "not-a-number", "--dry-run"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage error); stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "not-a-number") {
		t.Errorf("stderr should name the bad value: %q", errb.String())
	}
}

// TestCertifyRepoAcceptsBoundaryMinKillRateValues proves 0.0 and 1.0 are
// valid (the range is inclusive on both ends), and that a bare integer
// string ("0", "1") parses too.
func TestCertifyRepoAcceptsBoundaryMinKillRateValues(t *testing.T) {
	root := t.TempDir()
	for _, ok := range []string{"0", "0.0", "1", "1.0", "0.5"} {
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--min-kill-rate", ok, "--dry-run"}, &out, &errb)
		if code != 0 {
			t.Fatalf("--min-kill-rate %s: exit %d, want 0; stdout=%s stderr=%s", ok, code, out.String(), errb.String())
		}
	}
}

// TestParseMinKillRateValidation pins parseMinKillRate directly (the flag
// value is validated by this function before enumeration ever runs).
func TestParseMinKillRateValidation(t *testing.T) {
	valid := []string{"0", "0.0", "1", "1.0", "0.5", "0.999"}
	for _, s := range valid {
		if _, err := parseMinKillRate(s); err != nil {
			t.Errorf("parseMinKillRate(%q): unexpected error %v", s, err)
		}
	}
	// NaN is the value the naive "v < 0 || v > 1" range check cannot reject:
	// strconv.ParseFloat parses it cleanly (err == nil) and every comparison
	// against NaN is false, so both bounds silently pass. ParseFloat is also
	// case-insensitive, so the lowercase spelling must be caught too.
	invalid := []string{"1.1", "-0.0001", "2", "-1", "abc", "", "  ", "1,0", "NaN", "nan"}
	for _, s := range invalid {
		if _, err := parseMinKillRate(s); err == nil {
			t.Errorf("parseMinKillRate(%q): want an error, got none", s)
		}
	}
}

// TestNewLocalExecutorSkipsSandboxForWorkspaceSubstrate proves the jail
// preflight is conditional on the selected substrate: the workspace substrate
// needs no jail by construction (buildJailWiring's workspace branch never
// builds a seed, resolves an isolator, or binds a mount), so a host with no
// working bwrap must not be refused when --substrate workspace is selected —
// that is exactly the CI runner this substrate exists to serve.
func TestNewLocalExecutorSkipsSandboxForWorkspaceSubstrate(t *testing.T) {
	var resolutions int
	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) {
		resolutions++
		return nil, errors.New("no bwrap on this host")
	}
	t.Cleanup(func() { resolveJailFn = orig })

	ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, io.Discard)
	defer ex.Close()
	if err := ex.preflight(); err != nil {
		t.Fatalf("workspace substrate must not demand a sandbox: %v", err)
	}
	if resolutions != 0 {
		t.Errorf("workspace substrate resolved the sandbox %d time(s), want 0", resolutions)
	}
}

// TestNewLocalExecutorStillRequiresSandboxForJailSubstrate is the other
// direction of the same fix: the jail substrate (the default) must still
// refuse, with the SAME message, on a host that cannot sandbox — this
// behaviour must not regress while fixing the workspace path.
func TestNewLocalExecutorStillRequiresSandboxForJailSubstrate(t *testing.T) {
	wantErr := errors.New("no bwrap on this host")
	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) {
		return nil, wantErr
	}
	t.Cleanup(func() { resolveJailFn = orig })

	for _, substrate := range []string{"", substrateJail} {
		ex := newLocalExecutor(t.TempDir(), nil, substrate, 0, io.Discard)
		err := ex.preflight()
		ex.Close()
		if err == nil {
			t.Fatalf("substrate %q: jail substrate must still refuse when no sandbox resolves", substrate)
		}
		if !errors.Is(err, wantErr) {
			t.Errorf("substrate %q: preflight error = %v, want it to wrap %v (the same message)", substrate, err, wantErr)
		}
	}
}

// TestNewLocalExecutorSkipsTheSeedCacheForWorkspaceSubstrate is the seed-cache
// corollary of Gap 1: buildJailWiring's workspace branch never reads a
// shared seed (it builds its own empty overlay and mutates repoDir
// directly), but before this fix the seed cache was wired unconditionally.
// On the workspace path that meant a failed `go mod vendor` — no network, a
// private module proxy, a small TMPDIR: normal conditions on the ephemeral
// CI runner this substrate exists to serve — cached an error that turned
// EVERY job ungradable (Execute's `l.seeds != nil` branch), a false
// COULD-NOT-GRADE red build from jail prep the run was never going to use.
// The fix must not wire a seed cache at all for substrateWorkspace, proven
// here as a builder invocation count of zero: nil is the only way Execute's
// existing nil-cache branch (already exercised by every other seam-level
// test in this file) can apply.
func TestNewLocalExecutorSkipsTheSeedCacheForWorkspaceSubstrate(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package p\n")
	mustWrite(t, filepath.Join(root, "a_test.go"), "package p\n")

	ex := newLocalExecutor(root, nil, substrateWorkspace, 0, io.Discard)
	defer ex.Close()
	if ex.seeds != nil {
		t.Fatal("workspace substrate must not wire a seed cache at all")
	}

	// Load-bearing, not just cosmetic: drive a real Execute and prove no
	// seed ever reaches either seam, and a job is gradable — this is the
	// exact path that used to fail closed with ReasonPrepFailed when
	// ensureGoVendored errored.
	ex.newBaseline = func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
		if in.seed != nil {
			t.Error("workspace substrate must never receive a shared seed")
		}
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
		if in.seed != nil {
			t.Error("workspace substrate must never receive a shared seed")
		}
		return advpool.Verdict{DevKillRate: 1, MutantsTotal: 1}, nil
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{
		Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "g"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Gradable || res.Reason == reposcan.ReasonPrepFailed {
		t.Errorf("workspace substrate must not fail closed on a seed it never builds: %+v", res)
	}
}

// TestNewLocalExecutorStillWiresTheSeedCacheForJailSubstrate is the other
// direction: the jail substrate's seed-build behaviour (built once, shared
// across every job of a language — TestLocalExecutorSharesOneSeedAcrossJobs
// already proves the sharing) must be unchanged by the workspace-substrate
// guard added above. "" (today's shipped default) and the explicit
// substrateJail value both still wire a real cache.
func TestNewLocalExecutorStillWiresTheSeedCacheForJailSubstrate(t *testing.T) {
	for _, substrate := range []string{"", substrateJail} {
		ex := newLocalExecutor(t.TempDir(), nil, substrate, 0, io.Discard)
		if ex.seeds == nil {
			t.Errorf("substrate %q: no seed cache wired — every job would re-prepare its own jail", substrate)
		}
		ex.Close()
	}
}

// TestLocalExecutorThreadsSubstrateIntoAuditInput proves the value actually
// arrives at localAuditInput — the seam the cache key is later computed
// from — rather than merely existing as an unused field. A test asserting
// only that the scan runs would still pass with substrate silently stuck at
// "".
func TestLocalExecutorThreadsSubstrateIntoAuditInput(t *testing.T) {
	var gotBaseline, gotAudit string
	ex := localExecutor{
		baselineRuns: 2,
		substrate:    substrateWorkspace,
		newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
			gotBaseline = in.substrate
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
			gotAudit = in.substrate
			return advpool.Verdict{DevKillRate: 1}, nil
		},
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "a.go", Goal: reposcan.Goal{Text: "g"}}); err != nil {
		t.Fatal(err)
	}
	if gotBaseline != substrateWorkspace || gotAudit != substrateWorkspace {
		t.Fatalf("substrate did not reach localAuditInput: baseline=%q audit=%q, want %q", gotBaseline, gotAudit, substrateWorkspace)
	}
}

// TestLocalExecutorThreadsTimeoutIntoAuditInput proves Fix 2's whole point:
// `--timeout` was previously accepted nowhere on `certify --repo`, so every
// job's localAuditInput.timeout was left at the zero value and auditOneFile
// silently fell back to its own 10-minute default with no way for an
// operator to give a large file more room — the exact reason the flask
// repro (src/flask/cli.py) had nothing but its own 10 minutes to work with.
// A value set on localExecutor.timeout (what newLocalExecutor wires from
// the CLI's --timeout flag) must arrive at localAuditInput.timeout for
// every job, mirroring TestLocalExecutorThreadsSubstrateIntoAuditInput's
// proof for --substrate.
func TestLocalExecutorThreadsTimeoutIntoAuditInput(t *testing.T) {
	var gotBaseline, gotAudit time.Duration
	want := 27 * time.Minute
	ex := localExecutor{
		baselineRuns: 2,
		timeout:      want,
		newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
			gotBaseline = in.timeout
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
			gotAudit = in.timeout
			return advpool.Verdict{DevKillRate: 1}, nil
		},
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "a.go", Goal: reposcan.Goal{Text: "g"}}); err != nil {
		t.Fatal(err)
	}
	if gotBaseline != want || gotAudit != want {
		t.Fatalf("timeout did not reach localAuditInput: baseline=%v audit=%v, want %v", gotBaseline, gotAudit, want)
	}
}

// TestCertifyRepoTimeoutFlagThreadsThroughAndSurvivesTheFlagCollisionGuard
// exercises the whole CLI path: --timeout given (legitimately) BEFORE `--`
// must parse, must reach newLocalExecutor (proven via the same drive/resolve
// seam TestNewLocalExecutorResolvesTheSandboxOnceAcrossJobs uses), and a
// LEGITIMATE `-- pytest -q` after it must still run untouched — the new flag
// must not itself trip checkArgvNoFlagCollision's guard when it is NOT the
// one placed after `--` (that misuse is covered separately by the "timeout"
// case in TestCertifyRepoRejectsFlagsAfterDashDash).
func TestCertifyRepoTimeoutFlagParsesBeforeDashDash(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--timeout", "27m",
		"--", "pytest", "-q",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, want 0 (a legitimate --timeout before -- must parse and a real check command after it must run): stdout=%s stderr=%s", code, out.String(), errb.String())
	}
}

func TestCertifyRepoRequiresRepoDir(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--goals", "g.json"}, &out, &errb)
	if code == 0 {
		t.Fatal("missing --repo should be an error")
	}
	if !strings.Contains(errb.String(), "--repo is required") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// --goals is OPTIONAL now: without it the scan derives a goal per file. The
// only remaining required flag is --repo, covered above.
func TestCertifyRepoWithoutGoalsDoesNotDemandThem(t *testing.T) {
	root := t.TempDir()
	// No candidates in an empty tree, so no derivation is attempted and no
	// provider credential is needed — the point is only that the old
	// "--goals is required" refusal is gone.
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "--goals is required") {
		t.Errorf("--goals is no longer required: %q", errb.String())
	}
}

// The dry-run path exercises enumerate + emit + accounting without a jail,
// so it is safe and fast in CI.
func TestCertifyRepoDryRunReportsAccounting(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic on empty input"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"1 job", "no-paired-test", "no-language"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, s)
		}
	}
}

// TestCertifyRepoReportsBothExclusionSources proves the report accounts for
// BOTH exclusion sources — the enumerator's (no-language / is-test /
// no-paired-test) AND EmitJobs' ungoaled ones. Dropping either silently
// breaks the coverage story the headline number depends on.
func TestCertifyRepoReportsBothExclusionSources(t *testing.T) {
	root := t.TempDir()
	// a.go is goaled; b.go is a valid candidate with NO goal (ungoaled);
	// README.md has no language; c.go has no paired test.
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "c.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic on empty input"}`)

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"pkg/b.go (" + reposcan.ReasonUngoaled + ")",     // from EmitJobs
		"pkg/c.go (" + reposcan.ReasonNoPairedTest + ")", // from Enumerate
		"pkg/a_test.go (" + reposcan.ReasonIsTest + ")",  // from Enumerate
		"README.md (" + reposcan.ReasonNoLanguage + ")",  // from Enumerate
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing exclusion %q:\n%s", want, s)
		}
	}
	// 6 files are excluded from the audit: 5 non-candidates (a_test.go,
	// b_test.go, c.go, README.md, goals.json) plus the ungoaled candidate b.go.
	if !strings.Contains(s, "6 file(s) excluded from the audit") {
		t.Errorf("want 6 exclusions (4 above + b_test.go + goals.json):\n%s", s)
	}
	// Finding 1 regression: the file total must be candidates + ENUMERATE-only
	// exclusions. b.go is BOTH a candidate and (ungoaled) an exclusion, so
	// counting the merged exclusion slice double-counted it. 7 files exist on
	// disk: a.go a_test.go b.go b_test.go c.go README.md goals.json.
	if !strings.Contains(s, "7 file(s) walked") {
		t.Errorf("file total must not double-count the ungoaled candidate b.go (want 7):\n%s", s)
	}
}

// TestCertifyRepoFileTotalMatchesDiskWithManyUngoaled is the wider version of
// the same arithmetic: with several ungoaled candidates the double-count grew
// with them, so a repo could report more files than it contains — in a number
// a later slice signs and anchors.
func TestCertifyRepoFileTotalMatchesDiskWithManyUngoaled(t *testing.T) {
	root := t.TempDir()
	// 4 goal-less candidates + 1 goaled, each with a paired test = 10 files,
	// plus goals.json = 11 on disk.
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		mustWrite(t, filepath.Join(root, "pkg", n+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", n+"_test.go"), "package pkg\n")
	}
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "11 file(s) walked") {
		t.Errorf("want 11 files walked (5 src + 5 tests + goals.json):\n%s", s)
	}
	if !strings.Contains(s, "5 candidate(s)") {
		t.Errorf("want 5 candidates:\n%s", s)
	}
	if !strings.Contains(s, "1 job(s)") {
		t.Errorf("want 1 job (only pkg/a.go is goaled):\n%s", s)
	}
	// 4 ungoaled + 5 test files + goals.json = 10 excluded from the audit.
	if !strings.Contains(s, "10 file(s) excluded from the audit") {
		t.Errorf("want 10 exclusions:\n%s", s)
	}
}

// TestRepoScanExitCodeNothingAuditedIsNonZero is Finding 4: a scan in which
// every file failed to grade must not read as green to CI. This is the
// whole-repo (non-diff) path — nothingInScope is always false there — so its
// exit codes must be unchanged by the --diff-base distinction added below.
func TestRepoScanExitCodeNothingAuditedIsNonZero(t *testing.T) {
	nothing := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonExecutorError},
	}, nil)
	if got := repoScanExitCode(nothing, false, 0, nil, nil); got == 0 {
		t.Errorf("a scan that graded nothing must exit non-zero, got %d", got)
	}

	graded := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.9}},
	}, nil)
	if got := repoScanExitCode(graded, false, 0, nil, nil); got != 0 {
		t.Errorf("a scan that graded something must exit 0, got %d", got)
	}
}

// TestRepoScanExitCodeAllTimedOutIsNonZero is review item 3: a scan whose
// only graded files are Verdict.TimedOut (banked by driveLocalRun's
// bankableTimeoutVerdict) has Audited > 0 — the dev-adequacy measurement is
// real — but corral's own adversarial verification (test-writer, critic)
// never ran to completion for ANY of them. A merge gate going green here
// would be the silent-no-gate class this scan already closes three other
// ways, arriving by a fourth: exit code must stay non-zero.
func TestRepoScanExitCodeAllTimedOutIsNonZero(t *testing.T) {
	allTimedOut := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "src/flask/cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.46, Survivors: 13, MutantsTotal: 24, TimedOut: true, DevScored: true}},
	}, nil)
	if got := repoScanExitCode(allTimedOut, false, 0, nil, nil); got == 0 {
		t.Errorf("a scan whose every audited file timed out before the pool converged must exit non-zero, got %d", got)
	}
}

// TestRepoScanExitCodePartialTimeoutStillExitsZero proves the rule is
// scoped to "EVERY audited file timed out", not "any file timed out": a
// scan where most files converged cleanly and passed must not be failed
// just because one OTHER file's run happened to hit its deadline — that
// would be its own over-broad false alarm, not the silent-no-gate bug #3
// targets.
func TestRepoScanExitCodePartialTimeoutStillExitsZero(t *testing.T) {
	mixed := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "clean.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.9}},
		{Job: reposcan.Job{Path: "cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.46, TimedOut: true, DevScored: true}},
	}, nil)
	if got := repoScanExitCode(mixed, false, 0, nil, nil); got != 0 {
		t.Errorf("a scan where NOT every audited file timed out must keep today's exit-code logic, got %d", got)
	}
}

// TestRepoScanExitCodeDistinguishesEmptyScopeFromNothingGradable is the
// --diff-base half of the exit-code contract: with diff scoping, the most
// common PR in existence (docs-only, or touching only files with no paired
// test) legitimately has nothing in scope, and exits 0 — a true, honest
// answer. Zero GRADABLE out of a NON-empty scope stays exit 1: files were in
// scope and none could be graded, which is a real failure to report.
func TestRepoScanExitCodeDistinguishesEmptyScopeFromNothingGradable(t *testing.T) {
	emptyScope := reposcan.Aggregate("o", "r", "c", 0, 0, nil, nil)
	if got := repoScanExitCode(emptyScope, true, 0, nil, nil); got != 0 {
		t.Errorf("an empty diff scope must exit 0 (nothing to audit), got %d", got)
	}

	nothingGradable := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonBaselineFailed},
	}, nil)
	if got := repoScanExitCode(nothingGradable, false, 0, nil, nil); got != 1 {
		t.Errorf("a non-empty scope where nothing graded must exit 1, got %d", got)
	}
}

// TestRepoScanExitCodeMinKillRateUnsetIsExactlyTodaysBehaviour is the opt-in
// contract: a nil --min-kill-rate must not change the exit code at all, even
// for a file that would obviously breach any reasonable threshold (0.0). A
// default threshold would break every existing caller of a shipped command —
// so "flag absent" and "flag threshold 0.0" must NOT be the same thing.
func TestRepoScanExitCodeMinKillRateUnsetIsExactlyTodaysBehaviour(t *testing.T) {
	weak := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.0}},
	}, nil)
	if got := repoScanExitCode(weak, false, 0, nil, nil); got != 0 {
		t.Errorf("min-kill-rate unset (nil) must leave a graded 0.00 file exiting 0, got %d", got)
	}
}

// TestRepoScanExitCodeMinKillRateBreachIsNonZero proves the new teeth: any
// audited file scoring strictly below the threshold fails the whole scan,
// even when other files pass and the aggregate would look fine.
func TestRepoScanExitCodeMinKillRateBreachIsNonZero(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "strong.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1.0}},
		{Job: reposcan.Job{Path: "weak.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.4}},
	}, nil)
	threshold := 0.8
	if got := repoScanExitCode(rep, false, 0, &threshold, nil); got != 1 {
		t.Errorf("one file below --min-kill-rate must fail the whole scan, got %d (a well-tested file must not mask a weak one)", got)
	}
}

// TestRepoScanExitCodeMinKillRateAtThresholdPasses pins the boundary: the
// flag is a MINIMUM, inclusive, so a file exactly at the threshold passes.
func TestRepoScanExitCodeMinKillRateAtThresholdPasses(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.8}},
	}, nil)
	threshold := 0.8
	if got := repoScanExitCode(rep, false, 0, &threshold, nil); got != 0 {
		t.Errorf("a file exactly at --min-kill-rate must PASS (inclusive minimum), got %d", got)
	}
}

// TestRepoScanExitCodeMinKillRateAboveThresholdPasses is the mirror of the
// boundary test: comfortably above the threshold must not be flagged.
func TestRepoScanExitCodeMinKillRateAboveThresholdPasses(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.95}},
	}, nil)
	threshold := 0.8
	if got := repoScanExitCode(rep, false, 0, &threshold, nil); got != 0 {
		t.Errorf("a file above --min-kill-rate must pass, got %d", got)
	}
}

// TestRepoScanExitCodeNothingInScopeWinsOverMinKillRate proves the ordering
// requirement: nothingInScope must decide FIRST. RepoReport.KillRate (and
// every per-file rate) is undefined when nothing was ever in scope, so a
// threshold check must never be reachable there — the empty-scope branch
// must return 0 regardless of what minKillRate says.
func TestRepoScanExitCodeNothingInScopeWinsOverMinKillRate(t *testing.T) {
	emptyScope := reposcan.Aggregate("o", "r", "c", 0, 0, nil, nil)
	threshold := 0.99
	if got := repoScanExitCode(emptyScope, true, 0, &threshold, nil); got != 0 {
		t.Errorf("nothingInScope must win over minKillRate, got %d", got)
	}
}

// TestRepoScanExitCodeAuditedZeroWinsOverMinKillRate is the other half of the
// ordering requirement: RepoReport.KillRate is NaN when Audited == 0, and
// every comparison against NaN is false — so a threshold breach can never be
// the thing that reports this failure. The existing COULD-NOT-GRADE exit (1)
// must fire regardless of minKillRate, not be silently satisfied by NaN
// comparisons all evaluating false.
func TestRepoScanExitCodeAuditedZeroWinsOverMinKillRate(t *testing.T) {
	nothingGradable := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonBaselineFailed},
	}, nil)
	threshold := 0.0
	if got := repoScanExitCode(nothingGradable, false, 0, &threshold, nil); got != 1 {
		t.Errorf("Audited==0 must still exit 1 even with a permissive minKillRate, got %d", got)
	}
}

// TestLocalExecutorExecuteRedBaselineSkipsTheAudit is Finding 3: a suite that
// consistently FAILS unmutated is stable (the runs agree) but ungradable, and
// must not pay for mutant generation, critic calls and a third suite run to
// produce a verdict that would only be discarded.
func TestLocalExecutorExecuteRedBaselineSkipsTheAudit(t *testing.T) {
	audited := false
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{false, false}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			audited = true
			return advpool.Verdict{}, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/a.go", Goal: reposcan.Goal{Text: "g"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if audited {
		t.Error("a consistently-red baseline must not pay for a full LLM audit")
	}
	if res.Gradable {
		t.Error("a red baseline must not be gradable")
	}
	if res.Reason != reposcan.ReasonBaselineFailed {
		t.Errorf("Reason = %q, want %q", res.Reason, reposcan.ReasonBaselineFailed)
	}
}

// TestLocalExecutorExecuteRedBaselineModuleNotFoundHint pins the stranger's-
// path fix on the `--repo` scan path (the sibling of
// TestRenderAdvVerdictBaselineFailedModuleNotFoundHint, which covers the
// same fix on the `--local` renderer): a consistently red baseline whose own
// output shows ModuleNotFoundError during collection gets one extra progress
// line naming PEP-735 dependency groups as the likely cause.
func TestLocalExecutorExecuteRedBaselineModuleNotFoundHint(t *testing.T) {
	var buf bytes.Buffer
	ex := localExecutor{
		baselineRuns: 2,
		progress:     &buf,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{
				results: []bool{false, false},
				output:  "ImportError while importing test module 'tests/test_mod.py'.\nModuleNotFoundError: No module named 'pytest_cov'",
			}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			t.Fatal("a consistently red baseline must not pay for an audit")
			return advpool.Verdict{}, nil
		},
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/mod.py", Goal: reposcan.Goal{Text: "g"}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "PEP-735") {
		t.Errorf("progress output is missing the dependency-groups hint:\n%s", got)
	}
	if !strings.Contains(got, "pip install --group") {
		t.Errorf("progress output must name the fix:\n%s", got)
	}
}

// TestLocalExecutorExecuteRedBaselineNoHintWithoutImportError guards the
// negative on the same path: a baseline failure unrelated to imports must not
// print the dependency-groups hint.
func TestLocalExecutorExecuteRedBaselineNoHintWithoutImportError(t *testing.T) {
	var buf bytes.Buffer
	ex := localExecutor{
		baselineRuns: 2,
		progress:     &buf,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{
				results: []bool{false, false},
				output:  "AssertionError: expected 2 got 3",
			}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			t.Fatal("a consistently red baseline must not pay for an audit")
			return advpool.Verdict{}, nil
		},
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/mod.py", Goal: reposcan.Goal{Text: "g"}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "PEP-735") {
		t.Errorf("a non-import baseline failure must not print the dependency-groups hint:\n%s", got)
	}
}

// TestLocalExecutorExecuteLogsProvenMissed proves the per-file progress line
// carries ProvenMissed the same way it already carries DevKillRate and
// Survivors — corral's real audit converged with a verdict of
// "kill_rate 0.467, 16 survivors, proven" (see internal/reposcan/report.go's
// package doc for the pallets/flask/src/flask/cli.py case this responds to)
// and the operator watching progress scroll by never saw the pool's actual
// finding, only the raw survivor count.
func TestLocalExecutorExecuteLogsProvenMissed(t *testing.T) {
	var buf bytes.Buffer
	ex := localExecutor{
		baselineRuns: 2,
		progress:     &buf,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			return advpool.Verdict{DevKillRate: 0.467, Survivors: 16, MutantsTotal: 30, ProvenMissed: 7, DevScored: true}, nil
		},
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "src/flask/cli.py", Goal: reposcan.Goal{Text: "g"}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "7 proven missed") {
		t.Errorf("per-file progress line is missing proven_missed:\n%s", got)
	}
}

// TestLocalExecutorReleasesTheBaselineJail proves the cleanup runs on every
// path — including the early return for a red baseline, where a leak would
// strand a vendor staging dir per file.
func TestLocalExecutorReleasesTheBaselineJail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []bool
	}{
		{"red baseline", []bool{false, false}},
		{"flaky baseline", []bool{true, false}},
		{"green baseline", []bool{true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := 0
			ex := localExecutor{
				baselineRuns: 2,
				newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
					return &scriptedBaseline{results: tc.results}, func() { cleaned++ }, nil
				},
				audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
					return advpool.Verdict{DevKillRate: 1}, nil
				},
			}
			if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "a.go", Goal: reposcan.Goal{Text: "g"}}); err != nil {
				t.Fatal(err)
			}
			if cleaned != 1 {
				t.Errorf("cleanup ran %d times, want exactly 1", cleaned)
			}
		})
	}
}

// TestResolveAuditRolesWarningCarriesTheCommandPrefix pins the observable
// output of the shipped command: the shadow-model warning must still be
// prefixed "corral certify --local: " after the extraction moved it into a
// shared helper, and must name the calling mode when another one uses it.
func TestResolveAuditRolesWarningCarriesTheCommandPrefix(t *testing.T) {
	var errb bytes.Buffer
	// critic != writer keeps decorrelation happy; shadow == mutant fires the
	// warning, which is written before any credential check.
	_, _ = resolveAuditRoles(localAuditInput{
		writerModel: "w", criticModel: "c", mutantModel: "m", shadowModel: "m",
	}, &errb)
	if !strings.Contains(errb.String(), "corral certify --local: warning: --shadow-model") {
		t.Errorf("stderr = %q", errb.String())
	}

	errb.Reset()
	_, _ = resolveAuditRoles(localAuditInput{
		cmdName:     "corral certify --repo",
		writerModel: "w", criticModel: "c", mutantModel: "m", shadowModel: "m",
	}, &errb)
	if !strings.Contains(errb.String(), "corral certify --repo: warning: --shadow-model") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// TestCertifyRepoMissingGoalsFileFailsClosed proves an unreadable --goals
// file is a hard error, never an empty goal set that would silently report
// "nothing to audit" as a clean scan.
func TestCertifyRepoMissingGoalsFileFailsClosed(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", filepath.Join(root, "nope.json"), "--dry-run"}, &out, &errb)
	if code == 0 {
		t.Fatal("an unreadable --goals file must not exit 0")
	}
	if !strings.Contains(errb.String(), "--goals") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// scriptedBaseline returns a canned pass/fail per RunBaseline call — a
// flapping suite when the results disagree.
type scriptedBaseline struct {
	results []bool
	n       int
	// output, when set, is returned by BaselineOutput() — the runner's own
	// words on a failing baseline, the same optional interface
	// jailBaselineRunner satisfies.
	output string
}

func (s *scriptedBaseline) RunBaseline() (bool, error) {
	if s.n >= len(s.results) {
		return false, errors.New("scriptedBaseline: out of scripted results")
	}
	v := s.results[s.n]
	s.n++
	return v, nil
}

// BaselineOutput satisfies the same optional interface Execute's
// baseline-failure path type-asserts for (see jailBaselineRunner).
func (s *scriptedBaseline) BaselineOutput() string { return s.output }

func TestLocalExecutorFlakyBaselineIsUngradable(t *testing.T) {
	flaky := &scriptedBaseline{results: []bool{true, false}}
	stable, err := reposcan.CheckBaselineStable(flaky, 2)
	if err != nil {
		t.Fatal(err)
	}
	if stable {
		t.Fatal("flapping baseline reported stable")
	}
}

// TestLocalExecutorExecuteFlakyBaselineNotGraded is the wiring proof the unit
// test above cannot give: it exercises localExecutor.Execute itself, with the
// jail and the pool stubbed out, and asserts that a flapping baseline yields
// an ungradable result with ReasonFlakyBaseline AND that the audit was never
// run at all (a coin-flip score is not merely discarded — it is never taken).
func TestLocalExecutorExecuteFlakyBaselineNotGraded(t *testing.T) {
	audited := false
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, false}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			audited = true
			return advpool.Verdict{}, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/a.go", Goal: reposcan.Goal{Text: "g"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Gradable {
		t.Error("a flapping baseline must not be gradable")
	}
	if res.Reason != reposcan.ReasonFlakyBaseline {
		t.Errorf("Reason = %q, want %q", res.Reason, reposcan.ReasonFlakyBaseline)
	}
	if audited {
		t.Error("the audit ran despite an unstable baseline — the score would be a coin flip")
	}
}

// TestLocalExecutorExecuteStableBaselineIsGraded is the other half: a stable
// baseline DOES reach the audit and its verdict is graded.
func TestLocalExecutorExecuteStableBaselineIsGraded(t *testing.T) {
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
			if in.codePath != "pkg/a.go" || in.goal != "g" {
				return advpool.Verdict{}, errors.New("job fields did not reach the audit")
			}
			return advpool.Verdict{DevKillRate: 0.75}, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{
		Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "g"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Gradable || res.Verdict.DevKillRate != 0.75 {
		t.Fatalf("want a graded 0.75 verdict, got %+v", res)
	}
}

// TestLocalExecutorExecuteBaselineFailedIsUngradable proves honesty invariant
// 1 survives the adapter: a verdict whose baseline could not pass is reported
// as ungradable-with-reason, never as a real 0.0 kill rate.
//
// The baseline here is GREEN, so the pre-audit short-circuit does not fire —
// this exercises the verdict-level backstop specifically, for the case where
// the suite passes the standalone baseline run but fails inside the audit's
// own scoring workspace.
func TestLocalExecutorExecuteBaselineFailedIsUngradable(t *testing.T) {
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			return advpool.Verdict{BaselineFailed: true}, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/a.go", Goal: reposcan.Goal{Text: "g"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Gradable {
		t.Error("a failed baseline must not be gradable")
	}
	if res.Reason != reposcan.ReasonBaselineFailed {
		t.Errorf("Reason = %q, want %q", res.Reason, reposcan.ReasonBaselineFailed)
	}
}

// countingIsolator is a stand-in sandbox with a real identity (a pointer, so
// two of them are distinguishable — unlike sandbox.bwrapIsolator, an empty
// struct whose independently-resolved values always compare equal).
type countingIsolator struct{ label string }

func (c *countingIsolator) Name() string     { return c.label }
func (c *countingIsolator) Preflight() error { return nil }
func (c *countingIsolator) Wrap(command string, _ sandbox.Options, _ []string) ([]string, error) {
	return []string{"/bin/sh", "-c", command}, nil
}

// TestScanResolvesTheSandboxExactlyOnceForTheWholeScan proves the perf claim
// as a COUNT, which is the only way it can be proven: the scan resolves the
// backend once in newLocalExecutor and hands that isolator to every job via
// localAuditInput.iso, so prepareAuditJail must never re-probe per file. An
// identity assertion cannot detect a violation here — bwrapIsolator is an
// empty struct, so a per-file re-resolution yields a value that compares
// EQUAL to the scan's. So this counts every resolution in the command (both
// resolveLocalJail and resolveScanJail go through resolveJailFn) across three
// jobs and requires exactly one.
//
// Counting through the seam also removes the host dependency: the fake
// resolver succeeds with no bwrap, so this runs everywhere instead of
// skipping on hosts without a sandbox backend.
func TestScanResolvesTheSandboxExactlyOnceForTheWholeScan(t *testing.T) {
	var resolutions int
	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) {
		resolutions++
		return &countingIsolator{label: "fake"}, nil
	}
	t.Cleanup(func() { resolveJailFn = orig })

	repo := t.TempDir()
	for name, body := range map[string]string{
		"a.go":      "package p\n\nfunc A() int { return 1 }\n",
		"a_test.go": "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ex := newLocalExecutor(repo, nil, "", 0, io.Discard)
	defer ex.Close()
	if ex.jailErr != nil {
		t.Fatalf("construction must resolve through the seam: %v", ex.jailErr)
	}
	if resolutions != 1 {
		t.Fatalf("construction resolved the sandbox %d time(s), want 1", resolutions)
	}

	plug, ok := lang.Detect("a.go")
	if !ok {
		t.Fatal("no go plugin")
	}
	// Both seams drive the REAL prepareAuditJail with the input Execute built,
	// which is where the per-file re-resolution would happen. Its outcome is
	// irrelevant here (the fake isolator cannot actually run a suite) — the
	// measurement is how many times the sandbox got resolved.
	drive := func(in localAuditInput) {
		if p, err := prepareAuditJail(context.Background(), in, plug, time.Minute, io.Discard); err == nil {
			p.cleanup()
		}
	}
	ex.newBaseline = func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
		drive(in)
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
		drive(in)
		return advpool.Verdict{DevKillRate: 1, MutantsTotal: 1}, nil
	}

	const jobs = 3
	for i := 0; i < jobs; i++ {
		if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"}); err != nil {
			t.Fatalf("Execute %d: %v", i, err)
		}
	}
	if resolutions != 1 {
		t.Errorf("sandbox resolved %d time(s) across %d job(s), want exactly 1 — the scan is re-probing the backend per file", resolutions, jobs)
	}
}

// TestPrintRepoReportNothingAuditedSaysSo proves the never-fabricate-a-score
// invariant at the presentation layer: with nothing audited the report must
// say COULD-NOT-GRADE, not print a 0.00 kill rate (or a NaN).
func TestPrintRepoReportNothingAuditedSaysSo(t *testing.T) {
	var out bytes.Buffer
	rep := reposcan.Aggregate("local", "r", "c", 3, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonFlakyBaseline},
	}, []reposcan.Exclusion{{Path: "b.go", Reason: reposcan.ReasonNoPairedTest}})
	printRepoReport(&out, rep, false, nil, nil, nil, time.Time{})
	s := out.String()
	if !strings.Contains(s, "COULD-NOT-GRADE") {
		t.Errorf("want COULD-NOT-GRADE, got:\n%s", s)
	}
	if strings.Contains(s, "kill rate") {
		t.Errorf("a report with nothing audited must not print a kill rate:\n%s", s)
	}
	if !strings.Contains(s, reposcan.ReasonFlakyBaseline) {
		t.Errorf("ungradable reasons must be reported:\n%s", s)
	}
}

// TestPrintRepoReportEmptyScopeSaysADifferentLineThanCouldNotGrade proves the
// two zero-audited outcomes are not conflated in the human-readable output:
// an empty diff scope (nothing was ever in bound) must not print the same
// line as a non-empty scope that failed to grade anything.
func TestPrintRepoReportEmptyScopeSaysADifferentLineThanCouldNotGrade(t *testing.T) {
	rep := reposcan.Aggregate("local", "r", "c", 0, 0, nil, nil)

	var scoped bytes.Buffer
	printRepoReport(&scoped, rep, true, nil, nil, nil, time.Time{})
	if strings.Contains(scoped.String(), "COULD-NOT-GRADE") {
		t.Errorf("an empty diff scope must not print COULD-NOT-GRADE:\n%s", scoped.String())
	}
	if !strings.Contains(scoped.String(), "NOTHING IN SCOPE") {
		t.Errorf("want a distinct NOTHING IN SCOPE line, got:\n%s", scoped.String())
	}

	var notScoped bytes.Buffer
	printRepoReport(&notScoped, rep, false, nil, nil, nil, time.Time{})
	if strings.Contains(notScoped.String(), "NOTHING IN SCOPE") {
		t.Errorf("the non-diff/nothing-gradable case must not print the scope line:\n%s", notScoped.String())
	}
	if !strings.Contains(notScoped.String(), "COULD-NOT-GRADE") {
		t.Errorf("want COULD-NOT-GRADE, got:\n%s", notScoped.String())
	}
}

// TestPrintRepoReportMinKillRateBreachIsLabelledDistinctlyFromCouldNotGrade
// proves the human-readable requirement: an operator reading the report must
// be able to see which file(s) breached --min-kill-rate and by how much, on
// a line that is NOT the COULD-NOT-GRADE line (that line means something
// different: nothing was measured at all, not "it was measured and failed").
func TestPrintRepoReportMinKillRateBreachIsLabelledDistinctlyFromCouldNotGrade(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "strong.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1.0}},
		{Job: reposcan.Job{Path: "weak.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.4}},
	}, nil)
	threshold := 0.8
	var out bytes.Buffer
	printRepoReport(&out, rep, false, &threshold, nil, nil, time.Time{})
	s := out.String()
	if !strings.Contains(s, "KILL-RATE BREACH") {
		t.Errorf("want a distinct breach line, got:\n%s", s)
	}
	if !strings.Contains(s, "weak.go") {
		t.Errorf("want the breaching file named, got:\n%s", s)
	}
	if strings.Contains(s, "COULD-NOT-GRADE") {
		t.Errorf("a threshold breach must not print the could-not-grade line:\n%s", s)
	}
	breachSection := s[strings.Index(s, "KILL-RATE BREACH"):]
	if strings.Contains(breachSection, "strong.go") {
		t.Errorf("a file that passed the threshold must not be listed under the breach line:\n%s", s)
	}
}

// TestPrintRepoReportMinKillRateNoBreachPrintsNoBreachLine proves the report
// stays silent about the threshold when nothing breached it — the line is
// meant to be a call to action, not noise on every green run.
func TestPrintRepoReportMinKillRateNoBreachPrintsNoBreachLine(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.9}},
	}, nil)
	threshold := 0.8
	var out bytes.Buffer
	printRepoReport(&out, rep, false, &threshold, nil, nil, time.Time{})
	if strings.Contains(out.String(), "KILL-RATE BREACH") {
		t.Errorf("no file breached the threshold; the breach line must not print:\n%s", out.String())
	}
}

// TestPrintRepoReportWeakestIsCapped proves the weakest-files list is capped
// with an honest "... and N more" rather than dumping every file.
func TestPrintRepoReportWeakestIsCapped(t *testing.T) {
	var results []reposcan.FileResult
	for i := 0; i < 12; i++ {
		results = append(results, reposcan.FileResult{
			Job:      reposcan.Job{Path: string(rune('a'+i)) + ".go"},
			Gradable: true,
			Verdict:  advpool.Verdict{DevKillRate: float64(i) / 100},
		})
	}
	var out bytes.Buffer
	printRepoReport(&out, reposcan.Aggregate("o", "r", "c", 12, len(results), results, nil), false, nil, nil, nil, time.Time{})
	s := out.String()
	if !strings.Contains(s, "... and 2 more") {
		t.Errorf("want the weakest list capped at 10 with a remainder line:\n%s", s)
	}
	if !strings.Contains(s, "kill rate") {
		t.Errorf("want a kill rate over the audited surface:\n%s", s)
	}
}

// TestRunCertifyDispatchesRepoScanOnGoals proves the subcommand wiring: a
// --goals invocation reaches runCertifyRepo (which alone knows --dry-run),
// and it never runs the check command or posts to a brain.
func TestRunCertifyDispatchesRepoScanOnGoals(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	code := runCertify([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"},
		run, post, fakeJail{exit: 0, out: "ok"},
		func() (ed25519.PrivateKey, error) { return nil, errors.New("unused") },
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "corral certify --repo") {
		t.Errorf("--goals did not reach the repo scan:\n%s", stdout.String())
	}
	if post.called || run.ranArgv != nil {
		t.Error("the repo scan must not post to a brain or run a check command")
	}
}

// TestRunCertifyLegacyRepoFlagIsNotHijacked is the regression guard for the
// flag collision: --repo has meant "the repository this record is ABOUT" on
// the brain/standalone paths since long before the scan existed. A plain
// `certify --repo <name> -- <cmd>` must still take the standalone path.
func TestRunCertifyLegacyRepoFlagIsNotHijacked(t *testing.T) {
	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	_ = runCertify([]string{"--repo", "pdbethke/corralai", "--commit", "y", "--", "true"},
		run, post, fakeJail{exit: 0, out: "ok"},
		func() (ed25519.PrivateKey, error) { return nil, errors.New("no signing key configured for this test") },
		&stdout, &stderr)
	if strings.Contains(stdout.String(), "corral certify --repo ") {
		t.Errorf("legacy --repo was hijacked by the repo scan:\n%s", stdout.String())
	}
}

// TestResolveAuditRolesRejectsCollapsedDecorrelation covers the shared role
// preflight the repo scan runs once before fanning out: a critic on the same
// model as the writer is a judge in her own cause, and must be a USAGE error
// (exit 2), not an internal failure.
func TestResolveAuditRolesRejectsCollapsedDecorrelation(t *testing.T) {
	_, err := resolveAuditRoles(localAuditInput{
		writerModel: "same-model", criticModel: "same-model",
		mutantModel: "same-model", shadowModel: "off",
	}, io.Discard)
	if err == nil {
		t.Fatal("critic == writer must not be accepted")
	}
	if !isAuditUsageError(err) {
		t.Errorf("want a usage error (exit 2), got %v", err)
	}
}

// TestCertifyRepoRefusesExplicitCheckCommandAcrossLanguages: an explicit
// `-- <cmd>` is applied to EVERY job, so in a mixed repo `go test ./...` would
// "grade" a mutated .py file — green on the baseline, green on every mutant,
// no error anywhere, and a confident 0.00 kill rate in the report. That is the
// never-fabricate-a-score invariant failing through the one path the invariant
// machinery does not watch, so the scan refuses instead.
func TestCertifyRepoRefusesExplicitCheckCommandAcrossLanguages(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "py", "m.py"), "x = 1\n")
	mustWrite(t, filepath.Join(root, "py", "test_m.py"), "x = 1\n")
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic", "py/m.py": "must not divide by zero"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run", "--", "go", "test", "./..."}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage error); stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	s := errb.String()
	for _, want := range []string{"spans 2 languages", "go, python"} {
		if !strings.Contains(s, want) {
			t.Errorf("error message missing %q:\n%s", want, s)
		}
	}
}

// The same explicit command is FINE when every job speaks one language — the
// refusal above must not break the single-language case.
func TestCertifyRepoAllowsExplicitCheckCommandForOneLanguage(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "py", "m.py"), "x = 1\n") // no goal: never a job
	mustWrite(t, filepath.Join(root, "py", "test_m.py"), "x = 1\n")
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run", "--", "go", "test", "./..."}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr=%s", code, errb.String())
	}
}

// TestCertifyRepoReportsEnumeratedCandidatesNotJobs is Finding I2 at the CLI:
// the score line's "% of N candidates" must be over the ENUMERATED candidates,
// so ungoaled files cannot be hidden from the coverage ratio.
func TestCertifyRepoReportsEnumeratedCandidatesNotJobs(t *testing.T) {
	excl := []reposcan.Exclusion{
		{Path: "b.go", Reason: reposcan.ReasonUngoaled},
		{Path: "c.go", Reason: reposcan.ReasonUngoaled},
		{Path: "d.go", Reason: reposcan.ReasonUngoaled},
		{Path: "e.go", Reason: reposcan.ReasonUngoaled},
	}
	rep := reposcan.Aggregate("o", "r", "c", 12, 5, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.8}},
	}, excl)
	var out bytes.Buffer
	printRepoReport(&out, rep, false, nil, nil, nil, time.Time{})
	s := out.String()
	if !strings.Contains(s, "20% of 5 candidates") {
		t.Errorf("want the ratio over the 5 enumerated candidates, got:\n%s", s)
	}
	if !strings.Contains(s, "ungradable: 4 ("+reposcan.ReasonUngoaled+")") {
		t.Errorf("ungoaled candidates must be accounted by reason:\n%s", s)
	}
}

// TestPrintRepoReportUngradableOrderIsStable: map iteration order is random,
// and a report a later slice signs and anchors has to be byte-reproducible.
func TestPrintRepoReportUngradableOrderIsStable(t *testing.T) {
	excl := []reposcan.Exclusion{
		{Path: "f.go", Reason: reposcan.ReasonUngoaled},
	}
	rep := reposcan.Aggregate("o", "r", "c", 9, 6, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1}},
		{Job: reposcan.Job{Path: "b.go"}, Gradable: false, Reason: reposcan.ReasonFlakyBaseline},
		{Job: reposcan.Job{Path: "c.go"}, Gradable: false, Reason: reposcan.ReasonBaselineFailed},
		{Job: reposcan.Job{Path: "d.go"}, Gradable: false, Reason: reposcan.ReasonExecutorError},
		{Job: reposcan.Job{Path: "e.go"}, Gradable: false, Reason: reposcan.ReasonCancelled},
	}, excl)

	var first bytes.Buffer
	printRepoReport(&first, rep, false, nil, nil, nil, time.Time{})
	for i := 0; i < 50; i++ {
		var again bytes.Buffer
		printRepoReport(&again, rep, false, nil, nil, nil, time.Time{})
		if again.String() != first.String() {
			t.Fatalf("report is not reproducible:\n--- run 1 ---\n%s\n--- run %d ---\n%s", first.String(), i+2, again.String())
		}
	}
	// And the order is the sorted one, not merely stable by luck.
	s := first.String()
	if a, b := strings.Index(s, reposcan.ReasonBaselineFailed), strings.Index(s, reposcan.ReasonCancelled); a > b {
		t.Errorf("ungradable reasons are not sorted:\n%s", s)
	}
}

// TestPrintRepoReportPrintsExecutorErrorDetail is the fix for the swallowed
// executor error: `ungradable: 1 (executor-error)` alone gave no usable
// diagnosis (see the flask preflight bug this fixes). The report must print
// the underlying error text, not just the count.
func TestPrintRepoReportPrintsExecutorErrorDetail(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "app.py"}, Gradable: false, Reason: reposcan.ReasonExecutorError,
			Detail: "python toolchain unavailable — refusing to grade: pytest not importable via \".venv/bin/python\""},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "app.py: python toolchain unavailable") {
		t.Errorf("report does not surface the executor's error detail:\n%s", got)
	}
}

// TestPrintRepoReportMarksTimedOutFiles proves the report never lets a file
// scored under an unconverged run (advpool.Verdict.TimedOut, banked by
// driveLocalRun's bankableTimeoutVerdict) read like a clean audit: it must
// carry a distinct marker in the weakest-files line AND a headline caveat
// count, so a reader scanning "kill rate X% over N audited files" cannot
// miss that some of that N didn't actually finish.
func TestPrintRepoReportMarksTimedOutFiles(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "clean.go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.9, Survivors: 1, MutantsTotal: 10}},
		{Job: reposcan.Job{Path: "src/flask/cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.46, Survivors: 13, MutantsTotal: 24, TimedOut: true, DevScored: true}},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "1 of the audited file(s) scored under an UNCONVERGED run") {
		t.Errorf("report is missing the headline timed-out caveat:\n%s", got)
	}
	if !strings.Contains(got, "src/flask/cli.py") || !strings.Contains(got, "[TIMED OUT") {
		t.Errorf("report does not mark src/flask/cli.py as timed out:\n%s", got)
	}
	// The clean file's line must NOT carry the marker.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "clean.go") && strings.Contains(line, "TIMED OUT") {
			t.Errorf("clean.go's line wrongly carries the TIMED OUT marker: %q", line)
		}
	}
}

// TestPrintRepoReportSaysDidNotFinishWhenEveryFileTimedOut pins the report
// side of review item 3: when repoScanExitCode fails the scan because every
// audited file timed out, the printed report must say so in the same terms
// — not just leave an operator to infer "exit 1" means "did not finish"
// from a bare kill-rate line and a per-file marker.
func TestPrintRepoReportSaysDidNotFinishWhenEveryFileTimedOut(t *testing.T) {
	allTimedOut := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "src/flask/cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.46, Survivors: 13, MutantsTotal: 24, TimedOut: true, DevScored: true}},
	}, nil)
	var buf bytes.Buffer
	printRepoReport(&buf, allTimedOut, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "DID NOT FINISH") {
		t.Errorf("report must say DID NOT FINISH when every audited file timed out:\n%s", got)
	}

	// The mirror: a PARTIAL timeout (some files converged) must not print
	// the all-timed-out line, even though it does print the per-file
	// caveat.
	mixed := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "clean.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.9}},
		{Job: reposcan.Job{Path: "cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.46, TimedOut: true, DevScored: true}},
	}, nil)
	buf.Reset()
	printRepoReport(&buf, mixed, false, nil, nil, nil, time.Time{})
	got = buf.String()
	if strings.Contains(got, "DID NOT FINISH") {
		t.Errorf("a partial timeout (some files converged) must not print DID NOT FINISH:\n%s", got)
	}
	if !strings.Contains(got, "1 of the audited file(s) scored under an UNCONVERGED run") {
		t.Errorf("a partial timeout must still print the per-count caveat:\n%s", got)
	}
}

// TestPrintRepoReportMarksTestWriterFailedFiles mirrors
// TestPrintRepoReportMarksTimedOutFiles for the other converged-but-unproven
// state: the pool found survivor(s) but exhausted its compile-retry budget
// before it could author a killing test for any of them
// (advpool.Verdict.TestWriterFailed). proven_missed reads 0 for that file,
// which must not be printable as "clean" — the exact real-world case this
// whole fix responds to (pallets/flask's src/flask/cli.py: 19 survivors, no
// authored test, because `import cli` cannot resolve inside a real package).
func TestPrintRepoReportMarksTestWriterFailedFiles(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "clean.go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.9, Survivors: 1, MutantsTotal: 10}},
		{Job: reposcan.Job{Path: "src/flask/cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.457, Survivors: 19, MutantsTotal: 35, TestWriterFailed: true, DevScored: true}},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "1 of the audited file(s) had survivor(s) the pool could not author a compiling test to kill") {
		t.Errorf("report is missing the headline test-writer-failed caveat:\n%s", got)
	}
	if !strings.Contains(got, "src/flask/cli.py") || !strings.Contains(got, "[WRITER FAILED") {
		t.Errorf("report does not mark src/flask/cli.py as writer-failed:\n%s", got)
	}
	// The clean file's line must NOT carry the marker.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "clean.go") && strings.Contains(line, "WRITER FAILED") {
			t.Errorf("clean.go's line wrongly carries the WRITER FAILED marker: %q", line)
		}
	}
}

// TestPrintRepoReportMarksPoolTestUnsoundFiles proves the F2 fix's new
// diagnosis (advpool.Verdict.PoolTestUnsound: a compiling authored test that
// never genuinely graded) reaches the printed report distinctly from
// [WRITER FAILED] — a compiling test WAS produced here, so reusing that
// marker's wording would misdescribe the file.
func TestPrintRepoReportMarksPoolTestUnsoundFiles(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "clean.go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.9, Survivors: 1, MutantsTotal: 10}},
		{Job: reposcan.Job{Path: "unsound.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.457, Survivors: 19, MutantsTotal: 35, PoolTestUnsound: true, DevScored: true}},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "never genuinely graded") {
		t.Errorf("report is missing the headline pool-test-unsound caveat:\n%s", got)
	}
	if !strings.Contains(got, "unsound.py") || !strings.Contains(got, "TEST UNSOUND") {
		t.Errorf("report does not mark unsound.py as test-unsound:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "unsound.py") && strings.Contains(line, "WRITER FAILED") {
			t.Errorf("unsound.py must NOT carry the WRITER FAILED marker — its test compiled: %q", line)
		}
		if strings.Contains(line, "clean.go") && strings.Contains(line, "TEST UNSOUND") {
			t.Errorf("clean.go's line wrongly carries the TEST UNSOUND marker: %q", line)
		}
	}
}

// TestPrintRepoReportZeroLineNamesAllThreeReasons is F5's regression test:
// the repo-level "0 proven gaps" line must name every reason a per-file 0
// can occur, including "timed out" — a report with a timed-out file and zero
// ProvenMissed used to omit that reason entirely, leaving a reader unable to
// map the marker below back to a cause named above.
func TestPrintRepoReportZeroLineNamesAllThreeReasons(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "stalled.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, Survivors: 3, MutantsTotal: 10, TimedOut: true, DevScored: true}},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	var zeroLine string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "0 proven gaps") {
			zeroLine = line
		}
	}
	if zeroLine == "" {
		t.Fatalf("no '0 proven gaps' line found:\n%s", got)
	}
	if !strings.Contains(zeroLine, "timed out") {
		t.Errorf("the 0-proven-gaps line's own reason list must name \"timed out\" (not just elsewhere in the report):\n%q", zeroLine)
	}
}

// TestPrintRepoReportProvenMissedFileSurvivesTruncation is F4's regression
// test: the repo-level line promises "see weakest files below," but the
// per-file listing truncates at 10 entries in weakest-first (ascending
// kill-rate) order — a file with a real ProvenMissed can have a HIGH kill
// rate and land past the cutoff, silently breaking that promise. 12 audited
// files: 11 clean/weak ones plus one with a high kill rate and a proven
// gap, sorted to the very end.
func TestPrintRepoReportProvenMissedFileSurvivesTruncation(t *testing.T) {
	var results []reposcan.FileResult
	for i := 0; i < 11; i++ {
		results = append(results, reposcan.FileResult{
			Job: reposcan.Job{Path: fmt.Sprintf("weak%02d.go", i)}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.1, Survivors: 5, MutantsTotal: 10, DevScored: true},
		})
	}
	results = append(results, reposcan.FileResult{
		Job: reposcan.Job{Path: "high_kill_rate_proven.py"}, Gradable: true,
		Verdict: advpool.Verdict{DevKillRate: 0.95, Survivors: 2, MutantsTotal: 40, ProvenMissed: 2, DevScored: true},
	})
	rep := reposcan.Aggregate("o", "r", "c", 12, 12, results, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "... and 2 more") {
		t.Fatalf("fixture must actually truncate (12 weakest, cap 10) — test setup assumption broken:\n%s", got)
	}
	if !strings.Contains(got, "high_kill_rate_proven.py") {
		t.Errorf("the ONE file with a real proven gap must never be silently hidden behind the truncation:\n%s", got)
	}
}

// TestPrintRepoReportShowsProvenMissed is the real pallets/flask case this
// whole gap-closing change responds to: a run converges cleanly, the pool's
// authored test kills some of the survivors, and that — corral's strongest
// claim, an execution-demonstrated bug the dev suite misses — must actually
// reach the printed report. Before this fix, ProvenMissed was computed and
// discarded: it appeared nowhere in printRepoReport's output.
func TestPrintRepoReportShowsProvenMissed(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "src/flask/cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.467, Survivors: 16, MutantsTotal: 30, ProvenMissed: 7, DevScored: true}},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()

	if !strings.Contains(got, "7 proven") {
		t.Errorf("report is missing the repo-level proven_missed rollup:\n%s", got)
	}
	if !strings.Contains(got, "src/flask/cli.py") || !strings.Contains(got, "7 proven missed") {
		t.Errorf("report does not show cli.py's per-file proven_missed count:\n%s", got)
	}
}

// TestPrintRepoReportZeroProvenMissedIsLegible pins the honesty requirement
// at the heart of this change: ProvenMissed==0 is ambiguous on its own (see
// WeakFile.ProvenMissed's doc for the three cases), so a reader must be able
// to tell, from the printed report alone, which of the three they are
// looking at — never a bare "0" that could read as reassurance.
func TestPrintRepoReportZeroProvenMissedIsLegible(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 3, 3, []reposcan.FileResult{
		// Case 1: no survivors at all — the writer never ran (moot).
		{Job: reposcan.Job{Path: "clean.go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 1.0, Survivors: 0, MutantsTotal: 5, ProvenMissed: 0, DevScored: true}},
		// Case 2: survivors, but the writer failed to author a compiling test.
		{Job: reposcan.Job{Path: "writer_failed.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, Survivors: 5, MutantsTotal: 10, ProvenMissed: 0, TestWriterFailed: true, DevScored: true}},
		// Case 3: writer ran, authored a compiling test, proved nothing.
		{Job: reposcan.Job{Path: "tried_and_missed.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.6, Survivors: 4, MutantsTotal: 10, ProvenMissed: 0, DevScored: true}},
	}, nil)

	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	lines := strings.Split(got, "\n")

	lineFor := func(path string) string {
		for _, l := range lines {
			if strings.Contains(l, path) {
				return l
			}
		}
		t.Fatalf("no line found for %s in:\n%s", path, got)
		return ""
	}

	// Case 1 (moot): the survivor count itself (0) is what makes "nothing
	// proven" legible — no proven-missed detail is needed or printed.
	clean := lineFor("clean.go")
	if strings.Contains(clean, "proven missed") {
		t.Errorf("clean.go (0 survivors, nothing to prove) should not print a proven-missed count at all: %q", clean)
	}

	// Case 2 (writer failed): the existing [WRITER FAILED] marker is what
	// makes the 0 legible — it must still be present.
	wf := lineFor("writer_failed.py")
	if !strings.Contains(wf, "WRITER FAILED") {
		t.Errorf("writer_failed.py must carry the WRITER FAILED marker so its 0 proven_missed cannot be misread as clean: %q", wf)
	}

	// Case 3 (tried and missed): survivors > 0, no WRITER FAILED marker —
	// the explicit "0 proven missed" on this line is what distinguishes it
	// from case 2, and must be present.
	tm := lineFor("tried_and_missed.py")
	if !strings.Contains(tm, "0 proven missed") {
		t.Errorf("tried_and_missed.py must explicitly print 0 proven missed (a real, demonstrated 'nothing proven' result): %q", tm)
	}
	if strings.Contains(tm, "WRITER FAILED") {
		t.Errorf("tried_and_missed.py's writer did not fail — must not carry the WRITER FAILED marker: %q", tm)
	}
}

// Finding I6, still guarded: `--repo <dir>` used to fall through to the record
// path, which certified the CURRENT directory while stamping the other repo's
// path onto the record — a signed statement about the wrong subject. It was
// fixed by refusing (goals were mandatory then); now that goals are derived,
// it is fixed by RUNNING the scan the operator asked for. Either way the
// record path must never see it.
func TestRunCertifyRepoDirWithoutGoalsGoesToTheScan(t *testing.T) {
	root := t.TempDir()
	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	code := runCertify([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run", "--", "true"},
		run, post, fakeJail{exit: 0, out: "ok"},
		func() (ed25519.PrivateKey, error) { return nil, errors.New("unused") },
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	// THE invariant: the record path must never run, because it would certify
	// the CURRENT directory while stamping root onto the record as its subject.
	if run.ranArgv != nil || post.called {
		t.Error("--repo <dir> must not run the check or post a record")
	}
	if !strings.Contains(stdout.String(), "corral certify --repo "+root) {
		t.Errorf("--repo <dir> did not reach the scan:\n%s", stdout.String())
	}
}

// The same dispatch must also recognise the --repo=<dir> spelling.
func TestRunCertifyRepoDirEqualsFormGoesToTheScan(t *testing.T) {
	root := t.TempDir()
	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	code := runCertify([]string{"--repo=" + root, "--dry-run", "--", "true"},
		run, post, fakeJail{exit: 0, out: "ok"},
		func() (ed25519.PrivateKey, error) { return nil, errors.New("unused") },
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	if run.ranArgv != nil || post.called {
		t.Error("--repo=<dir> must not run the check or post a record")
	}
	if !strings.Contains(stdout.String(), "corral certify --repo "+root) {
		t.Errorf("--repo=<dir> did not reach the scan:\n%s", stdout.String())
	}
}

// TestLocalExecutorSharesOneSeedAcrossJobs is the point of the shared-seed
// cache: jail preparation (a tree copy + `go mod vendor` + a full tree walk)
// depends only on the repo and the language, so it must happen ONCE for a whole
// scan — not twice per audited file, which on a 189-file repo meant 378 tree
// copies and 378 vendor runs, up to NumCPU concurrently.
func TestLocalExecutorSharesOneSeedAcrossJobs(t *testing.T) {
	var builds atomic.Int32
	ex := newLocalExecutor(t.TempDir(), nil, "", 0, io.Discard)
	ex.seeds = newSeedCache(func(lang string) (repoSeed, error) {
		builds.Add(1)
		return repoSeed{seedDir: "/seed/" + lang, files: map[string]string{}, cleanup: func() {}}, nil
	})
	defer ex.Close()

	// Stub both jail seams so no bwrap is needed.
	ex.newBaseline = func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(context.Context, localAuditInput) (advpool.Verdict, error) {
		return advpool.Verdict{DevKillRate: 1, MutantsTotal: 1}, nil
	}

	for i := 0; i < 8; i++ {
		if _, err := ex.Execute(context.Background(), reposcan.Job{
			Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "g"},
		}); err != nil {
			t.Fatalf("Execute %d: %v", i, err)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("seed built %d times across 8 jobs of one language, want 1", got)
	}
}

// The seed the cache built must actually REACH both jail seams — a cache
// consulted but never threaded through would still prepare twice per file
// inside prepareAuditJail.
func TestLocalExecutorPassesTheSharedSeedToBothSeams(t *testing.T) {
	want := repoSeed{seedDir: "/seed/go", files: map[string]string{"a.go": "package a\n"}, cleanup: func() {}}
	ex := newLocalExecutor(t.TempDir(), nil, "", 0, io.Discard)
	ex.seeds = newSeedCache(func(string) (repoSeed, error) { return want, nil })
	defer ex.Close()

	var baselineSeed, auditSeed *repoSeed
	ex.newBaseline = func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
		baselineSeed = in.seed
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
		auditSeed = in.seed
		return advpool.Verdict{DevKillRate: 1}, nil
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{
		Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "g"},
	}); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*repoSeed{"baseline": baselineSeed, "audit": auditSeed} {
		if got == nil {
			t.Fatalf("%s seam got no shared seed — it would prepare its own", name)
		}
		if got.seedDir != want.seedDir {
			t.Errorf("%s seam got seedDir %q, want %q", name, got.seedDir, want.seedDir)
		}
	}
}

// A language whose prep failed is ungradable WITH ITS REASON, never a
// fabricated score — and the cached failure means it is not retried per file.
func TestLocalExecutorPrepFailureIsUngradable(t *testing.T) {
	var builds atomic.Int32
	audited := false
	ex := newLocalExecutor(t.TempDir(), nil, "", 0, io.Discard)
	ex.seeds = newSeedCache(func(string) (repoSeed, error) {
		builds.Add(1)
		return repoSeed{cleanup: func() {}}, errors.New("go mod vendor failed")
	})
	defer ex.Close()
	ex.newBaseline = func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
		t.Error("a failed prep must not reach the baseline jail")
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(context.Context, localAuditInput) (advpool.Verdict, error) {
		audited = true
		return advpool.Verdict{}, nil
	}
	for i := 0; i < 3; i++ {
		res, err := ex.Execute(context.Background(), reposcan.Job{
			Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "g"},
		})
		if err != nil {
			t.Fatalf("Execute %d: %v", i, err)
		}
		if res.Gradable {
			t.Error("a file whose prep failed must not be gradable")
		}
		if res.Reason != reposcan.ReasonPrepFailed {
			t.Errorf("Reason = %q, want %q", res.Reason, reposcan.ReasonPrepFailed)
		}
	}
	if audited {
		t.Error("a failed prep must not pay for an LLM audit")
	}
	if got := builds.Load(); got != 1 {
		t.Errorf("prep retried %d times; a cached failure must be attempted once", got)
	}
}

func TestLocalExecutorCloseReleasesSeeds(t *testing.T) {
	var cleaned atomic.Int32
	ex := newLocalExecutor(t.TempDir(), nil, "", 0, io.Discard)
	ex.seeds = newSeedCache(func(lang string) (repoSeed, error) {
		return repoSeed{seedDir: lang, files: map[string]string{}, cleanup: func() { cleaned.Add(1) }}, nil
	})
	ex.newBaseline = func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(context.Context, localAuditInput) (advpool.Verdict, error) {
		return advpool.Verdict{DevKillRate: 1, MutantsTotal: 1}, nil
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{
		Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "g"},
	}); err != nil {
		t.Fatal(err)
	}
	ex.Close()
	if got := cleaned.Load(); got != 1 {
		t.Fatalf("cleanup ran %d times after Close, want 1", got)
	}
	// Idempotent: the driver defers it, and a later explicit call must not
	// release a staging dir twice.
	ex.Close()
	if got := cleaned.Load(); got != 1 {
		t.Fatalf("cleanup ran %d times after a second Close, want 1", got)
	}
}

// The whole point of this slice: newLocalExecutor must WIRE a seed cache.
// Every other executor test overwrites ex.seeds with a stub, so without this
// one, deleting `l.seeds = newSeedCache(...)` from the constructor breaks no
// test — and Execute's `if l.seeds != nil` guard turns that deletion into a
// silent regression to per-file jail prep (2 tree copies + 2 `go mod vendor`
// runs per audited file), the exact bug this branch exists to remove.
func TestNewLocalExecutorWiresASeedCache(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), nil, "", 0, io.Discard)
	defer ex.Close()
	if ex.seeds == nil {
		t.Fatal("no seed cache: every job would prepare its own jail")
	}
}

// The cache must be wired even when the host cannot sandbox: jailErr is
// reported by preflight, and a nil cache would silently change the fan-out's
// prep strategy rather than fail.
func TestNewLocalExecutorWiresASeedCacheEvenWhenTheJailIsUnavailable(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), nil, "", 0, io.Discard)
	defer ex.Close()
	if ex.jailErr == nil {
		t.Skip("this host has a working sandbox; the jail-unavailable path is covered on hosts without one")
	}
	if ex.seeds == nil {
		t.Fatal("no seed cache on the jail-error path")
	}
	if _, err := ex.seeds.get("go"); err == nil {
		t.Fatal("seed build must fail closed when the sandbox could not be resolved")
	}
}

// TestLocalExecutorExecuteSuiteIgnoresFileIsUngradable proves the adapter maps
// the canary's own diagnosis to its own reason. The baseline here is GREEN —
// that is the whole point: a suite that passes both its baseline and
// deliberately invalid source is not broken, it is pointed somewhere else, and
// reporting it as baseline-failed would send an operator to debug their build.
func TestLocalExecutorExecuteSuiteIgnoresFileIsUngradable(t *testing.T) {
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			return advpool.Verdict{SuiteIgnoresFile: true}, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/a.go", Goal: reposcan.Goal{Text: "g"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Gradable {
		t.Error("a suite that never reads the file must not be gradable")
	}
	if res.Reason != reposcan.ReasonSuiteIgnoresFile {
		t.Errorf("Reason = %q, want %q", res.Reason, reposcan.ReasonSuiteIgnoresFile)
	}
}

// TestLocalExecutorSuiteIgnoresFileBeatsBaselineFailed pins the ORDER: a
// verdict carrying both flags is reported as the more specific diagnosis.
func TestLocalExecutorSuiteIgnoresFileBeatsBaselineFailed(t *testing.T) {
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			return advpool.Verdict{SuiteIgnoresFile: true, BaselineFailed: true}, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/a.go", Goal: reposcan.Goal{Text: "g"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Reason != reposcan.ReasonSuiteIgnoresFile {
		t.Errorf("Reason = %q, want %q", res.Reason, reposcan.ReasonSuiteIgnoresFile)
	}
}

// TestCertifyRepoDryRunRanksSelectsAndAccounts proves the bound is applied
// BEFORE derivation and that what fell outside it is accounted, not dropped.
func TestCertifyRepoDryRunRanksSelectsAndAccounts(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--top", "1", "--dry-run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, reposcan.ReasonNotSelected) {
		t.Errorf("bounded scan must account the unselected candidate:\n%s", s)
	}
	// The bound is applied BEFORE goals are obtained: only the selected
	// candidate becomes a job, so only it would ever cost a derivation.
	if !strings.Contains(s, "1 job(s)") {
		t.Errorf("--top 1 must emit exactly one job:\n%s", s)
	}
	// The selection rule is disclosed, not silent. Matched as a whole line
	// rather than on the substring "ranked by": Rank's own degradation note
	// ("... ranked by source size alone") contains that phrase, so a bare
	// Contains check passes even with the disclosure line deleted.
	if !regexp.MustCompile(`(?m)^  ranked by \S+; auditing 1 of 2 candidate\(s\)$`).MatchString(s) {
		t.Errorf("output must disclose the ranking signal and the bound:\n%s", s)
	}
}

// --goals still wins: hand-written goals and the file source keep working.
func TestCertifyRepoGoalsFileTakesPrecedenceOverDerivation(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "hand written"}`)

	var out, errb bytes.Buffer
	// No provider credential is needed on this path — proof derivation was
	// not attempted.
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "1 job(s)") {
		t.Errorf("hand-written goal did not produce a job:\n%s", out.String())
	}
}

// TestCertifyRepoGoalsFileIsNotBoundedByTheDefaultTop guards the existing
// hand-written-goals path against the bound added for derivation. --top's
// default exists to cap what DERIVATION costs; it is taken over ALL
// candidates, so applying it here would audit whichever 25 ranked highest —
// most of which have no hand-written goal — instead of the map the operator
// wrote. An explicit --top is still honoured (covered below).
func TestCertifyRepoGoalsFileIsNotBoundedByTheDefaultTop(t *testing.T) {
	root := t.TempDir()
	goals := map[string]string{}
	// More candidates than defaultScanTop, every one of them goaled.
	for i := 0; i < defaultScanTop+5; i++ {
		name := fmt.Sprintf("f%02d", i)
		mustWrite(t, filepath.Join(root, "pkg", name+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", name+"_test.go"), "package pkg\n")
		goals["pkg/"+name+".go"] = "must not panic"
	}
	b, err := json.Marshal(goals)
	if err != nil {
		t.Fatal(err)
	}
	goalsFile := filepath.Join(root, "goals.json")
	mustWrite(t, goalsFile, string(b))

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goalsFile, "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	want := fmt.Sprintf("%d job(s)", defaultScanTop+5)
	if !strings.Contains(out.String(), want) {
		t.Errorf("every hand-written goal must become a job (want %q):\n%s", want, out.String())
	}
	if strings.Contains(out.String(), reposcan.ReasonNotSelected) {
		t.Errorf("the default bound must not apply to --goals:\n%s", out.String())
	}
}

// ...but an EXPLICIT --top still bounds the goals path, so an operator can cap
// a large hand-written map on purpose.
func TestCertifyRepoExplicitTopStillBoundsTheGoalsPath(t *testing.T) {
	root := t.TempDir()
	goals := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(root, "pkg", n+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", n+"_test.go"), "package pkg\n")
		goals["pkg/"+n+".go"] = "must not panic"
	}
	b, err := json.Marshal(goals)
	if err != nil {
		t.Fatal(err)
	}
	goalsFile := filepath.Join(root, "goals.json")
	mustWrite(t, goalsFile, string(b))

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goalsFile, "--top", "2", "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "2 job(s)") {
		t.Errorf("an explicit --top must bound the goals path too:\n%s", out.String())
	}
	if !strings.Contains(out.String(), reposcan.ReasonNotSelected) {
		t.Errorf("the bounded-out candidate must be accounted:\n%s", out.String())
	}
}

// --- final-review fix wave -------------------------------------------------

// TestCertifyRepoPreflightsBeforeSpendingOnDerivation is I1: EmitJobs performs
// up to --top sequential model calls, and the two scan-fatal preflights (jail,
// provider roles) used to run AFTER it. On a host that cannot sandbox the
// operator paid for 25 derivations and then got exit 1 having graded nothing.
//
// The jail is forced to fail here, so the run must die before a goal source is
// ever built: no disclosure line, no deriver error, exit 1.
func TestCertifyRepoPreflightsBeforeSpendingOnDerivation(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")

	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) {
		return nil, errors.New("no usable sandbox on this host")
	}
	t.Cleanup(func() { resolveJailFn = orig })

	var out, errb bytes.Buffer
	// NOT a dry run: this is the path that spends money.
	code := runCertifyRepo([]string{"--repo", root}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 from the jail preflight; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "no usable sandbox on this host") {
		t.Errorf("want the jail preflight failure on stderr:\n%s", errb.String())
	}
	// The proof that nothing was paid for: the goal source is built AFTER the
	// preflights, so neither its disclosure line nor its credential error can
	// have been reached.
	if strings.Contains(out.String(), "goals derived per file by") {
		t.Errorf("a goal source was built before the jail preflight:\n%s", out.String())
	}
	if strings.Contains(errb.String(), "goal deriver") {
		t.Errorf("the deriver was constructed before the jail preflight:\n%s", errb.String())
	}
}

// TestResolveGoalSourceDerivedPathDisclosesTheModel is I3. There is deliberately
// no goal-critic — a goal cannot be executed, so a second model grading the
// first is opinion on opinion — which makes this line the entire accountability
// mechanism for a machine-invented goal.
func TestResolveGoalSourceDerivedPathDisclosesTheModel(t *testing.T) {
	var errb bytes.Buffer
	called := 0
	gs, disclosure, code := resolveGoalSource(&errb, t.TempDir(), "", "test-model-x", "", false, 3,
		func(model, _ string) (reposcan.Deriver, error) {
			called++
			if model != "test-model-x" {
				t.Errorf("factory got model %q", model)
			}
			return stubDeriver{}, nil
		}, nil, false, true)
	if code != 0 || gs == nil {
		t.Fatalf("code=%d gs=%v stderr=%s", code, gs, errb.String())
	}
	if called != 1 {
		t.Errorf("deriver factory called %d times, want 1", called)
	}
	for _, want := range []string{
		"goals derived per file by test-model-x@" + version,
		"no goal-critic",
		"judged after the fact by mutant yield",
	} {
		if !strings.Contains(disclosure, want) {
			t.Errorf("disclosure missing %q:\n%s", want, disclosure)
		}
	}
}

// The hand-written path invents nothing, so it discloses nothing — and must not
// build a deriver at all (that path needs no provider credential).
func TestResolveGoalSourceGoalsFileDisclosesNothingAndDerivesNothing(t *testing.T) {
	root := t.TempDir()
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "hand written"}`)

	var errb bytes.Buffer
	gs, disclosure, code := resolveGoalSource(&errb, root, goals, "test-model-x", "", false, 3,
		func(string, string) (reposcan.Deriver, error) {
			t.Fatal("the --goals path must never construct a deriver")
			return nil, nil
		}, nil, false, true)
	if code != 0 || gs == nil {
		t.Fatalf("code=%d gs=%v stderr=%s", code, gs, errb.String())
	}
	if disclosure != "" {
		t.Errorf("the --goals path must disclose no derivation, got %q", disclosure)
	}
}

// A scan that selected nothing never asks for a goal, so it must not demand a
// provider credential either.
func TestResolveGoalSourceNothingSelectedNeedsNoDeriver(t *testing.T) {
	var errb bytes.Buffer
	gs, disclosure, code := resolveGoalSource(&errb, t.TempDir(), "", "test-model-x", "", false, 0,
		func(string, string) (reposcan.Deriver, error) {
			t.Fatal("no candidate was selected; a deriver must not be built")
			return nil, nil
		}, nil, false, true)
	if code != 0 || gs == nil || disclosure != "" {
		t.Fatalf("code=%d gs=%v disclosure=%q stderr=%s", code, gs, disclosure, errb.String())
	}
}

// A missing credential is a USAGE error (exit 2), reported before any spend.
func TestResolveGoalSourceDeriverFailureIsAUsageError(t *testing.T) {
	var errb bytes.Buffer
	gs, _, code := resolveGoalSource(&errb, t.TempDir(), "", "test-model-x", "", false, 3,
		func(string, string) (reposcan.Deriver, error) { return nil, errors.New("goal deriver: no key") }, nil, false, true)
	if code != 2 || gs != nil {
		t.Fatalf("code=%d gs=%v, want 2 and nil", code, gs)
	}
	if !strings.Contains(errb.String(), "no key") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// The disclosure PRINT SITE is shared by both paths that have something to say;
// this pins it through the CLI on the one that costs nothing to run.
func TestCertifyRepoDryRunSaysGoalsWereNotDerived(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")

	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "goals were NOT derived (no model calls)") {
		t.Errorf("a dry run must say the goals are placeholders:\n%s", out.String())
	}
	// ...and must never claim a derivation it did not perform.
	if strings.Contains(out.String(), "goals derived per file by") {
		t.Errorf("a dry run must not claim derived goals:\n%s", out.String())
	}
}

// TestPrintExclusionsListsCandidateLevelReasonsFirst is I6: the listing is
// capped at 20 lines and enumerate-level exclusions come first by construction,
// so a real bounded scan spent every printed line on `no-language` noise and
// named none of the files that fell outside the bound.
func TestPrintExclusionsListsCandidateLevelReasonsFirst(t *testing.T) {
	var excl []reposcan.Exclusion
	// 30 enumerate-level exclusions, ahead of the interesting ones — exactly
	// the shape Enumerate + Select produce.
	for i := 0; i < 30; i++ {
		excl = append(excl, reposcan.Exclusion{
			Path:   fmt.Sprintf("doc%02d.md", i),
			Reason: reposcan.ReasonNoLanguage,
		})
	}
	excl = append(excl,
		reposcan.Exclusion{Path: "bounded_out.go", Reason: reposcan.ReasonNotSelected},
		reposcan.Exclusion{Path: "unclear.go", Reason: reposcan.ReasonUngoaled},
		reposcan.Exclusion{Path: "ratelimited.go", Reason: reposcan.ReasonDeriveFailed},
		reposcan.Exclusion{Path: "generated.go", Reason: reposcan.ReasonSourceTooLarge},
	)

	var out bytes.Buffer
	printExclusions(&out, excl)
	s := out.String()
	for _, want := range []string{"bounded_out.go", "unclear.go", "ratelimited.go", "generated.go"} {
		if !strings.Contains(s, want) {
			t.Errorf("candidate-level exclusion %s was buried under the cap:\n%s", want, s)
		}
	}
	// The tally stays COMPLETE regardless of what the capped listing shows.
	if !strings.Contains(s, "30 "+reposcan.ReasonNoLanguage) {
		t.Errorf("the tally by reason must still count every exclusion:\n%s", s)
	}
	if !strings.Contains(s, "and 14 more excluded file(s)") {
		t.Errorf("the cap must announce exactly how many lines it withheld:\n%s", s)
	}
}

// TestPrintSearchPairingsIsCapped pins the same cap-and-announce shape
// printExclusions already uses: a real repo can have far more than
// maxListedExclusions files paired only by the recursive search fallback,
// and an uncapped listing buries the report exactly the way an uncapped
// exclusion listing used to.
func TestPrintSearchPairingsIsCapped(t *testing.T) {
	var cands []reposcan.Candidate
	n := maxListedExclusions + 5
	for i := 0; i < n; i++ {
		cands = append(cands, reposcan.Candidate{
			Path:      fmt.Sprintf("pkg%02d/thing.py", i),
			TestPath:  fmt.Sprintf("tests/pkg%02d/test_thing.py", i),
			ViaSearch: true,
		})
	}

	var out bytes.Buffer
	printSearchPairings(&out, cands)
	s := out.String()

	for i := 0; i < maxListedExclusions; i++ {
		want := fmt.Sprintf("pkg%02d/thing.py", i)
		if !strings.Contains(s, want) {
			t.Errorf("pairing %s should be listed (within the cap):\n%s", want, s)
		}
	}
	if strings.Contains(s, fmt.Sprintf("pkg%02d/thing.py", n-1)) {
		t.Errorf("pairing beyond the cap must not be listed individually:\n%s", s)
	}
	if !strings.Contains(s, "... and 5 more paired by search") {
		t.Errorf("the cap must announce exactly how many lines it withheld:\n%s", s)
	}
}

// --all audits every candidate, ignoring the default bound.
func TestCertifyRepoAllIgnoresTheDefaultBound(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < defaultScanTop+3; i++ {
		name := fmt.Sprintf("f%02d", i)
		mustWrite(t, filepath.Join(root, "pkg", name+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", name+"_test.go"), "package pkg\n")
	}
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--all", "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	want := fmt.Sprintf("auditing %d of %d candidate(s)", defaultScanTop+3, defaultScanTop+3)
	if !strings.Contains(out.String(), want) {
		t.Errorf("--all must select every candidate (want %q):\n%s", want, out.String())
	}
	if strings.Contains(out.String(), reposcan.ReasonNotSelected) {
		t.Errorf("--all must leave nothing unselected:\n%s", out.String())
	}
}

// --top 0 and a negative --top both mean "no bound", like --all.
func TestCertifyRepoTopZeroAndNegativeMeanUnbounded(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < defaultScanTop+2; i++ {
		name := fmt.Sprintf("f%02d", i)
		mustWrite(t, filepath.Join(root, "pkg", name+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", name+"_test.go"), "package pkg\n")
	}
	for _, top := range []string{"0", "-1"} {
		var out, errb bytes.Buffer
		if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--top", top, "--dry-run"}, &out, &errb); code != 0 {
			t.Fatalf("--top %s: exit %d, stderr=%s", top, code, errb.String())
		}
		want := fmt.Sprintf("auditing %d of %d candidate(s)", defaultScanTop+2, defaultScanTop+2)
		if !strings.Contains(out.String(), want) {
			t.Errorf("--top %s must be unbounded (want %q):\n%s", top, want, out.String())
		}
		if strings.Contains(out.String(), reposcan.ReasonNotSelected) {
			t.Errorf("--top %s must leave nothing unselected:\n%s", top, out.String())
		}
	}
}

// stubDeriver is never called: the tests above only exercise how a goal source
// is CHOSEN and disclosed. No unit test in this package may make a model call.
type stubDeriver struct{}

func (stubDeriver) Derive(context.Context, reposcan.Candidate, string) (string, bool, error) {
	return "", false, errors.New("stubDeriver must never be invoked in a unit test")
}

// TestWorkspaceSubstrateNeverResolvesAJailPerFile is the PER-JOB layer of the
// same invariant TestNewLocalExecutorSkipsSandboxForWorkspaceSubstrate (the
// constructor layer) and TestNewLocalExecutorSkipsTheSeedCacheForWorkspaceSubstrate
// (the scan-wide seed layer) assert: selecting the workspace substrate means
// NO jail construction runs, anywhere.
//
// prepareAuditJail resolved an isolator whenever localAuditInput.iso was nil,
// with no substrate guard — and on the workspace path iso is ALWAYS nil (the
// constructor deliberately leaves it so). On a GitHub-hosted runner, which
// ships no bubblewrap, that resolution fails closed and every audited file
// came back `could not audit: no working bwrap sandbox`, i.e. exit 1 with
// COULD-NOT-GRADE — the precise false red this substrate exists to remove.
//
// The two constructor-level tests cannot catch it: they stop before any job
// runs. So this one stubs the resolver to FAIL (a bwrap-less runner) and
// drives a real Execute — with the REAL baselineRunnerFor seam, which is what
// calls prepareAuditJail — through to a gradable result.
func TestWorkspaceSubstrateNeverResolvesAJailPerFile(t *testing.T) {
	var resolutions int
	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) {
		resolutions++
		return nil, errors.New("no working bwrap sandbox: bwrap backend unavailable")
	}
	t.Cleanup(func() { resolveJailFn = orig })

	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "a.go"), "package p\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(repo, "a_test.go"), "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n")

	// `true` stands in for the project's own test command: the point here is
	// which SUBSTRATE runs it, not what it runs. It exits 0, so the baseline
	// is stable and passing — and it runs through the real WorkspaceRunner.
	ex := newLocalExecutor(repo, []string{"true"}, substrateWorkspace, 0, io.Discard)
	defer ex.Close()
	// newBaseline is deliberately left REAL (baselineRunnerFor →
	// prepareAuditJail): that is the layer under test. Only the audit itself
	// is stubbed, because it would spend model calls.
	ex.audit = func(context.Context, localAuditInput) (advpool.Verdict, error) {
		return advpool.Verdict{DevKillRate: 1, MutantsTotal: 1}, nil
	}

	job := reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "A returns 1"}}
	res, err := ex.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("workspace substrate must not need a jail to audit a file: %v", err)
	}
	if !res.Gradable {
		t.Fatalf("result not gradable (reason %q); the workspace substrate graded nothing", res.Reason)
	}
	if resolutions != 0 {
		t.Errorf("the workspace substrate resolved a jail %d time(s), want 0", resolutions)
	}
}

// TestWorkspaceSubstrateSerializesTheSwarm proves the scan does not fan out
// concurrent jobs over ONE shared checkout.
//
// On the workspace substrate every job mutates the same tree in place
// (adequacy.NewWorkspaceRunner(repoDir)), and applyFiles' restore ledger is
// per-runner — it assumes exclusivity. Two jobs at once means job B's suite
// runs while job A has a mutant (or adequacy.CanaryCode, which does not even
// compile) written into A's file: B's surviving mutants get recorded as
// KILLED, inflating the kill rate on a record this product signs, and B's
// baseline can fail into a spurious baseline-failed/flaky-baseline. The swarm
// sizing was substrate-blind (NumCPU-1, so a live run showed 8 workers), and
// the Action passes no --swarm, so any PR touching two audited files hit it.
//
// Serialization is the accepted cost — giving each job its own tree copy is
// exactly the memory ceiling this substrate exists to escape — so it must be
// SAID, not silently differ from what the operator asked for.
func TestWorkspaceSubstrateSerializesTheSwarm(t *testing.T) {
	for _, ask := range []int{0, 4, 8} {
		workers, readout := resolveScanWorkers(ask, substrateWorkspace)
		if workers != 1 {
			t.Errorf("--swarm %d on the workspace substrate: %d workers, want 1 (one shared checkout)", ask, workers)
		}
		if !strings.Contains(readout, "1 worker") {
			t.Errorf("--swarm %d: readout %q must state the real worker count", ask, readout)
		}
		if !strings.Contains(readout, substrateWorkspace) {
			t.Errorf("--swarm %d: readout %q must say WHY it serialized, naming the substrate", ask, readout)
		}
	}
}

// TestJailSubstrateSwarmSizingIsUnchanged is the other direction: the jail
// substrate (including the "" zero value, today's shipped default) keeps the
// exact auto-sizing and the exact readout it has always had. `certify --repo`
// is a shipped command; the fix above must not change it.
func TestJailSubstrateSwarmSizingIsUnchanged(t *testing.T) {
	for _, substrate := range []string{"", substrateJail} {
		for _, ask := range []int{0, 3} {
			workers, readout := resolveScanWorkers(ask, substrate)
			if want := resolveSwarm(ask); workers != want {
				t.Errorf("substrate %q, --swarm %d: %d workers, want %d", substrate, ask, workers, want)
			}
			if got, want := readout, fmt.Sprintf("  swarm: %d workers\n", workers); got != want {
				t.Errorf("substrate %q: readout %q, want %q (unchanged)", substrate, got, want)
			}
		}
	}
}

// --- --preflight -----------------------------------------------------------
//
// preflightGoFixture writes a tiny, REAL Go module to root: pkg/a.go is
// exercised by pkg/a_test.go, pkg/b.go has no paired test at all (so it is
// an Enumerate-level ReasonNoPairedTest exclusion, never a Candidate) but IS
// still language-detected, non-test source — exactly the file
// enumeratedSourcePaths must add back so the pre-flight can report it as
// measured-and-never-executed. `go test ./... -coverprofile=...` genuinely
// instruments BOTH files (same package), so this exercises the real
// substrate end to end, no mocked runner.
func preflightGoFixture(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "go.mod"), "module coveragefixture\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n\nfunc B() int { return 2 }\n")
}

// TestCertifyRepoPreflightFlagAbsentIsByteIdenticalToBaseline is the brief's
// scenario 1: with --preflight absent, the runner must never be invoked and
// stdout must be byte-identical to today. Proven by capturing BOTH a run
// without the flag and a run WITH it (same fixture, same everything else),
// then confirming the --preflight run is EXACTLY the no-flag run plus (a)
// one extra progress line and (b) one extra trailing report section — never
// a difference anywhere else in the shared output.
//
// --goals maps to {} (nothing goaled) so EmitJobs emits ZERO jobs: no
// baseline run, no mutant generation, no model call of any kind — the only
// thing this scan does besides accounting is the pre-flight's own real `go
// test` run, which is exactly what is under test.
func TestCertifyRepoPreflightFlagAbsentIsByteIdenticalToBaseline(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightGoFixture(t, root)
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{}`)

	var without, with bytes.Buffer
	var errb1, errb2 bytes.Buffer
	// --no-ledger on both: the second run would otherwise read the first's
	// entry as its prior, which is a documented line but not --preflight's.
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--substrate", substrateWorkspace, "--no-ledger"}, &without, &errb1); code != 1 {
		// 0 jobs emitted (nothing goaled) => COULD-NOT-GRADE => exit 1. The
		// exit code itself is not what this test is about; it just must be
		// the SAME in both runs (checked below).
		t.Logf("no-flag run exit %d, stderr=%s", code, errb1.String())
	}
	codeWith := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--substrate", substrateWorkspace, "--preflight", "--no-ledger"}, &with, &errb2)
	_ = codeWith

	withStr := with.String()
	head, _, found := strings.Cut(withStr, "\nCoverage pre-flight")
	if !found {
		t.Fatalf("--preflight run did not print the coverage pre-flight section:\n%s", withStr)
	}
	const progressLine = "  preflight: running the suite once with coverage instrumentation…\n"
	if !strings.Contains(head, progressLine) {
		t.Fatalf("--preflight run did not print the pre-flight progress line:\n%s", head)
	}
	head = strings.Replace(head, progressLine, "", 1)

	if head != without.String() {
		t.Fatalf("--preflight run's shared output diverged from the no-flag baseline beyond the two documented additions.\nno-flag:\n%s\n--preflight (with additions stripped):\n%s", without.String(), head)
	}
}

// TestCertifyRepoPreflightRanNamesUnexercisedFiles is the brief's scenario 2:
// with --preflight and a pre-flight that DID run, the report names the
// unexercised file(s) under their own heading. Runs the real `go test
// ./... -coverprofile=...` against preflightGoFixture (real substrate, no
// mocking) — pkg/b.go is never called by the suite and must be named;
// pkg/a.go IS exercised and must not appear in that list.
func TestCertifyRepoPreflightRanNamesUnexercisedFiles(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightGoFixture(t, root)
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{}`) // nothing goaled: Scan runs 0 jobs, no model call

	var out, errb bytes.Buffer
	runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--substrate", substrateWorkspace, "--preflight"}, &out, &errb)

	s := out.String()
	if !strings.Contains(s, "Coverage pre-flight") {
		t.Fatalf("missing the pre-flight section:\n%s\nstderr:\n%s", s, errb.String())
	}
	if !strings.Contains(s, "measured and NEVER executed by the suite") {
		t.Fatalf("want the unexercised-files heading:\n%s", s)
	}
	_, section, found := strings.Cut(s, "\nCoverage pre-flight")
	if !found {
		t.Fatalf("missing the pre-flight section:\n%s", s)
	}
	if !strings.Contains(section, "pkg/b.go") {
		t.Errorf("pkg/b.go is never called by the suite and must be named as unexercised:\n%s", section)
	}
	if strings.Contains(section, "pkg/a.go") {
		t.Errorf("pkg/a.go IS exercised by the suite and must not be named anywhere in the pre-flight section: %q", section)
	}
	if !strings.Contains(section, "1 file(s) executed at least once") {
		t.Errorf("want pkg/a.go counted as executed:\n%s", section)
	}
}

// TestCertifyRepoPreflightCouldNotRunReportsNoteAndNoFileList is the brief's
// scenario 3: when the pre-flight could not run (here: zero candidates, so
// runPreflight declines with a Note rather than guessing), the report says
// so and lists NO unexercised files — Ran == false means no file list, ever.
func TestCertifyRepoPreflightCouldNotRunReportsNoteAndNoFileList(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "README.md"), "# nothing to audit here\n")

	var out, errb bytes.Buffer
	runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--substrate", substrateWorkspace, "--preflight"}, &out, &errb)

	s := out.String()
	if !strings.Contains(s, "Coverage pre-flight") {
		t.Fatalf("missing the pre-flight section:\n%s\nstderr:\n%s", s, errb.String())
	}
	if !strings.Contains(s, "could not run:") {
		t.Errorf("want the could-not-run note:\n%s", s)
	}
	if strings.Contains(s, "measured and NEVER executed") {
		t.Errorf("a Ran=false pre-flight must never print an unexercised-files list:\n%s", s)
	}
	if strings.Contains(s, "file(s) executed at least once") {
		t.Errorf("a Ran=false pre-flight must never print executed-file counts either:\n%s", s)
	}
}

// TestCertifyRepoPreflightDryRunNeverRunsTheSuite is the brief's scenario 4:
// --dry-run means no execution, full stop — even with --preflight, the
// instrumented suite must never run. Proven the same way scenario 1 is: the
// --dry-run+--preflight output must be BYTE-IDENTICAL to the --dry-run-only
// output — no progress line, no report section, nothing.
func TestCertifyRepoPreflightDryRunNeverRunsTheSuite(t *testing.T) {
	root := t.TempDir()
	preflightGoFixture(t, root)
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must return 1"}`)

	var without, with bytes.Buffer
	var errb1, errb2 bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run"}, &without, &errb1); code != 0 {
		t.Fatalf("no-flag dry run: exit %d, stderr=%s", code, errb1.String())
	}
	if code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals, "--dry-run", "--preflight"}, &with, &errb2); code != 0 {
		t.Fatalf("--preflight dry run: exit %d, stderr=%s", code, errb2.String())
	}

	if with.String() != without.String() {
		t.Fatalf("--dry-run --preflight must be byte-identical to --dry-run alone (dry-run means no execution).\nwithout:\n%s\nwith:\n%s", without.String(), with.String())
	}
	if strings.Contains(with.String(), "Coverage pre-flight") || strings.Contains(with.String(), "preflight: running") {
		t.Errorf("a dry run must never print any pre-flight output:\n%s", with.String())
	}
}

// TestEnumeratedSourcePathsAddsBackNoPairedTestAndAmbiguousFiles pins
// enumeratedSourcePaths' own contract directly: it must recover EVERY
// language-detected, non-test file Enumerate saw — not just the ones that
// became Candidates — by adding back ReasonNoPairedTest and
// ReasonAmbiguousTest exclusions (both still language-detected, non-test
// files), while leaving ReasonNoLanguage/ReasonIsTest/ReasonNotRegularFile/
// ReasonSkippedDir OUT (they are not source files Enumerate ever treated as
// auditable subjects).
func TestEnumeratedSourcePathsAddsBackNoPairedTestAndAmbiguousFiles(t *testing.T) {
	cands := []reposcan.Candidate{{Path: "pkg/a.go", Lang: "go"}}
	excl := []reposcan.Exclusion{
		{Path: "pkg/b.go", Reason: reposcan.ReasonNoPairedTest},
		{Path: "pkg/c.go", Reason: reposcan.ReasonAmbiguousTest},
		{Path: "pkg/a_test.go", Reason: reposcan.ReasonIsTest},
		{Path: "README.md", Reason: reposcan.ReasonNoLanguage},
	}
	got := enumeratedSourcePaths(cands, excl)
	want := []string{"pkg/a.go", "pkg/b.go", "pkg/c.go"}
	if len(got) != len(want) {
		t.Fatalf("enumeratedSourcePaths = %v, want %v", got, want)
	}
	gotSet := map[string]bool{}
	for _, p := range got {
		gotSet[p] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("enumeratedSourcePaths missing %q; got %v", w, got)
		}
	}
	if gotSet["pkg/a_test.go"] || gotSet["README.md"] {
		t.Errorf("enumeratedSourcePaths must not include is-test/no-language exclusions: %v", got)
	}
}

// TestSplitPreflightFindingsThreeBuckets pins the tri-state fan-out directly:
// present-true is "executed", present-false is "unexercised" (the actual
// finding), and ABSENT is a count only, never a name.
func TestSplitPreflightFindingsThreeBuckets(t *testing.T) {
	sources := []string{"a.go", "b.go", "c.go", "d.go"}
	cm := reposcan.CoverageMap{
		Ran: true,
		Executed: map[string]bool{
			"a.go": true,
			"b.go": false,
			// c.go, d.go: absent — never measured.
		},
	}
	got := splitPreflightFindings(sources, cm)
	if got.executed != 1 {
		t.Errorf("executed = %d, want 1", got.executed)
	}
	if len(got.unexercised) != 1 || got.unexercised[0] != "b.go" {
		t.Errorf("unexercised = %v, want [b.go]", got.unexercised)
	}
	if got.notMeasured != 2 {
		t.Errorf("notMeasured = %d, want 2", got.notMeasured)
	}
}

// TestPrintPreflightReportRanFalsePrintsNoteOnlyNoFileList pins the printer's
// own contract (belt-and-braces alongside the CLI-level integration test
// above): Ran == false prints ONLY the Note, never a file list, regardless
// of what sourceFiles contains.
func TestPrintPreflightReportRanFalsePrintsNoteOnlyNoFileList(t *testing.T) {
	var buf bytes.Buffer
	printPreflightReport(&buf, reposcan.CoverageMap{Ran: false, Note: "go: no coverage instrumentation for test command []"}, []string{"a.go", "b.go"})
	s := buf.String()
	if !strings.Contains(s, "go: no coverage instrumentation for test command []") {
		t.Errorf("want the Note printed verbatim:\n%s", s)
	}
	if strings.Contains(s, "a.go") || strings.Contains(s, "b.go") {
		t.Errorf("Ran=false must never print a file list:\n%s", s)
	}
}

// TestSelectPreflightLanguageNoCheckArgvAlwaysDeclines pins the unchanged
// half of F5: with no explicit `-- <cmd>`, there is no principled way to
// pick a stock TestCmd() across multiple languages, so a multi-language
// scan still declines exactly as it did before this function existed.
func TestSelectPreflightLanguageNoCheckArgvAlwaysDeclines(t *testing.T) {
	langName, note := selectPreflightLanguage(map[string]bool{"python": true, "typescript": true}, nil)
	if langName != "" {
		t.Fatalf("langName = %q, want \"\" (no checkArgv given)", langName)
	}
	if !strings.Contains(note, "scan spans 2 languages") {
		t.Errorf("note = %q, want it to name the languages", note)
	}
}

// TestSelectPreflightLanguageResolvesAisuiteShape pins the F5 fix itself:
// andrewyng/aisuite has python + typescript candidates, but typescript has
// no lang.CoverageReporter at all — so `-- pytest -q` is NOT ambiguous, it
// is the only language among the candidates that can even answer the
// question. Before this fix, runPreflight declined this repo outright;
// after it, python is instrumented and typescript files simply never enter
// CoverageMap.Executed (reported as a "not measured" count elsewhere, not
// here).
func TestSelectPreflightLanguageResolvesAisuiteShape(t *testing.T) {
	langName, note := selectPreflightLanguage(map[string]bool{"python": true, "typescript": true}, []string{"pytest", "-q"})
	if langName != "python" {
		t.Fatalf("langName = %q, note = %q; want \"python\" (typescript has no CoverageReporter, so python is unambiguous)", langName, note)
	}
	if note != "" {
		t.Errorf("note = %q, want empty on a resolved language", note)
	}
}

// TestSelectPreflightLanguageGoAndPythonBothMatchStaysAmbiguous pins the
// boundary F5 must NOT cross: goPlugin.CoverageCmd accepts ANY non-empty
// argv by design (it never inspects shape), so a scan spanning go AND
// python, given `-- pytest -q`, has TWO languages whose CoverageCmd
// accepts it — genuinely ambiguous, and must still decline rather than
// guess which one the operator meant.
func TestSelectPreflightLanguageGoAndPythonBothMatchStaysAmbiguous(t *testing.T) {
	langName, note := selectPreflightLanguage(map[string]bool{"python": true, "go": true}, []string{"pytest", "-q"})
	if langName != "" {
		t.Fatalf("langName = %q, want \"\" (go's CoverageCmd accepts any argv, so this is genuinely ambiguous)", langName)
	}
	if !strings.Contains(note, "ambiguous") {
		t.Errorf("note = %q, want it to say ambiguous", note)
	}
}

// TestSelectPreflightLanguageNoLanguageMatchesFallsBackToTheBlanketRefusal
// covers the zero-match case: a `--` command shaped for neither candidate
// language's coverage instrumentation falls back to the same "spans N
// languages" refusal the no-checkArgv case gives, rather than a confusing
// "ambiguous" message about zero matches.
//
// The argv here used to be `npm test`, chosen when TypeScript had no
// CoverageReporter at all and so could match nothing. TypeScript has one now
// (it delegates to the Node/V8 instrumentation), which makes `npm test` a
// genuine single match — see the test below. A build-system command that no
// language plugin claims keeps this path covered for what it is actually
// about.
func TestSelectPreflightLanguageNoLanguageMatchesFallsBackToTheBlanketRefusal(t *testing.T) {
	langName, note := selectPreflightLanguage(map[string]bool{"python": true, "typescript": true}, []string{"make", "check"})
	if langName != "" {
		t.Fatalf("langName = %q, want \"\" (neither candidate language's CoverageCmd accepts this argv)", langName)
	}
	if !strings.Contains(note, "scan spans 2 languages") || strings.Contains(note, "ambiguous") {
		t.Errorf("note = %q, want the blanket refusal, not the ambiguous-match wording", note)
	}
}

// TestSelectPreflightLanguageResolvesNodeCommandsToTypeScript pins what the
// Ruby/JS/TS coverage reporters bought at this seam.
//
// Before they existed, a mixed python+typescript scan given `-- npm test` got
// the blanket refusal: TypeScript could not be instrumented, so no language
// claimed the command and the pre-flight was skipped entirely. It now resolves
// — `npm test` is a Node command, python's CoverageCmd declines it, and one
// match is not ambiguous. This is the disambiguation the allow-list in each
// CoverageCmd exists to make possible: a reporter that accepted any argv (as
// goPlugin's does, deliberately) cannot participate in it.
func TestSelectPreflightLanguageResolvesNodeCommandsToTypeScript(t *testing.T) {
	langName, note := selectPreflightLanguage(map[string]bool{"python": true, "typescript": true}, []string{"npm", "test"})
	if langName != "typescript" {
		t.Fatalf("langName = %q, note = %q; want \"typescript\" (npm is a Node command and python declines it)", langName, note)
	}
	if note != "" {
		t.Errorf("note = %q, want empty on a clean single match", note)
	}
}

// TestGoModulePathParsesLegalGoModForms is the regression for the review's
// Minor 5. goModulePath did `strings.CutPrefix(line, "module ")` +
// TrimSpace, which mis-parses two forms `go.mod` legally permits — a quoted
// module path and a trailing `//` comment. The returned prefix then matches
// no profile path, every Go file falls out of CoverageMap.Executed, and the
// report reads `0 executed, 0 findings, N not measured`: confident, empty,
// and indistinguishable from a genuinely uninstrumented repo.
func TestGoModulePathParsesLegalGoModForms(t *testing.T) {
	cases := []struct {
		name    string
		gomod   string
		want    string
		wantErr bool
	}{
		{"plain", "module example.com/x\n\ngo 1.22\n", "example.com/x", false},
		{"quoted", "module \"example.com/x\"\n\ngo 1.22\n", "example.com/x", false},
		{"trailing comment", "module example.com/x // v2\n\ngo 1.22\n", "example.com/x", false},
		{"quoted and commented", "module \"example.com/x\" // why\n", "example.com/x", false},
		{"tab separated", "module\texample.com/x\n", "example.com/x", false},
		// A go.mod with no module line at all cannot yield a prefix, and a
		// missing prefix silently voids the whole report — so it must be a
		// REPORTED failure, never a silent empty string.
		{"no module directive", "go 1.22\n", "", true},
		{"empty module path", "module \n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.gomod), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := goModulePath(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("goModulePath = (%q, nil), want an error: an unparseable module line voids the entire report", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("goModulePath = %v, want %q", err, tc.want)
			}
			if got != tc.want {
				t.Errorf("goModulePath = %q, want %q", got, tc.want)
			}
		})
	}
}

// A repo with no go.mod at all is the same class of failure: there is no
// module prefix to strip, so every Go file would land in "never measured".
// Reported, not silently empty.
func TestGoModulePathReportsAMissingGoMod(t *testing.T) {
	if _, err := goModulePath(t.TempDir()); err == nil {
		t.Fatal("goModulePath on a directory with no go.mod returned no error")
	}
}

// preflightUnpairedPythonFixture writes a small, REAL Python project in
// which corral's test-PAIRING heuristic matches nothing at all — the exact
// shape of the repos --preflight exists for. `mypkg/core.py` would pair with
// `test_core.py`/`tests/test_core.py`; the suite instead lives in
// `tests/test_smoke.py`, which pairs with no source file, so every source
// file is an Enumerate-level ReasonNoPairedTest exclusion and the scan has
// ZERO candidates.
//
// `mypkg/orphan.py` is never imported by anything; the coverage config's
// `source = ["mypkg"]` is what makes coverage.py measure it anyway (real
// projects — flask's own pyproject.toml among them — carry exactly this),
// so it lands in the measured-and-never-executed bucket rather than
// vanishing into "not measured".
func preflightUnpairedPythonFixture(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.coverage.run]\nsource = [\"mypkg\"]\n")
	mustWrite(t, filepath.Join(root, "mypkg", "__init__.py"), "")
	mustWrite(t, filepath.Join(root, "mypkg", "core.py"), "def used():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "mypkg", "orphan.py"), "def never_called():\n    return 2\n")
	mustWrite(t, filepath.Join(root, "tests", "test_smoke.py"),
		"from mypkg.core import used\n\n\ndef test_it():\n    assert used() == 1\n")
}

func skipWithoutPythonCoverage(t *testing.T) {
	t.Helper()
	// #nosec G204 -- fixed argv
	if err := exec.Command("python3", "-c", "import coverage, pytest").Run(); err != nil {
		t.Skipf("python3 with coverage+pytest not available: %v", err)
	}
}

// preflightImportOnlyPythonFixture is preflightUnpairedPythonFixture's
// sibling for THE FOURTH DEFECT: mypkg/__init__.py carries real
// module-level (import-time) code, executed once when
// tests/test_smoke.py's `from mypkg.core import used` first imports the
// package — at COLLECTION time, before pytest-cov has switched to any
// test's own dynamic context, so coverage.py records it as STATIC
// (context-less) coverage, never a per-test one. It has no filename
// pairing (no test___init__.py) and zero covering tests, but IS genuinely
// executed — the exact shape ReasonImportOnly exists to name honestly,
// distinct from mypkg/orphan.py's genuine ReasonUncovered right beside it.
func preflightImportOnlyPythonFixture(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.coverage.run]\nsource = [\"mypkg\"]\n")
	mustWrite(t, filepath.Join(root, "mypkg", "__init__.py"), "GREETING = \"hello\"\n")
	mustWrite(t, filepath.Join(root, "mypkg", "core.py"), "def used():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "mypkg", "orphan.py"), "def never_called():\n    return 2\n")
	mustWrite(t, filepath.Join(root, "tests", "test_smoke.py"),
		"from mypkg.core import used\n\n\ndef test_it():\n    assert used() == 1\n")
}

// TestCertifyRepoImportOnlyFileIsExcludedDistinctlyFromUncovered is the
// real, end-to-end proof (real pytest + coverage, no faking): a package
// __init__.py executed only at import time must be excluded
// "imported at load time — no test exercises it directly", NEVER the
// literal string "no test executes this file" — the false claim a reader
// would reasonably read as "nothing runs this", when in truth every test
// that imports the package runs it. mypkg/orphan.py, genuinely never
// executed, must still read ReasonUncovered right beside it, and the
// summary line must count both, separately.
func TestCertifyRepoImportOnlyFileIsExcludedDistinctlyFromUncovered(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightImportOnlyPythonFixture(t, root)

	var out, errb bytes.Buffer
	runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace,
	}, &out, &errb)
	s := out.String()

	if !strings.Contains(s, "excluded mypkg/__init__.py (imported at load time — no test exercises it directly)") {
		t.Errorf("mypkg/__init__.py must be excluded under the import-only reason, verbatim:\n%s", s)
	}
	if strings.Contains(s, "excluded mypkg/__init__.py (uncovered — no test executes this file)") {
		t.Errorf("mypkg/__init__.py must NEVER read as uncovered — it was genuinely executed at import time:\n%s", s)
	}
	if !strings.Contains(s, "excluded mypkg/orphan.py (uncovered — no test executes this file)") {
		t.Errorf("mypkg/orphan.py, genuinely never executed, must still read uncovered:\n%s", s)
	}
	if !strings.Contains(s, "uncovered 1 · import-only 1") {
		t.Errorf("summary line must count uncovered and import-only separately (1 each), got:\n%s", s)
	}
}

// TestCertifyRepoPreflightRunsWhenPairingFindsNoCandidates is the regression
// for the review's Important 1. runPreflight derived its language set from
// `cands` — the test-pairing-derived CANDIDATE set — so it declined with
// "preflight: no candidates to instrument" on every repo where pairing lands
// nowhere. That is the precise limitation the feature was built to route
// around, and it was measured on four real repos (jsonschema 0/31, filelock
// 0/35, itsdangerous 0/10, markupsafe 0/7).
//
// The language set now comes from the ENUMERATED SOURCE SET, which is the
// same slice the report buckets against.
//
// Since the evidence-as-candidacy change, PAIRING alone still finds no
// candidates here (mypkg/core.py has no filename-conventional test), but the
// scan's own selection evidence now DOES pair it — tests/test_smoke.py
// covers it — so it is 1 candidate, "paired by evidence", not 0. It stays
// COULD-NOT-GRADE because --goals `{}` supplies it none, so nothing about
// the pre-flight assertions below — the reason this test exists — changes:
// the pre-flight still runs over the same enumerated source set and still
// finds mypkg/orphan.py the one file measured and never executed.
func TestCertifyRepoPreflightRunsWhenPairingFindsNoCandidates(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)

	var out, errb bytes.Buffer
	runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", writeGoals(t, root, `{}`),
		"--substrate", substrateWorkspace, "--preflight"}, &out, &errb)

	s := out.String()
	if !strings.Contains(s, "1 candidate(s)") {
		t.Fatalf("fixture is wrong: this scan must have exactly 1 candidate (mypkg/core.py, paired by evidence):\n%s", s)
	}
	if !strings.Contains(s, "mypkg/core.py paired by evidence:") {
		t.Errorf("mypkg/core.py must be disclosed as evidence-paired — pairing alone finds no candidate here:\n%s", s)
	}
	_, section, found := strings.Cut(s, "\nCoverage pre-flight")
	if !found {
		t.Fatalf("missing the pre-flight section:\n%s\nstderr:\n%s", s, errb.String())
	}
	if strings.Contains(section, "could not run:") {
		t.Fatalf("the pre-flight declined on a repo with no candidates — the exact repos it exists for:\n%s", section)
	}
	if !strings.Contains(section, "mypkg/orphan.py") {
		t.Errorf("mypkg/orphan.py is measured and never executed and must be named:\n%s", section)
	}
	if strings.Contains(section, "mypkg/core.py") {
		t.Errorf("mypkg/core.py IS executed by the suite and must not be named:\n%s", section)
	}
}

// TestCertifyRepoPreflightRunsWhenPairingAndEvidenceBothFindNoCandidates is
// F2's restoration of the ORIGINAL zero-candidate regression
// TestCertifyRepoPreflightRunsWhenPairingFindsNoCandidates used to guard,
// before evidence-first candidacy widened its fixture to 1. --whole-suite
// collects no selection evidence at all (see the design's evidence-absent
// fallback), so candidacy here stays genuinely pairing-only and genuinely
// zero — the exact case runPreflight was built to still work on (measured
// on jsonschema 0/31, filelock 0/35, itsdangerous 0/10, markupsafe 0/7).
func TestCertifyRepoPreflightRunsWhenPairingAndEvidenceBothFindNoCandidates(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)

	var out, errb bytes.Buffer
	runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", writeGoals(t, root, `{}`),
		"--substrate", substrateWorkspace, "--whole-suite", "--preflight"}, &out, &errb)

	s := out.String()
	if !strings.Contains(s, "0 candidate(s)") {
		t.Fatalf("fixture is wrong: --whole-suite collects no evidence, so this scan must have ZERO candidates:\n%s", s)
	}
	if strings.Contains(s, "paired by evidence") {
		t.Errorf("--whole-suite must produce no evidence-paired candidate:\n%s", s)
	}
	_, section, found := strings.Cut(s, "\nCoverage pre-flight")
	if !found {
		t.Fatalf("missing the pre-flight section:\n%s\nstderr:\n%s", s, errb.String())
	}
	if strings.Contains(section, "could not run:") {
		t.Fatalf("the pre-flight declined on a repo with no candidates — the exact repos it exists for:\n%s", section)
	}
	if !strings.Contains(section, "mypkg/orphan.py") {
		t.Errorf("mypkg/orphan.py is measured and never executed and must be named:\n%s", section)
	}
	if strings.Contains(section, "mypkg/core.py") {
		t.Errorf("mypkg/core.py IS executed by the suite and must not be named:\n%s", section)
	}
}

// writeGoals writes a --goals file into root and returns its path.
func writeGoals(t *testing.T, root, body string) string {
	t.Helper()
	p := filepath.Join(root, "goals.json")
	mustWrite(t, p, body)
	return p
}

// preflightAisuiteShapeFixture reproduces andrewyng/aisuite's shape: PAIRED
// candidates in BOTH Python and TypeScript, so the emitted jobs span two
// languages — which is what makes an explicit `-- pytest -q` a refusal for
// the audit.
func preflightAisuiteShapeFixture(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.coverage.run]\nsource = [\"mypkg\"]\n")
	mustWrite(t, filepath.Join(root, "mypkg", "__init__.py"), "")
	mustWrite(t, filepath.Join(root, "mypkg", "core.py"), "def used():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "mypkg", "orphan.py"), "def never_called():\n    return 2\n")
	mustWrite(t, filepath.Join(root, "mypkg", "test_core.py"),
		"from mypkg.core import used\n\n\ndef test_it():\n    assert used() == 1\n")
	mustWrite(t, filepath.Join(root, "src", "thing.ts"), "export function thing(): number { return 1; }\n")
	mustWrite(t, filepath.Join(root, "src", "thing.test.ts"), "import { thing } from './thing';\ntest('thing', () => { expect(thing()).toBe(1); });\n")
}

// TestCertifyRepoPreflightRunsOnTheAisuiteShape is the regression for the
// review's Important 2. checkArgvSpansOneLanguage ran BEFORE runPreflight and
// returned exit 2 outright, so the documented invocation
// `certify --repo aisuite --preflight -- pytest -q` never reached the
// pre-flight at all — selectPreflightLanguage, added specifically so that
// repo would work, was unreachable in composition while README.md and
// docs/corral/github-action.md both stated flatly that aisuite runs it.
//
// The gate now scopes to THE AUDIT, which still refuses with the same
// message and the same exit 2 (asserted here, and independently in
// TestCertifyRepoRefusesAnExplicitCommandAcrossLanguages).
func TestCertifyRepoPreflightRunsOnTheAisuiteShape(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightAisuiteShapeFixture(t, root)
	goals := writeGoals(t, root, `{"mypkg/core.py": "must return 1", "src/thing.ts": "must return 1"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--goals", goals,
		"--substrate", substrateWorkspace, "--preflight", "--", "pytest", "-q"}, &out, &errb)

	// The audit still refuses — unchanged.
	if code != 2 {
		t.Fatalf("exit %d, want 2: the AUDIT must still refuse a `--` command spanning two languages.\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "spans 2 languages") {
		t.Errorf("want the audit's own refusal on stderr:\n%s", errb.String())
	}

	// ...and the pre-flight, which is not the audit, still answered.
	_, section, found := strings.Cut(out.String(), "\nCoverage pre-flight")
	if !found {
		t.Fatalf("the documented `--preflight -- pytest -q` invocation printed no pre-flight section:\n%s", out.String())
	}
	if strings.Contains(section, "could not run:") {
		t.Fatalf("`-- pytest -q` unambiguously names python here; the pre-flight must run:\n%s", section)
	}
	if !strings.Contains(section, "mypkg/orphan.py") {
		t.Errorf("mypkg/orphan.py is measured and never executed and must be named:\n%s", section)
	}
	// TypeScript was never instrumented, so its files are absent from the
	// coverage map: a COUNT, never a name.
	if strings.Contains(section, "src/thing.ts") {
		t.Errorf("a file in a language this run never instrumented must never be named:\n%s", section)
	}
}

// A diff that selected zero candidates has TWO very different causes, and the
// merge gate must not report them the same way.
//
// A docs-only PR genuinely needs no audit — "no audit was needed" is true and
// green is the right answer. But a PR that changed real source files corral
// simply could not PAIR with tests also selects zero candidates, and telling
// that reader "no audit was needed" is a fail-open: an audit was needed, corral
// could not perform one, and the gate went green anyway. This is the exact
// shape a JS/TS project hits, where filename pairing routinely finds nothing.
func TestPrintRepoReportUnpairableDiffIsNotReportedAsNothingNeeded(t *testing.T) {
	rep := reposcan.Aggregate("local", "r", "c", 0, 0, nil, nil)

	var out bytes.Buffer
	printRepoReport(&out, rep, true, nil, nil, []string{"lib/response.js", "lib/request.js"}, time.Time{})

	s := out.String()

	if strings.Contains(s, "no audit was needed") {
		t.Errorf("changed files that could not be paired must NOT be reported as needing no audit:\n%s", s)
	}
	if !strings.Contains(s, "lib/response.js") {
		t.Errorf("the unpairable files must be named so the reader can act:\n%s", s)
	}
	if !strings.Contains(s, "--tests") {
		t.Errorf("the report must point at the way out (--tests map):\n%s", s)
	}
}

// The genuine docs-only case is unchanged: nothing changed that corral audits,
// so "no audit was needed" is the honest answer and must survive.
func TestPrintRepoReportDocsOnlyDiffStillSaysNoAuditNeeded(t *testing.T) {
	rep := reposcan.Aggregate("local", "r", "c", 0, 0, nil, nil)

	var out bytes.Buffer
	printRepoReport(&out, rep, true, nil, nil, nil, time.Time{})
	s := out.String()
	if !strings.Contains(s, "no audit was needed") {
		t.Errorf("a docs-only diff must still report that no audit was needed:\n%s", s)
	}
}

// TestPrintWeakFileEmitsTheAuthoredTest pins the artifact that makes a proven
// gap actionable.
//
// `--repo` is the mode the GitHub Action runs. It reported "N proven, catchable
// gap(s)" and dropped the test that produced that number — a test the pool had
// already compiled AND executed against the survivor. A developer reading a CI
// run was told a gap is provable and handed nothing to act on, which is the
// difference between a report and a task.
func TestPrintWeakFileEmitsTheAuthoredTest(t *testing.T) {
	var b strings.Builder
	printWeakFile(&b, reposcan.WeakFile{
		Path: "cmd/corral/main.go", KillRate: 0.33, Survivors: 27, ProvenMissed: 1,
		AuthoredTest: "func TestCorral_ProvesTheGap(t *testing.T) {\n\tt.Fatal(\"boom\")\n}",
	})
	out := b.String()
	if !strings.Contains(out, "TestCorral_ProvesTheGap") {
		t.Fatalf("the authored test must reach the report:\n%s", out)
	}
	if !strings.Contains(out, "RAN it to prove the gap") {
		t.Fatalf("the output must say the test was executed, not merely written:\n%s", out)
	}
}

// TestPrintWeakFileOmitsAuthoredTestWhenNothingWasProven: on TimedOut /
// TestWriterFailed / PoolTestUnsound, ProvenMissed is not meaningful, and
// printing a test beside a marker explaining that nothing graded would invite
// a reader to trust an artifact the run itself disclaims.
func TestPrintWeakFileOmitsAuthoredTestWhenNothingWasProven(t *testing.T) {
	var b strings.Builder
	printWeakFile(&b, reposcan.WeakFile{
		Path: "x.go", KillRate: 0, Survivors: 4, ProvenMissed: 0, PoolTestUnsound: true,
		AuthoredTest: "func TestShouldNotAppear(t *testing.T) {}",
	})
	if strings.Contains(b.String(), "TestShouldNotAppear") {
		t.Fatalf("must not print an authored test that proved nothing:\n%s", b.String())
	}
}

// TestCertifyRepoDiffBaseScopesAChangedTestToItsSource: a pull request that
// changes ONLY a test file must still be audited.
//
// This was the gate's blind spot, and it was aimed at its own thesis. Scoping
// on the source path alone meant a PR that deleted assertions touched no
// candidate, so the audit printed "NOTHING IN SCOPE" and passed green — while
// the suite it was supposedly guarding had just been gutted. Weakening a suite
// is the pure form of "tests that pass and defend nothing"; a gate blind to
// precisely that is worse than no gate, because it certifies the change.
//
// Found by opening a real pull request against a real repository that deleted
// the assertions pinning its central guarantee, and watching the check go
// green.
func TestCertifyRepoDiffBaseScopesAChangedTestToItsSource(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	// Only the TEST changes — the shape of a PR that weakens a suite.
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg // assertions removed\n")
	gitRun("add", "pkg/a_test.go")
	gitRun("commit", "-q", "-m", "weaken the tests", "--no-gpg-sign")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic", "pkg/b.go": "must not panic either"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant,
		"--critic-model", "off", "--diff-base", base, "--goals", goals, "--dry-run",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "NOTHING IN SCOPE") {
		t.Fatalf("a PR that gutted pkg/a_test.go was reported as nothing to audit:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "1 job(s)") {
		t.Errorf("expected the changed test to put pkg/a.go in scope (and only it):\n%s", out.String())
	}
}

// TestCertifyRepoDiffBaseRefusesAPRThatDeletesATest is the sixth (Action)
// review's first finding: the guard above covers a test that is EDITED.
// A test that is DELETED walked around it — the source it defended is not
// in the diff, is no longer paired (Enumerate excluded it, its test being
// gone), and is not covered by evidence either — so `git rm pkg/a_test.go`
// read as NOTHING IN SCOPE, exit 0, under --min-kill-rate 0.9
// --max-proven-missed 0. The orphaned source is now reported NOT AUDITED,
// exit 1, with the deletion named as the cause.
func TestCertifyRepoDiffBaseRefusesAPRThatDeletesATest(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	gitRun("rm", "-q", "pkg/a_test.go")
	gitRun("commit", "-q", "-m", "delete the tests", "--no-gpg-sign")

	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic", "pkg/b.go": "must not panic either"}`)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant,
		"--critic-model", "off", "--diff-base", base, "--goals", goals, "--substrate", substrateWorkspace,
		"--min-kill-rate", "0.9", "--max-proven-missed", "0",
	}, &out, &errb) // not --dry-run: zero jobs means zero model calls, and the real path prints the verdict
	if strings.Contains(out.String(), "NOTHING IN SCOPE") {
		t.Fatalf("a PR that deleted pkg/a_test.go was reported as nothing to audit:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "NOT AUDITED") || !strings.Contains(out.String(), "pkg/a.go") || !strings.Contains(out.String(), "DELETED") {
		t.Errorf("the orphaned source must be reported NOT AUDITED, naming the deletion:\n%s", out.String())
	}
	if code == 0 {
		t.Errorf("exit 0 on a PR that deleted a test file — the gate went green on the change it exists to catch:\n%s", out.String())
	}
}

// TestCertifyRepoDiffBaseHonoursAnExplicitTop: action.yml, the self-audit
// workflow and both docs pages told operators to set `top` "to bound what
// one PR can cost", and on the diff path it did nothing — a 3-file PR under
// --top 1 audited 3. An explicit --top now bounds the diff's candidates,
// and the files cut are printed as NOT audited, never left to read as clean.
func TestCertifyRepoDiffBaseHonoursAnExplicitTop(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(root, "pkg", n+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", n+"_test.go"), "package pkg\n")
	}
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(root, "pkg", n+".go"), "package pkg // changed "+n+"\n")
	}
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "change all three", "--no-gpg-sign")
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "g", "pkg/b.go": "g", "pkg/c.go": "g"}`)

	var out, errb bytes.Buffer
	runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant,
		"--critic-model", "off", "--diff-base", base, "--goals", goals, "--dry-run", "--top", "1",
	}, &out, &errb)
	if !strings.Contains(out.String(), "1 job(s)") {
		t.Errorf("--top 1 on a 3-file diff did not bound the scan to one job:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "NOT auditing 2 changed file(s)") {
		t.Errorf("the two files cut by --top must be named as unaudited:\n%s", out.String())
	}
	// Without --top the diff is the bound, as before.
	out.Reset()
	runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant,
		"--critic-model", "off", "--diff-base", base, "--goals", goals, "--dry-run",
	}, &out, &errb)
	if !strings.Contains(out.String(), "3 job(s)") {
		t.Errorf("without --top the diff should be the bound:\n%s", out.String())
	}
}

// The merge gate that does not flap. A kill rate is a proportion of freshly
// generated mutants: it moves between runs on unchanged code, so a threshold
// set near a healthy value goes red on good work and gets switched off. A
// proven-missed gap is a specific bug the pool DEMONSTRATED the suite misses,
// by writing a test and running it — established by execution, not sampled.
func TestMaxProvenMissedFailsOnADemonstratedGap(t *testing.T) {
	zero, two := 0, 2
	clean := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{
		{Path: "pkg/a.go", KillRate: 0.85, Survivors: 3, ProvenMissed: 0},
	}}
	if got := repoScanExitCode(clean, false, 0, nil, &zero); got != 0 {
		t.Errorf("no proven gap must pass --max-proven-missed 0, got exit %d", got)
	}

	proven := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{
		{Path: "pkg/a.go", KillRate: 0.85, Survivors: 3, ProvenMissed: 3},
	}}
	if got := repoScanExitCode(proven, false, 0, nil, &zero); got == 0 {
		t.Error("three demonstrated gaps passed --max-proven-missed 0")
	}
	// The same run clears a threshold that allows them.
	three := 3
	if got := repoScanExitCode(proven, false, 0, nil, &three); got != 0 {
		t.Errorf("3 gaps must clear --max-proven-missed 3 (a maximum, inclusive), got exit %d", got)
	}
	if got := repoScanExitCode(proven, false, 0, nil, &two); got == 0 {
		t.Error("3 gaps passed --max-proven-missed 2")
	}
}

// ProvenMissed==0 is ambiguous by design (see WeakFile's doc): with survivors
// present and no test that graded, it means nothing was PROVEN — not that the
// suite is clean. A gate keyed on it must fail closed there, or it passes on a
// question nobody answered, which is the failure this tool exists to find.
func TestMaxProvenMissedFailsClosedWhenNothingCouldBeProven(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name string
		f    reposcan.WeakFile
	}{
		{"writer never produced a compiling test", reposcan.WeakFile{
			Path: "pkg/a.go", Survivors: 4, ProvenMissed: 0, TestWriterFailed: true}},
		{"authored test never genuinely graded", reposcan.WeakFile{
			Path: "pkg/a.go", Survivors: 4, ProvenMissed: 0, PoolTestUnsound: true}},
	} {
		r := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{tc.f}}
		if got := repoScanExitCode(r, false, 0, nil, &zero); got == 0 {
			t.Errorf("%s: a 0 that means 'nothing was proven' passed the gate", tc.name)
		}
	}

	// But a genuinely clean file — no survivors at all — must still pass.
	clean := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{
		{Path: "pkg/a.go", KillRate: 1.0, Survivors: 0, ProvenMissed: 0},
	}}
	if got := repoScanExitCode(clean, false, 0, nil, &zero); got != 0 {
		t.Errorf("a file with no survivors must pass, got exit %d", got)
	}
}

// The report has to say which file and why, or a red build is a puzzle.
func TestProvenGapBreachIsReported(t *testing.T) {
	zero := 0
	var out bytes.Buffer
	printRepoReport(&out, reposcan.RepoReport{Audited: 1, Weakest: []reposcan.WeakFile{
		{Path: "pkg/a.go", KillRate: 0.85, Survivors: 3, ProvenMissed: 3},
	}}, false, nil, &zero, nil, time.Time{})
	if !strings.Contains(out.String(), "PROVEN-GAP BREACH") || !strings.Contains(out.String(), "pkg/a.go") {
		t.Errorf("breach not reported with its file:\n%s", out.String())
	}

	var unmeasured bytes.Buffer
	printRepoReport(&unmeasured, reposcan.RepoReport{Audited: 1, Weakest: []reposcan.WeakFile{
		{Path: "pkg/b.go", Survivors: 4, ProvenMissed: 0, TestWriterFailed: true},
	}}, false, nil, &zero, nil, time.Time{})
	if !strings.Contains(unmeasured.String(), "PROVEN-GAP UNMEASURED") {
		t.Errorf("an unprovable 0 must be reported distinctly, not as a pass:\n%s", unmeasured.String())
	}
}

// TestDocsPinTheNewestCutTag: the docs must not advertise an OLDER action tag
// than the newest one released.
//
// Its sibling, TestDocsNeverAdvertiseAnUncutActionTag, catches a pin pointing
// at a tag that does not exist yet. This catches the opposite drift, which has
// happened repeatedly: tags get cut, the docs keep naming an older one, and a
// reader installs something behind what the Releases page calls current. Both
// halves are the same defect — the page and the docs disagreeing about what to
// install — and only one of them was guarded.
//
// Deliberately compares against the newest tag in THIS clone rather than the
// GitHub API: a test that needs the network is a test that gets skipped.
func TestDocsPinTheNewestCutTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := filepath.Join("..", "..")
	out, err := runGit(t, repoRoot, "tag", "-l", "v*", "--sort=-v:refname")
	if err != nil || strings.TrimSpace(out) == "" {
		t.Skipf("no tags in this clone (shallow/tagless): %v", err)
	}
	newest := strings.Fields(out)[0]

	docs := docsAdvertisingAnActionRef(t, repoRoot)
	ref := regexp.MustCompile(`pdbethke/corralai@(v[0-9][A-Za-z0-9._-]*)`)
	for doc, body := range docs {
		for _, m := range ref.FindAllStringSubmatch(body, -1) {
			if m[1] != newest {
				t.Errorf("%s pins %s but %s is cut — a reader following the docs installs something older than the Releases page calls current", doc, m[1], newest)
			}
		}
	}
}

// TestMaxProvenMissedFailsClosedOnATimedOutFile is both the first test to
// exercise --max-proven-missed at all and the fix for the hole in it.
//
// THE HOLE. repoScanExitCode fails a scan when EVERY audited file timed out
// (TimedOut == Audited). Inside the maxProvenMissed loop it then refuses a
// zero it cannot trust — but it enumerated only two of the three ways the pool
// can fail to try: TestWriterFailed and PoolTestUnsound. A file that hit its
// deadline before the writer ran carries ProvenMissed 0, TestWriterFailed
// false and PoolTestUnsound false, so in a MIXED scan — one healthy file, one
// timed out — it sailed through `--max-proven-missed 0` and the gate reported
// pass on a question nobody answered.
//
// That is the silent-no-gate class this function already closes four other
// ways, arriving by a fifth route, and the comment above the loop already
// states the rule it missed: "a zero here is only trustworthy when the pool
// actually got to try."
//
// A timed-out file with a NON-zero ProvenMissed is not caught here on purpose:
// since PoolScored rides the verdict, a run can converge its pool score and
// only then stall, and those proven gaps are real measurements. The threshold
// comparison above handles them like any other.
func TestMaxProvenMissedFailsClosedOnATimedOutFile(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name string
		file reposcan.WeakFile
		want int
	}{
		{
			name: "timed out before the writer ran, survivors unproven",
			file: reposcan.WeakFile{Path: "a.go", KillRate: 0.8, Survivors: 3, ProvenMissed: 0, TimedOut: true},
			want: 1,
		},
		{
			name: "timed out AFTER proving gaps — a real measurement",
			file: reposcan.WeakFile{Path: "a.go", KillRate: 0.8, Survivors: 3, ProvenMissed: 0, TimedOut: true, PoolScored: true},
			want: 0,
		},
		{
			name: "clean file, nothing timed out",
			file: reposcan.WeakFile{Path: "a.go", KillRate: 0.9, Survivors: 0, ProvenMissed: 0},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A MIXED scan: one healthy file plus the one under test, so the
			// all-files-timed-out guard above cannot be what catches it.
			rep := reposcan.RepoReport{
				Audited: 2,
				// Both files graded — this test is about the PROVEN-gap gate,
				// not the nothing-was-graded one.
				GradedFiles: 2,
				TimedOut:    map[bool]int{true: 1, false: 0}[tc.file.TimedOut],
				Weakest: []reposcan.WeakFile{
					{Path: "healthy.go", KillRate: 1.0},
					tc.file,
				},
			}
			if got := repoScanExitCode(rep, false, 0, nil, &zero); got != tc.want {
				t.Errorf("exit = %d, want %d — the gate %s", got, tc.want,
					map[int]string{0: "passed on a question nobody answered", 1: "failed on a real measurement"}[got])
			}
		})
	}
}

// TestRepoScanExitCodeFailsWhenNothingWasGraded closes the last route to a
// false green in this function.
//
// repoScanExitCode's own doc comment states the rule — "a scan that measured
// NOTHING is not a passing scan: exiting 0 would read as green in CI for a repo
// where every single file failed to grade" — and the function then checked
// nothingInScope, Audited == 0 and all-timed-out, but never GradedFiles. An
// all-UNCOVERED scan (no test exercises any audited file) satisfies none of
// those: Audited > 0, TimedOut == 0, kill rate NaN. With no threshold flags it
// returned 0, while printRepoReport printed "NO GRADED FILE: all N audited
// file(s) are UNCOVERED" for the human reading the same run.
//
// The last case is the one that keeps this honest: a scan that DID grade
// something must still pass, or the fix trades a false green for a false red.
func TestRepoScanExitCodeFailsWhenNothingWasGraded(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  reposcan.RepoReport
		want int
	}{
		{
			name: "every audited file uncovered — nothing graded",
			rep:  reposcan.RepoReport{Audited: 2, UncoveredFiles: 2, GradedFiles: 0},
			want: 1,
		},
		{
			name: "some graded, some uncovered",
			rep: reposcan.RepoReport{Audited: 2, UncoveredFiles: 1, GradedFiles: 1,
				Weakest: []reposcan.WeakFile{{Path: "a.go", KillRate: 0.9}}},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := repoScanExitCode(tc.rep, false, 0, nil, nil); got != tc.want {
				t.Errorf("exit = %d, want %d — %s", got, tc.want,
					map[int]string{0: "CI goes green on a scan whose own report says NO GRADED FILE", 1: "a scan that graded a file was failed anyway"}[got])
			}
		})
	}
}

// TestAllMutantsInvalidIsNotAGradedZero closes the last fabricated-zero route
// in the repository mean.
//
// adequacy.Report.KillRate returns a literal 0 when Total == 0, and Total
// deliberately excludes mutants the compile gate rejected. So a file whose
// every mutant failed to build produced DevKillRate 0 — and reposcan's
// aggregation branched only on Uncovered, so that 0 was counted as a GRADED
// file, dragging the repo mean down and printing "0.00 <path> (0 survivor(s))"
// with no marker. An accusation against tests that were never given anything
// to catch.
//
// It is the same fabrication the Uncovered branch exists to refuse, reached by
// a different route: there no test runs the file, here no mutant survived the
// compiler.
func TestAllMutantsInvalidIsNotAGradedZero(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 2, 2, []reposcan.FileResult{
		{
			Job:      reposcan.Job{Path: "real.go"},
			Gradable: true,
			Verdict:  advpool.Verdict{DevKillRate: 0.8, MutantsTotal: 10, DevScored: true},
		},
		{
			Job:      reposcan.Job{Path: "nothing-compiled.go"},
			Gradable: true,
			// Every mutant rejected: MutantsTotal (the graded denominator) is
			// 0, so DevKillRate is a zero nobody measured.
			Verdict: advpool.Verdict{DevKillRate: 0, MutantsTotal: 0, MutantsInvalid: 7, DevScored: true},
		},
	}, nil)

	if rep.UngradableFiles != 1 {
		t.Errorf("UngradableFiles = %d, want 1 — a file whose mutants all failed to build was never graded", rep.UngradableFiles)
	}
	if rep.GradedFiles != 1 {
		t.Errorf("GradedFiles = %d, want 1 — counting the ungradable file makes the mean average a number no measurement supports", rep.GradedFiles)
	}
	if rep.KillRate < 0.79 || rep.KillRate > 0.81 {
		t.Errorf("KillRate = %.3f, want ~0.80 — the repo mean was dragged toward zero by a file that was never graded", rep.KillRate)
	}
}

// TestPrintWeakFileNamesAnUngradableFile: the marker is the other half. A
// reader seeing 0.00 must be told the exam had no questions, or they will read
// it as a suite that caught nothing.
func TestPrintWeakFileNamesAnUngradableFile(t *testing.T) {
	var out bytes.Buffer
	printWeakFile(&out, reposcan.WeakFile{
		Path: "nothing-compiled.go", KillRate: 0, MutantsGraded: 0, MutantsInvalid: 7,
	})
	got := out.String()
	if !strings.Contains(got, "NO GRADABLE MUTANT") {
		t.Errorf("line = %q, want a marker saying the exam had no questions", got)
	}
	if !strings.Contains(got, "7") {
		t.Errorf("line = %q, want the rejected-mutant count so the reader can see how much was thrown away", got)
	}
}

// TestUnpairableDiffFailsTheGate closes a fail-open on the change the gate is
// most often installed to inspect.
//
// A diff whose changed source files cannot be paired with tests selects zero
// candidates, so it arrives at repoScanExitCode AS nothingInScope — whose first
// statement returned 0. Meanwhile printRepoReport printed
//
//	NOT AUDITED: the diff changed N source file(s) corral could not pair with a
//	test … This is a pairing limitation, NOT a clean bill of health
//
// and its own comment states the rule the exit code was breaking: "'no audit was
// needed' here would be a fail-open: the gate goes green on exactly the change it
// was installed to inspect."
//
// Not a corner case. Filename pairing routinely pairs nothing on JS/TS layouts —
// this repository's own foreign sweep pins express at 213 candidates, 0 pairs — so
// on a TypeScript repo this was the COMMON path: honest summary, green check.
//
// The third case is what keeps the fix from overcorrecting: a genuinely
// docs-only diff still passes, because that is the honest, expected outcome and
// failing it would train people to ignore the gate.
func TestUnpairableDiffFailsTheGate(t *testing.T) {
	for _, tc := range []struct {
		name           string
		nothingInScope bool
		unpairable     int
		want           int
	}{
		{"changed files that could not be paired", true, 2, 1},
		{"docs-only diff — nothing in scope, nothing unpaired", true, 0, 0},
		{"unpairable files reported even without the scope flag", false, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := reposcan.RepoReport{Audited: 0}
			if got := repoScanExitCode(rep, tc.nothingInScope, tc.unpairable, nil, nil); got != tc.want {
				t.Errorf("exit = %d, want %d — %s", got, tc.want,
					map[int]string{
						0: "CI goes green on a diff whose own report says NOT AUDITED",
						1: "a docs-only diff was failed, which trains people to ignore the gate",
					}[got])
			}
		})
	}
}

// TestSignableKillRateWithholdsAFabricatedZero: the human report and the SIGNED
// statement must refuse the same numbers.
//
// printRepoReport marks a file whose every mutant was rejected by the compile
// gate as [NO GRADABLE MUTANT], and reposcan's own comment calls that zero "a
// fabrication: nothing was graded". signableKillRate withheld only for Uncovered,
// so the statement carried "killRate":0 — and the --attest help promises rates
// "WITH the honesty flags that say what a zero means", while certify.AuditedFile
// has no flag for this state. Absence is the honest carrier.
func TestSignableKillRateWithholdsAFabricatedZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    reposcan.WeakFile
		want bool // want a signed rate?
	}{
		{"every mutant rejected by the compile check", reposcan.WeakFile{MutantsGraded: 0, MutantsInvalid: 7}, false},
		{"uncovered", reposcan.WeakFile{Uncovered: true, MutantsGraded: 4}, false},
		{"genuinely graded zero — the suite really caught nothing", reposcan.WeakFile{KillRate: 0, MutantsGraded: 9}, true},
		{"ordinary graded file", reposcan.WeakFile{KillRate: 0.8, MutantsGraded: 10}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := signableKillRate(tc.f)
			if (got != nil) != tc.want {
				t.Errorf("signed rate present = %v, want %v — %s", got != nil, tc.want,
					map[bool]string{
						true:  "a zero denominator was signed as a measurement",
						false: "a REAL measured zero was withheld, which hides the worst finding corral can make",
					}[got != nil])
			}
		})
	}
}

// looksLikeATestPath is the cheap pre-evidence question the --diff-base bound
// asks: could this changed file be a test? Generous on purpose — a false yes
// costs one instrumented run, a false no is the false green.
func TestLooksLikeATestPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"tests/test_behaviour.py", true}, {"test/calc_test.rb", true}, {"spec/calc_spec.rb", true},
		{"src/__tests__/calc.test.ts", true}, {"lib/calc_test.go", true}, {"tests/CalcTest.php", true},
		{"pkg/calc.py", false}, {"lib/calc.js", false}, {"README.md", false}, {"internal/testing_helpers.go", false},
	} {
		if got := looksLikeATestPath(tc.path); got != tc.want {
			t.Errorf("looksLikeATestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
