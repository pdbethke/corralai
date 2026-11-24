// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestReasonGlossDistinguishesFaultFromFact pins the point of the gloss: a
// reader must be able to tell corral failing from a file with nothing to audit,
// from an invocation that cannot reach the code. Printed as bare codes, all
// three read as failures — on spf13/afero four CORRECT calls looked like four
// crashes.
func TestReasonGlossDistinguishesFaultFromFact(t *testing.T) {
	notAFailure := []string{
		reposcan.ReasonTestCmdCannotCollect,
		reposcan.ReasonUngoaled,
	}
	for _, r := range notAFailure {
		g := reasonGloss(r)
		if g == "" {
			t.Errorf("%s has no gloss: it will read as a crash", r)
			continue
		}
		if !strings.Contains(g, "NOT a corral failure") && !strings.Contains(g, "nothing a mutant could violate") {
			t.Errorf("%s gloss does not say it is not a failure: %q", r, g)
		}
	}

	// The genuine failure must still read as one.
	if g := reasonGloss(reposcan.ReasonExecutorError); !strings.Contains(g, "failed to run") {
		t.Errorf("executor-error gloss should still read as a failure, got %q", g)
	}
}

// An unknown reason gets NO gloss rather than a guessed one — a confident
// explanation of a disposition we do not recognize is worse than silence.
func TestReasonGlossSilentOnUnknown(t *testing.T) {
	if g := reasonGloss("some-future-reason"); g != "" {
		t.Fatalf("unknown reason glossed as %q, want empty", g)
	}
}

// The preflight refusal must carry its own disposition through the scan, not be
// flattened into executor-error.
func TestNotCollectedErrorCarriesItsScanReason(t *testing.T) {
	err := auditNotCollectedErr("your test command would not run the test this audit writes")
	rc, ok := err.(reposcan.ReasonCarrier)
	if !ok {
		t.Fatal("the refusal does not implement reposcan.ReasonCarrier")
	}
	if got := rc.ScanReason(); got != reposcan.ReasonTestCmdCannotCollect {
		t.Fatalf("ScanReason() = %q, want %q", got, reposcan.ReasonTestCmdCannotCollect)
	}
	// An ordinary audit error must keep the catch-all.
	if rc2, ok := auditErr("boom").(reposcan.ReasonCarrier); ok && rc2.ScanReason() != "" {
		t.Fatalf("ordinary audit error claims reason %q, want none", rc2.ScanReason())
	}
}

// A collect refusal on certify --repo comes after the file's goal was
// derived, so neither the summary gloss nor the per-file refusal may claim
// nothing was spent. Both did, above a cost line of 78.1k tokens
// (ramsey/uuid, 2026-11-15).
func TestCollectRefusalNeverClaimsNothingWasSpent(t *testing.T) {
	for name, text := range map[string]string{
		"gloss":   reasonGloss(reposcan.ReasonTestCmdCannotCollect),
		"refusal": notCollectedAdvice,
	} {
		if strings.Contains(strings.ToLower(text), "nothing was spent") {
			t.Errorf("%s claims nothing was spent, which is false on certify --repo: %q", name, text)
		}
	}
}

// TestThePreflightRefusesEachUnreachedOutcomeForItsOwnReason: only a control
// run that PASSED shows the command does not run corral's test, so only it
// is refused as test-cmd-cannot-collect with the "widen your command"
// advice. For an inline landing that advice is about the file itself, not a
// file beside the developer's. A timeout and a failure that did not name the
// reach canary are said as such and recorded under their own reason, as is a
// runner whose failures corral cannot read: before this, every one of them
// printed "corral writes its killing test beside your own … `-- pytest
// tests/`" and recorded test-cmd-cannot-collect.
func TestThePreflightRefusesEachUnreachedOutcomeForItsOwnReason(t *testing.T) {
	inline := advpool.AuthoredLanding{Landing: pairing.Landing{Path: "src/lib.rs", Shape: pairing.Inline, Into: true, DeclarationsRead: true, OwnModule: true}}
	for _, tc := range []struct {
		name    string
		landing advpool.AuthoredLanding
		code    string
		reach   advpool.Reach
		reason  string
		says    []string
		notSays []string
	}{
		{"reached", inline, "src/lib.rs", advpool.Reach{Outcome: advpool.ReachReached}, "", nil, nil},
		{"file of its own, run passed", advpool.AuthoredLanding{}, "pkg/a.py", advpool.Reach{Outcome: advpool.ReachRunPassed}, reposcan.ReasonTestCmdCannotCollect,
			[]string{"corral writes its killing test beside your own"}, nil},
		{"inline, run passed", inline, "src/lib.rs", advpool.Reach{Outcome: advpool.ReachRunPassed}, reposcan.ReasonTestCmdCannotCollect,
			[]string{"appended to src/lib.rs itself", "name filter"}, []string{"beside your own", "pytest"}},
		{"inline, timed out", inline, "src/lib.rs", advpool.Reach{Outcome: advpool.ReachTimedOut, Detail: "the run timed out"}, reposcan.ReasonReachControlInconclusive,
			[]string{"timed out"}, []string{"beside your own", "pytest", "Widen"}},
		{"inline, failed without the canary", inline, "src/lib.rs", advpool.Reach{Outcome: advpool.ReachFailedWithoutCanary, Detail: "the run failed without the reach canary failing: error: unused import"}, reposcan.ReasonReachControlInconclusive,
			[]string{"error: unused import", "`-- cargo test`"}, []string{"beside your own", "pytest", "Widen"}},
		{"inline, runner unreadable", inline, "src/lib.rs", advpool.Reach{Outcome: advpool.ReachRunnerUnreadable, Detail: "the runner of [x] names no failing test"}, reposcan.ReasonTestCmdFailuresUnreadable,
			[]string{"names no failing test"}, []string{"beside your own", "pytest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := preflightRefusal(tc.reach, tc.landing, tc.code, "")
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("a reached control was refused: %v", err)
				}
				return
			}
			rc, ok := err.(reposcan.ReasonCarrier)
			if !ok || rc.ScanReason() != tc.reason {
				t.Fatalf("refusal %v; want reason %q", err, tc.reason)
			}
			for _, w := range tc.says {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal does not say %q:\n%s", w, err)
				}
			}
			for _, w := range tc.notSays {
				if strings.Contains(err.Error(), w) {
					t.Errorf("refusal says %q, which is not this outcome's diagnosis:\n%s", w, err)
				}
			}
			if strings.Contains(strings.ToLower(err.Error()), "nothing was spent") || reasonGloss(tc.reason) == "" {
				t.Errorf("refusal claims nothing was spent, or its reason has no gloss: %q / %q", err, reasonGloss(tc.reason))
			}
		})
	}
}
