// SPDX-License-Identifier: Elastic-2.0
package advpool

import "github.com/pdbethke/corralai/internal/matrix"

const (
	AdjUnadjudicated = "unadjudicated"
	AdjConfirmed     = "confirmed"
	AdjRefuted       = "refuted"
	ScopeWholeTest   = "whole-test"
	ScopeDeadCheck   = "dead-check"
)

func NormalizeScope(s string) string {
	if s == ScopeWholeTest {
		return ScopeWholeTest
	}
	return ScopeDeadCheck // "", unknown, or dead-check all collapse here (fail-safe)
}

// AutoAdjudication is sound by construction: it only ever downgrades a
// whole-test "can never fail" claim to refuted when execution PROVED the test
// can fail (kills>=1). dead-check claims are never auto-touched (a live test
// killing mutants says nothing about whether one internal check is dead), and
// no path ever auto-confirms (0 kills is inconclusive, not proof of vacuity).
func AutoAdjudication(scope string, ran bool, kills int) string {
	if NormalizeScope(scope) == ScopeWholeTest && ran && kills >= 1 {
		return AdjRefuted
	}
	return AdjUnadjudicated
}

// matrixRowFor finds the tests×mutants matrix row for a critic finding's
// TestSelector, nil if the matrix never scored that selector (e.g. it was
// not among the enumerated tests).
func matrixRowFor(rows []matrix.TestAdequacy, selector string) *matrix.TestAdequacy {
	for i := range rows {
		if rows[i].Selector == selector {
			return &rows[i]
		}
	}
	return nil
}

// matrixAdjudication is AutoAdjudication's matrix-driven sibling: unlike
// AutoAdjudication (which never auto-confirms — a single re-scored test in
// isolation has no way to know whether ANY test could have caught the gap),
// the matrix DOES know: catchable is true iff some OTHER scored test in the
// same run killed at least one mutant, which is the soundness floor that
// makes "this specific test caught nothing, and something else proves the
// mutants were catchable at all" a sound zero-kill confirmation. A test that
// could not even be scored (row.Scored false — e.g. its baseline couldn't
// pass) is never adjudicated either way: NOT running is not evidence.
func matrixAdjudication(row matrix.TestAdequacy, catchable bool) string {
	switch {
	case row.Scored && row.Kills >= 1:
		return AdjRefuted
	case row.Scored && row.Kills == 0 && catchable:
		// NOT a confirmation. `catchable` says some OTHER test killed the
		// mutants; it says nothing about whether THIS test can ever fail.
		// A test asserting different behavior legitimately kills none of
		// these mutants without being vacuous, and the critic's claim is
		// about the test, not about the mutant set.
		//
		// It mattered because this fed the LEADERBOARD: a confirmed row
		// records OutcomePass for the critic seat (driver.go), so a critic
		// earned gate-earned fitness from a confirmation execution had not
		// established — in a system whose rule is that fitness comes from
		// execution and never from an assertion.
		//
		// Execution can DISPROVE "this test can never fail" — it killed
		// something — and cannot prove it. So this path is refute-only now,
		// like the review harness, which demotes and never promotes. The
		// consequence is deliberate and worth stating: a critic can lose
		// standing here and cannot gain it, until there is a signal that
		// actually bears on the claim (the mutants this test's own coverage
		// reaches, rather than any mutant at all).
		//
		// Found by a cold review of internal/advpool, 2026-09-08, and let
		// stand by its verifier.
		return AdjUnadjudicated
	default:
		return AdjUnadjudicated
	}
}
