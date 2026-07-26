// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

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

func TestCertifyRepoRequiresGoalsFile(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", t.TempDir()}, &out, &errb)
	if code == 0 {
		t.Fatal("missing --goals should be an error in H1a")
	}
	if !strings.Contains(errb.String(), "--goals is required") {
		t.Errorf("stderr = %q", errb.String())
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
// every file failed to grade must not read as green to CI.
func TestRepoScanExitCodeNothingAuditedIsNonZero(t *testing.T) {
	nothing := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonExecutorError},
	}, nil)
	if got := repoScanExitCode(nothing); got == 0 {
		t.Errorf("a scan that graded nothing must exit non-zero, got %d", got)
	}

	graded := reposcan.Aggregate("o", "r", "c", 2, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.9}},
	}, nil)
	if got := repoScanExitCode(graded); got != 0 {
		t.Errorf("a scan that graded something must exit 0, got %d", got)
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

// TestPrintRepoReportNothingAuditedSaysSo proves the never-fabricate-a-score
// invariant at the presentation layer: with nothing audited the report must
// say COULD-NOT-GRADE, not print a 0.00 kill rate (or a NaN).
func TestPrintRepoReportNothingAuditedSaysSo(t *testing.T) {
	var out bytes.Buffer
	rep := reposcan.Aggregate("local", "r", "c", 3, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: false, Reason: reposcan.ReasonFlakyBaseline},
	}, []reposcan.Exclusion{{Path: "b.go", Reason: reposcan.ReasonNoPairedTest}})
	printRepoReport(&out, rep)
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
	printRepoReport(&out, reposcan.Aggregate("o", "r", "c", 12, len(results), results, nil))
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
	if strings.Contains(stderr.String(), "--goals is required") {
		t.Errorf("legacy --repo was hijacked by the repo scan:\n%s", stderr.String())
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
	rep := reposcan.Aggregate("o", "r", "c", 12, 5, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.8}},
	}, nil)
	var out bytes.Buffer
	printRepoReport(&out, rep)
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
	rep := reposcan.Aggregate("o", "r", "c", 9, 6, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1}},
		{Job: reposcan.Job{Path: "b.go"}, Gradable: false, Reason: reposcan.ReasonFlakyBaseline},
		{Job: reposcan.Job{Path: "c.go"}, Gradable: false, Reason: reposcan.ReasonBaselineFailed},
		{Job: reposcan.Job{Path: "d.go"}, Gradable: false, Reason: reposcan.ReasonExecutorError},
		{Job: reposcan.Job{Path: "e.go"}, Gradable: false, Reason: reposcan.ReasonCancelled},
	}, nil)

	var first bytes.Buffer
	printRepoReport(&first, rep)
	for i := 0; i < 50; i++ {
		var again bytes.Buffer
		printRepoReport(&again, rep)
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

// TestRunCertifyRepoDirWithoutGoalsRefuses is Finding I6: `--repo <dir>` with
// --goals forgotten silently certified the CURRENT directory and stamped the
// other repo's path onto the record — a signed statement about the wrong
// subject. It must refuse and point at the scan.
func TestRunCertifyRepoDirWithoutGoalsRefuses(t *testing.T) {
	root := t.TempDir()
	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	code := runCertify([]string{"--repo", root, "--", "true"},
		run, post, fakeJail{exit: 0, out: "ok"},
		func() (ed25519.PrivateKey, error) { return nil, errors.New("unused") },
		&stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if run.ranArgv != nil || post.called {
		t.Error("a --repo <dir> typo must not run the check or post a record")
	}
	if !strings.Contains(stderr.String(), "--goals") {
		t.Errorf("the error must point at the missing --goals:\n%s", stderr.String())
	}
}

// The same guard must also catch the --repo=<dir> spelling.
func TestRunCertifyRepoDirEqualsFormWithoutGoalsRefuses(t *testing.T) {
	root := t.TempDir()
	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	code := runCertify([]string{"--repo=" + root, "--", "true"},
		run, post, fakeJail{exit: 0, out: "ok"},
		func() (ed25519.PrivateKey, error) { return nil, errors.New("unused") },
		&stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, stderr.String())
	}
}
