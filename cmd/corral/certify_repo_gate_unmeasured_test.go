// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// --max-proven-missed 0 must not PASS a file where nothing was graded.
//
// Every mutant rejected by the compile gate leaves MutantsGraded 0,
// Survivors 0 and ProvenMissed 0 — bit-identical to "measured, and nothing
// survived" — so the gate reported a green merge check for an audit that
// answered nothing. A user's CI gate saying pass is corral saying something
// false about their code, which is the one thing it must not do.
// Found by a cold review of certify_repo.go, 2026-09-08 (round five, R1).
func TestMaxProvenMissedRefusesAFileNothingWasGradedOn(t *testing.T) {
	nothingGraded := reposcan.WeakFile{
		Path:           "app/thing.go",
		MutantsGraded:  0,
		MutantsInvalid: 12, // every one rejected by the compile gate
		Survivors:      0,
		ProvenMissed:   0,
	}
	if !noGradableMutant(nothingGraded) {
		t.Fatal("fixture: this is meant to BE the compile-gate zero denominator")
	}

	measuredClean := reposcan.WeakFile{
		Path:          "app/other.go",
		MutantsGraded: 12,
		Survivors:     0,
		ProvenMissed:  0,
	}
	if noGradableMutant(measuredClean) {
		t.Fatal("fixture: a genuinely clean file must not read as ungraded")
	}

	zero := 0
	report := func(f reposcan.WeakFile) reposcan.RepoReport {
		return reposcan.RepoReport{Weakest: []reposcan.WeakFile{f}, Audited: 1}
	}
	if got := repoScanExitCode(report(nothingGraded), false, 0, nil, &zero); got == 0 {
		t.Error("a file whose every mutant was rejected passed --max-proven-missed 0: the gate answered a question nobody could answer")
	}
	if got := repoScanExitCode(report(measuredClean), false, 0, nil, &zero); got != 0 {
		t.Errorf("a genuinely clean, fully graded file failed the gate (exit %d) — the fix must not turn a real pass into a failure", got)
	}
}
