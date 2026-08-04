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
	"sync"
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
	concurrency   int
}

// WithMutantTimeout overrides the auto-derived per-mutant timeout with an
// explicit cap. d <= 0 restores auto-derive (the default when no option is
// given at all).
func WithMutantTimeout(d time.Duration) ScoreOption {
	return func(c *scoreConfig) { c.mutantTimeout = d }
}

// WithConcurrency scores up to n mutants at once. The default (and anything
// < 2) is strictly sequential, which is what every existing caller gets.
//
// OPT-IN ON PURPOSE, and the caller — not this package — owns the safety
// argument, because it depends entirely on the Jail implementation:
//
//   - bwrapJail is a stateless value whose writeWorkspace does its own
//     os.MkdirTemp per call, so concurrent runs are fully isolated. Safe.
//   - WorkspaceRunner mutates ONE checkout in place and has NO mutex.
//     Concurrent applyFiles would interleave and corrupt the tree. NEVER
//     raise this for that substrate.
//
// This is the single biggest lever on audit wall-clock. Scoring runs the
// target's whole suite once per mutant, so cost is O(mutants x suite runtime)
// — measured at 1.46s/suite for pallets/flask but 77s for psf/requests, where
// the suite is ~96% of a file's audit. Sequentially that is the wall a hosted
// tier would hit; the work is embarrassingly parallel and was simply never
// distributed.
func WithConcurrency(n int) ScoreOption {
	return func(c *scoreConfig) { c.concurrency = n }
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

// CanaryCode is deliberately invalid source in every language corral
// supports (go, python, ruby, javascript, typescript). Any check command
// that genuinely compiles or imports the file under audit MUST fail on it.
// A suite that still passes provably never reads the file, so nothing it
// reports about that file can mean anything.
const CanaryCode = "!!!corral canary!!!"

// Report is the outcome of scoring a candidate test against compliant code
// and a set of mutants.
type Report struct {
	CompliantPass bool
	// CanaryKilled reports whether the suite failed on deliberately invalid
	// source. False means the suite never compiles or imports the file, so
	// KillRate is meaningless — the same fail-closed shape as
	// CompliantPass. Callers MUST check this before reading KillRate.
	CanaryKilled bool
	// AuthoredTestUnreached narrows CanaryKilled==false for ONE specific,
	// extremely common cause: the test command never collected the authored
	// test's own file, so the run proved nothing about it.
	//
	// The two causes are wildly different to act on. "Your authored test fails
	// on correct code" is a problem with the test. "Your test command doesn't
	// run that file" is a problem with the COMMAND — usually because it was
	// pinned to one path (`vitest run one.test.ts`, a `-run` regex, a narrow
	// glob) while the authored test is a NEW file beside the developer's. That
	// run grades perfectly and reports proven_missed 0 forever, which reads as
	// "your suite has no provable gaps" when it means "nobody looked".
	//
	// Set only by the positive control in advpool's authored-scoring path,
	// which already computes exactly this and used to discard the distinction.
	// CanaryKilled stays false alongside it — this narrows the diagnosis, it
	// never softens it.
	AuthoredTestUnreached bool
	// BaselineOutput is the runner's OWN output from the compliant (unmutated)
	// run, populated only when that run FAILED and only when the Jail can
	// report it (see VerboseJail).
	//
	// A failing baseline is the most common way a real audit dies, and for a
	// long time it reported nothing but "baseline does not pass unmutated" —
	// discarding the compiler/runner text that says exactly why. Two paid
	// audits on two different repos dead-ended on that missing string: one was
	// a Python venv the offline jail could not see, one was never diagnosed at
	// all. corral already feeds the compiler's own error back to the
	// test-writer for the same reason; the baseline deserves the same respect.
	//
	// Empty on success (there is nothing to explain) and empty for a Jail that
	// only implements RunTest.
	BaselineOutput string
	Total          int
	Killed         []string
	Survived       []string
}

// VerboseJail is a Jail that can also report what a run PRINTED, not just
// whether it passed. bwrapJail implements it via RunTestVerbose over the same
// workspace helper, so honouring it costs nothing extra — the baseline is one
// run either way and the output is simply kept instead of dropped.
//
// Optional on purpose: Jail stays a one-method interface, so every existing
// implementation (and every test fake) keeps compiling and simply reports no
// output.
type VerboseJail interface {
	RunTestVerbose(ctx context.Context, files map[string]string, testCmd []string) (bool, string, error)
}

// KillRate is the adequacy score: the fraction of mutants the test caught.
// Zero when no mutants were run (including when the report is invalid
// because the test failed on compliant code, or because the canary survived
// and the suite provably never reads the file).
func (r Report) KillRate() float64 {
	if r.Total == 0 || !r.CanaryKilled {
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

	// The BASELINE run alone goes through the verbose path when the jail offers
	// one: it is a single run either way, so keeping the output costs nothing,
	// and it is the one run whose failure a human must be able to diagnose.
	// Mutant runs deliberately stay on the plain path — there are ~42 of them
	// and their output is not evidence of anything.
	baselineRun := func(rctx context.Context, code string) (bool, string, error) {
		vj, ok := j.(VerboseJail)
		if !ok {
			p, err := run(rctx, code)
			return p, "", err
		}
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return vj.RunTestVerbose(rctx, files, testCmd)
	}

	start := time.Now()
	pass, baseOut, err := baselineRun(ctx, compliantCode)
	baseDur := time.Since(start)
	if err != nil {
		if errors.Is(err, ErrTestTimeout) {
			// The healthy suite itself couldn't pass within the jail's own
			// generous budget — it is broken or too slow. Fail closed: never
			// score mutants against a baseline that can't even pass. The
			// output rides along: "which test hung" is exactly the question a
			// timed-out baseline leaves open.
			return Report{CompliantPass: false, BaselineOutput: baseOut}, nil
		}
		return Report{}, err
	}
	rep := Report{CompliantPass: pass}
	if !pass {
		// broken/overreaching test — do not score mutants (fail-safe: no kill
		// rate for an invalid test). Carry the runner's own words: without them
		// this is the single least debuggable outcome an audit can produce.
		rep.BaselineOutput = baseOut
		return rep, nil
	}

	// The canary runs only when there are mutants to score. The
	// baseline-stability path calls Score with none, twice per file, and a
	// canary there would triple its cost for no information.
	if len(mutants) > 0 {
		cpass, cerr := run(ctx, CanaryCode)
		if cerr != nil {
			if errors.Is(cerr, ErrTestTimeout) {
				// A suite that hangs on invalid source did react to it.
				rep.CanaryKilled = true
			} else {
				return Report{}, cerr
			}
		} else {
			rep.CanaryKilled = !cpass
		}
		if !rep.CanaryKilled {
			// The suite passed on source that cannot compile: it never reads
			// this file. Fail closed — no mutants scored, no kill rate.
			return rep, nil
		}
	}

	perMutant := cfg.mutantTimeout
	if perMutant <= 0 {
		perMutant = clampMutantTimeout(baseDur)
	}

	rep.Total = len(mutants)

	// Results are collected into an INDEXED slice and assembled in mutants'
	// own order below — never appended as they finish. Score's contract is
	// that Killed/Survived follow the input order so the report is
	// reproducible, and this ledger is signed: an ordering that depended on
	// scheduling would make two runs of identical inputs disagree.
	type outcome struct {
		killed bool
		err    error
	}
	outcomes := make([]outcome, len(mutants))

	scoreOne := func(i int, m Mutant) {
		mctx, cancel := context.WithTimeout(ctx, perMutant)
		passed, err := run(mctx, m.Code)
		cancel()
		if err != nil {
			if errors.Is(err, ErrTestTimeout) {
				// Non-terminating mutant: the suite hanging IS a caught
				// divergence from the healthy baseline — count it a kill,
				// not an aborted run.
				outcomes[i] = outcome{killed: true}
				return
			}
			outcomes[i] = outcome{err: err}
			return
		}
		// test PASSED on a violation => it did NOT catch it
		outcomes[i] = outcome{killed: !passed}
	}

	if cfg.concurrency > 1 && len(mutants) > 1 {
		sem := make(chan struct{}, cfg.concurrency)
		var wg sync.WaitGroup
		for i, m := range mutants {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, m Mutant) {
				defer wg.Done()
				defer func() { <-sem }()
				scoreOne(i, m)
			}(i, m)
		}
		wg.Wait()
	} else {
		for i, m := range mutants {
			scoreOne(i, m)
		}
	}

	for i, m := range mutants {
		if outcomes[i].err != nil {
			// Fail the whole report, exactly as the sequential path did: a
			// mutant that could not be RUN is not a mutant that survived, and
			// must never be scored as one.
			return Report{}, outcomes[i].err
		}
		if outcomes[i].killed {
			rep.Killed = append(rep.Killed, m.ID)
			continue
		}
		rep.Survived = append(rep.Survived, m.ID)
	}
	return rep, nil
}
