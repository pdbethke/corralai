// SPDX-License-Identifier: Elastic-2.0

// Package adequacy implements the deterministic mutation-testing adequacy
// scorer for the control-gate control loop: it measures how well a candidate
// test "bites" by running it against compliant code (must pass) and against
// a set of goal-violating mutants (each catch = a kill), reporting the kill
// rate and the surviving (uncaught) mutants.
//
// Scoring is pure and deterministic: no LLM, no network, no time.Now(). The
// only external dependency is the injected Jail, which runs a test command
// against a set of files and reports whether it passed.
package adequacy

import "context"

// Mutant is a single goal-violating variant of the code under test.
type Mutant struct {
	ID   string
	Code string
	// ParentSHA256 is the hex SHA-256 of the EXACT original code this mutant was
	// derived from (empty for hand-built test fixtures). It ties each mutant to
	// the precise bytes under audit: a mutant is a faithful single-point edit of
	// that original, so `sha256(original) == ParentSHA256` and the recorded
	// patch re-applies to reproduce Code. Set by testgen's patch applier, which
	// drops any mutant that cannot be proven a clean single-region derivative.
	ParentSHA256 string
}

// Jail runs a test command against a set of files (path -> content) in an
// isolated environment and reports whether the test passed. Task 1 exercises
// this via a fake; Task 2 wires in the real bwrap-jail adapter.
type Jail interface {
	RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error)
}

// Report is the outcome of scoring a candidate test against compliant code
// and a set of mutants.
type Report struct {
	CompliantPass bool
	Total         int
	Killed        []string
	Survived      []string
}

// KillRate is the adequacy score: the fraction of mutants the test caught.
// Zero when no mutants were run (including when the report is invalid
// because the test failed on compliant code).
func (r Report) KillRate() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(len(r.Killed)) / float64(r.Total)
}

// Score runs a candidate test (identified by testCmd, operating on the file
// tree in base plus the code under test at codePath) against the compliant
// code and then against each mutant in turn, reporting kills and survivors.
//
// Fail-safe: if the test does not pass against the compliant code, it is
// broken or overreaching, and the report is marked invalid (CompliantPass
// false) with no mutants run — an invalid test must never earn a kill rate.
//
// Determinism: mutants are run in slice order, and Killed/Survived are
// appended in that same order (never collected via a map) so the report is
// reproducible.
func Score(ctx context.Context, j Jail, base map[string]string, codePath, compliantCode string, mutants []Mutant, testCmd []string) (Report, error) {
	run := func(code string) (bool, error) {
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return j.RunTest(ctx, files, testCmd)
	}

	pass, err := run(compliantCode)
	if err != nil {
		return Report{}, err
	}
	rep := Report{CompliantPass: pass}
	if !pass {
		// broken/overreaching test — do not score mutants (fail-safe: no kill rate for an invalid test)
		return rep, nil
	}

	rep.Total = len(mutants)
	for _, m := range mutants {
		passed, err := run(m.Code)
		if err != nil {
			return Report{}, err
		}
		if passed { // test PASSED on a violation => it did NOT catch it
			rep.Survived = append(rep.Survived, m.ID)
		} else { // test FAILED on the violation => caught (killed)
			rep.Killed = append(rep.Killed, m.ID)
		}
	}
	return rep, nil
}
