// SPDX-License-Identifier: Elastic-2.0

// Package adequacy implements the deterministic mutation-testing adequacy
// scorer for the control-gate control loop: it measures how well a candidate
// test "bites" by running it against compliant code (must pass) and against
// a set of goal-violating mutants (each catch = a kill), reporting the kill
// rate and the surviving (uncaught) mutants.
//
// Scoring is deterministic in its VERDICTS (kill/survive/pass never depend on
// wall-clock, only on what the Jail reports) and has no LLM or network calls.
// It does read time.Now() once, around the compliant-baseline run, solely to
// auto-derive a short per-mutant timeout from how long a healthy suite
// actually took (see clampMutantTimeout) — that derived duration only ever
// widens or narrows a timeout window, it never changes a kill/survive
// verdict for a run that completes. The only external dependency for the
// verdicts themselves is the injected Jail, which runs a test command
// against a set of files and reports whether it passed.
package adequacy

import (
	"context"
	"errors"
	"time"
)

// minMutantTimeout / maxMutantTimeout bound the auto-derived per-mutant
// timeout (see scoreConfig.mutantTimeout / clampMutantTimeout): a floor so a
// pathologically fast healthy suite (sub-second) still gives a mutant enough
// room to run at all, and a ceiling so a pathologically slow healthy suite
// doesn't turn "8x baseline" into a per-mutant wait that is itself most of
// the whole-run budget.
const (
	minMutantTimeout = 30 * time.Second
	maxMutantTimeout = 5 * time.Minute
	// mutantTimeoutMultiple is how many multiples of the healthy baseline's
	// own wall-clock a mutant run gets before it is treated as non-terminating.
	mutantTimeoutMultiple = 8
)

// clampMutantTimeout derives the per-mutant timeout from how long the
// compliant baseline actually took to run: mutantTimeoutMultiple x that,
// clamped to [minMutantTimeout, maxMutantTimeout]. This auto-adapts to any
// repo's suite with no operator tuning.
func clampMutantTimeout(baseDur time.Duration) time.Duration {
	d := baseDur * mutantTimeoutMultiple
	if d < minMutantTimeout {
		return minMutantTimeout
	}
	if d > maxMutantTimeout {
		return maxMutantTimeout
	}
	return d
}

// ScoreOption configures a single Score call. The zero value of every option
// preserves auto-derive behavior.
type ScoreOption func(*scoreConfig)

type scoreConfig struct {
	mutantTimeout time.Duration
}

// WithMutantTimeout overrides the auto-derived per-mutant timeout with an
// explicit cap. d <= 0 restores auto-derive (the default when no option is
// given at all).
func WithMutantTimeout(d time.Duration) ScoreOption {
	return func(c *scoreConfig) { c.mutantTimeout = d }
}

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

// Enumerator runs a command in the SAME jailed, disposable-workspace
// convention as Jail.RunTest, but returns its captured stdout instead of a
// bool pass/fail — the seam the tests×mutants matrix needs to enumerate a
// suite's individual tests (Jail.RunTest only ever answers "did it pass",
// never "what did it print"). bwrapJail implements both over the identical
// writeWorkspace helper, so an Enumerate call gets the exact same
// perms/anti-traversal/backend handling RunTest does.
type Enumerator interface {
	Enumerate(ctx context.Context, files map[string]string, cmd []string) (stdout string, err error)
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
//
// Non-terminating mutants: a mutation that breaks a loop bound (extremely
// common on iterator/loop-heavy code) can make the candidate suite hang
// forever. Score protects against that by timing the compliant baseline and
// deriving a short per-MUTANT timeout from it (mutantTimeoutMultiple x the
// baseline's own wall-clock, clamped — see clampMutantTimeout), or by using
// the caller's explicit WithMutantTimeout override. A mutant run that times
// out (Jail.RunTest returning an error matching ErrTestTimeout) is scored as
// KILLED: non-termination is a detected divergence from the baseline's own
// runtime, exactly the kind of thing mutation testing exists to catch — by
// convention it counts as a catch, not an inconclusive result. The baseline
// itself is NOT subject to this short cap (it runs under the jail's own
// generous construction timeout); if the baseline itself times out, the
// suite is broken/too-slow and Score fails closed: CompliantPass=false, no
// mutants scored, no kill rate.
func Score(ctx context.Context, j Jail, base map[string]string, codePath, compliantCode string, mutants []Mutant, testCmd []string, opts ...ScoreOption) (Report, error) {
	var cfg scoreConfig
	for _, o := range opts {
		o(&cfg)
	}

	run := func(rctx context.Context, code string) (bool, error) {
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return j.RunTest(rctx, files, testCmd)
	}

	start := time.Now()
	pass, err := run(ctx, compliantCode)
	baseDur := time.Since(start)
	if err != nil {
		if errors.Is(err, ErrTestTimeout) {
			// The healthy suite itself couldn't pass within the jail's own
			// generous budget — it is broken or too slow. Fail closed: never
			// score mutants against a baseline that can't even pass.
			return Report{CompliantPass: false}, nil
		}
		return Report{}, err
	}
	rep := Report{CompliantPass: pass}
	if !pass {
		// broken/overreaching test — do not score mutants (fail-safe: no kill rate for an invalid test)
		return rep, nil
	}

	perMutant := cfg.mutantTimeout
	if perMutant <= 0 {
		perMutant = clampMutantTimeout(baseDur)
	}

	rep.Total = len(mutants)
	for _, m := range mutants {
		mctx, cancel := context.WithTimeout(ctx, perMutant)
		passed, err := run(mctx, m.Code)
		cancel()
		if err != nil {
			if errors.Is(err, ErrTestTimeout) {
				// Non-terminating mutant: the suite hanging IS a caught
				// divergence from the healthy baseline — count it a kill,
				// not an aborted run.
				rep.Killed = append(rep.Killed, m.ID)
				continue
			}
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
