// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

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
