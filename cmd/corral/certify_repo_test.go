// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
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
	if strings.Contains(err.Error(), "exit status 128") && !strings.Contains(err.Error(), "no-such-ref-at-all") {
		t.Errorf("error discarded git's own explanation, left only the exit code: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown revision") && !strings.Contains(err.Error(), "bad revision") {
		t.Errorf("want git's stderr explanation in the error, got: %v", err)
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
	code := runCertifyRepo([]string{"--repo", root, "--diff-base", base, "--goals", goals, "--dry-run"}, &out, &errb)
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
		"--repo", root, "--diff-base", base, "--goals", goals,
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
		"--repo", root, "--diff-base", base, "--goals", goals,
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

// TestCertifyRepoRejectsUnknownSubstrate proves an unrecognized --substrate
// value is a usage error (exit 2), never a silent fall-through to the jail
// default — a run that quietly used the wrong substrate while claiming the
// other is exactly the accountability failure this branch closes.
func TestCertifyRepoRejectsUnknownSubstrate(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--substrate", "docker", "--dry-run"}, &out, &errb)
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
		code := runCertifyRepo([]string{"--repo", root, "--substrate", s, "--dry-run"}, &out, &errb)
		if code != 0 {
			t.Fatalf("--substrate %s: exit %d, stderr=%s", s, code, errb.String())
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

	ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, io.Discard)
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
		ex := newLocalExecutor(t.TempDir(), nil, substrate, io.Discard)
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
	code := runCertifyRepo([]string{"--repo", root, "--dry-run"}, &out, &errb)
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
	code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--dry-run"}, &out, &errb)
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
	if code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--dry-run"}, &out, &errb); code != 0 {
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
	if code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--dry-run"}, &out, &errb); code != 0 {
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
	if got := repoScanExitCode(nothing, false); got == 0 {
		t.Errorf("a scan that graded nothing must exit non-zero, got %d", got)
	}

	graded := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.9}},
	}, nil)
	if got := repoScanExitCode(graded, false); got != 0 {
		t.Errorf("a scan that graded something must exit 0, got %d", got)
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
	if got := repoScanExitCode(emptyScope, true); got != 0 {
		t.Errorf("an empty diff scope must exit 0 (nothing to audit), got %d", got)
	}

	nothingGradable := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonBaselineFailed},
	}, nil)
	if got := repoScanExitCode(nothingGradable, false); got != 1 {
		t.Errorf("a non-empty scope where nothing graded must exit 1, got %d", got)
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
	code := runCertifyRepo([]string{"--repo", root, "--goals", filepath.Join(root, "nope.json"), "--dry-run"}, &out, &errb)
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
}

func (s *scriptedBaseline) RunBaseline() (bool, error) {
	if s.n >= len(s.results) {
		return false, errors.New("scriptedBaseline: out of scripted results")
	}
	v := s.results[s.n]
	s.n++
	return v, nil
}

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

	ex := newLocalExecutor(repo, nil, "", io.Discard)
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
		if p, err := prepareAuditJail(in, plug, time.Minute, io.Discard); err == nil {
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
	printRepoReport(&out, rep, false)
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
	printRepoReport(&scoped, rep, true)
	if strings.Contains(scoped.String(), "COULD-NOT-GRADE") {
		t.Errorf("an empty diff scope must not print COULD-NOT-GRADE:\n%s", scoped.String())
	}
	if !strings.Contains(scoped.String(), "NOTHING IN SCOPE") {
		t.Errorf("want a distinct NOTHING IN SCOPE line, got:\n%s", scoped.String())
	}

	var notScoped bytes.Buffer
	printRepoReport(&notScoped, rep, false)
	if strings.Contains(notScoped.String(), "NOTHING IN SCOPE") {
		t.Errorf("the non-diff/nothing-gradable case must not print the scope line:\n%s", notScoped.String())
	}
	if !strings.Contains(notScoped.String(), "COULD-NOT-GRADE") {
		t.Errorf("want COULD-NOT-GRADE, got:\n%s", notScoped.String())
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
	printRepoReport(&out, reposcan.Aggregate("o", "r", "c", 12, len(results), results, nil), false)
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
	code := runCertify([]string{"--repo", root, "--goals", goals, "--dry-run"},
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
	code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--dry-run", "--", "go", "test", "./..."}, &out, &errb)
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
	code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--dry-run", "--", "go", "test", "./..."}, &out, &errb)
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
	printRepoReport(&out, rep, false)
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
	printRepoReport(&first, rep, false)
	for i := 0; i < 50; i++ {
		var again bytes.Buffer
		printRepoReport(&again, rep, false)
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
	code := runCertify([]string{"--repo", root, "--dry-run", "--", "true"},
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
	ex := newLocalExecutor(t.TempDir(), nil, "", io.Discard)
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
	ex := newLocalExecutor(t.TempDir(), nil, "", io.Discard)
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
	ex := newLocalExecutor(t.TempDir(), nil, "", io.Discard)
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
	ex := newLocalExecutor(t.TempDir(), nil, "", io.Discard)
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
	ex := newLocalExecutor(t.TempDir(), nil, "", io.Discard)
	defer ex.Close()
	if ex.seeds == nil {
		t.Fatal("no seed cache: every job would prepare its own jail")
	}
}

// The cache must be wired even when the host cannot sandbox: jailErr is
// reported by preflight, and a nil cache would silently change the fan-out's
// prep strategy rather than fail.
func TestNewLocalExecutorWiresASeedCacheEvenWhenTheJailIsUnavailable(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), nil, "", io.Discard)
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
	code := runCertifyRepo([]string{"--repo", root, "--top", "1", "--dry-run"}, &out, &errb)
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
	code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--dry-run"}, &out, &errb)
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
	if code := runCertifyRepo([]string{"--repo", root, "--goals", goalsFile, "--dry-run"}, &out, &errb); code != 0 {
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
	if code := runCertifyRepo([]string{"--repo", root, "--goals", goalsFile, "--top", "2", "--dry-run"}, &out, &errb); code != 0 {
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
	gs, disclosure, code := resolveGoalSource(&errb, t.TempDir(), "", "test-model-x", false, 3,
		func(model string) (reposcan.Deriver, error) {
			called++
			if model != "test-model-x" {
				t.Errorf("factory got model %q", model)
			}
			return stubDeriver{}, nil
		})
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
	gs, disclosure, code := resolveGoalSource(&errb, root, goals, "test-model-x", false, 3,
		func(string) (reposcan.Deriver, error) {
			t.Fatal("the --goals path must never construct a deriver")
			return nil, nil
		})
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
	gs, disclosure, code := resolveGoalSource(&errb, t.TempDir(), "", "test-model-x", false, 0,
		func(string) (reposcan.Deriver, error) {
			t.Fatal("no candidate was selected; a deriver must not be built")
			return nil, nil
		})
	if code != 0 || gs == nil || disclosure != "" {
		t.Fatalf("code=%d gs=%v disclosure=%q stderr=%s", code, gs, disclosure, errb.String())
	}
}

// A missing credential is a USAGE error (exit 2), reported before any spend.
func TestResolveGoalSourceDeriverFailureIsAUsageError(t *testing.T) {
	var errb bytes.Buffer
	gs, _, code := resolveGoalSource(&errb, t.TempDir(), "", "test-model-x", false, 3,
		func(string) (reposcan.Deriver, error) { return nil, errors.New("goal deriver: no key") })
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
	if code := runCertifyRepo([]string{"--repo", root, "--dry-run"}, &out, &errb); code != 0 {
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

// --all audits every candidate, ignoring the default bound.
func TestCertifyRepoAllIgnoresTheDefaultBound(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < defaultScanTop+3; i++ {
		name := fmt.Sprintf("f%02d", i)
		mustWrite(t, filepath.Join(root, "pkg", name+".go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", name+"_test.go"), "package pkg\n")
	}
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", root, "--all", "--dry-run"}, &out, &errb); code != 0 {
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
		if code := runCertifyRepo([]string{"--repo", root, "--top", top, "--dry-run"}, &out, &errb); code != 0 {
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
