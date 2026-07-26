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
	// 6 files on disk (5 source + README) + goals.json itself.
	if !strings.Contains(s, "6 file(s) excluded") {
		t.Errorf("want 6 exclusions (4 above + b_test.go + goals.json):\n%s", s)
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
func TestLocalExecutorExecuteBaselineFailedIsUngradable(t *testing.T) {
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{false, false}}, func() {}, nil
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
	rep := reposcan.Aggregate("local", "r", "c", 3, []reposcan.FileResult{
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
	printRepoReport(&out, reposcan.Aggregate("o", "r", "c", 12, results, nil))
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
