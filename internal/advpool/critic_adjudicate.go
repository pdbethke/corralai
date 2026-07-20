// SPDX-License-Identifier: Elastic-2.0
package advpool

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
