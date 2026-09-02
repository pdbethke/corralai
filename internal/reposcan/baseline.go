// SPDX-License-Identifier: Elastic-2.0

package reposcan

import "fmt"

// BaselineRunner runs a candidate's UNMUTATED test suite once.
type BaselineRunner interface {
	RunBaseline() (pass bool, err error)
}

// CheckBaselineStable runs the unmutated suite `runs` times and reports
// whether the outcome was consistent. A flapping suite cannot be graded: a
// flaky test makes a mutant look killed or survived at random, so any kill
// rate derived from it is a coin flip. Consistent failure is STABLE (that is
// the existing BaselineFailed case) — only disagreement is instability.
func CheckBaselineStable(r BaselineRunner, runs int) (bool, error) {
	if runs < 2 {
		return false, fmt.Errorf("reposcan: baseline stability needs at least 2 runs, got %d", runs)
	}
	first, err := r.RunBaseline()
	if err != nil {
		return false, err
	}
	for i := 1; i < runs; i++ {
		got, err := r.RunBaseline()
		if err != nil {
			return false, err
		}
		if got != first {
			return false, nil
		}
	}
	return true, nil
}
