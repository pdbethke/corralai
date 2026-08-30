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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
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
	// mutantCompileCheck is the language plugin's own CompileCheck sequence,
	// run against each mutant BEFORE the suite. Empty disables the gate, which
	// preserves the pre-gate behavior for every caller that has not opted in.
	mutantCompileCheck [][]string
	// commandFor, when set, grades each mutant with the command it chooses
	// for that mutant instead of the shared testCmd. nil (the default)
	// reproduces today's behavior byte-for-byte.
	commandFor CommandFor
}

// MutantCommand is what CommandFor decides for one mutant.
type MutantCommand struct {
	Cmd   []string
	Tests int    // tests the command runs; 0 when unknown
	Rule  string // lang.SpanRule*; "" when CommandFor was not set
}

// CommandFor chooses the test command to grade a single mutant with.
type CommandFor func(m Mutant) MutantCommand

// WithCommandFor grades each mutant with the command f returns for it,
// instead of the testCmd every mutant shares. nil is today's behaviour.
// The baseline and the compile gate are unaffected — they run with testCmd.
func WithCommandFor(f CommandFor) ScoreOption {
	return func(c *scoreConfig) { c.commandFor = f }
}

// WithMutantTimeout overrides the auto-derived per-mutant timeout with an
// explicit cap. d <= 0 restores auto-derive (the default when no option is
// given at all).
func WithMutantTimeout(d time.Duration) ScoreOption {
	return func(c *scoreConfig) { c.mutantTimeout = d }
}

// WithMutantCompileCheck gates every mutant on the language's own compile
// check before it is scored. A mutant that fails the check is recorded in
// Report.Invalid, never in Killed or Survived, and never reaches the suite.
//
// The sequence is the plugin's Plugin.CompileCheck output and is chained as by
// `&&`: run in order, stop at the first non-zero exit. Passing it in rather
// than deriving it here keeps adequacy language-agnostic — no Go-specific
// string matching on compiler output, which would silently misclassify for
// python, ruby, javascript and typescript.
func WithMutantCompileCheck(cmds [][]string) ScoreOption {
	return func(c *scoreConfig) { c.mutantCompileCheck = cmds }
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
	// Span is the 1-based, inclusive range of ORIGINAL lines this mutant's
	// SEARCH anchor occupied — the lines a test must reach to observe it.
	// Zero when the producer cannot say (hand-built fixtures): the scorer
	// then grades the mutant by the file's whole selection and says so.
	Span lang.LineRange
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
	// BaselineDuration is how long the compliant (unmutated) suite took.
	//
	// It was computed on every run since this package existed — to derive the
	// per-mutant timeout — and then discarded. It is the single input to the
	// audit cost model: cost is O(mutants x the TARGET's suite runtime), and
	// that multiplier is the target's, not ours. Measured at 1.46s for
	// pallets/flask and 77s for psf/requests, a 53x spread between two
	// ordinary Python repos, which is why an estimate extrapolated from one
	// repo is worthless. Recording it turns capacity planning into a query.
	BaselineDuration time.Duration
	// Total is the number of mutants actually GRADED — killed + survived. It
	// deliberately EXCLUDES Invalid, because KillRate divides by it and a
	// mutant the compiler rejected is not part of any exam the suite sat.
	Total    int
	Killed   []string
	Survived []string
	// Invalid are mutants that failed the language's own compile check and so
	// were never run against the suite.
	//
	// WHY THIS EXISTS. Before the gate, a mutant that did not compile made the
	// test command exit non-zero, and `killed: !passed` scored that as a KILL —
	// the suite was credited with catching a bug that never existed in runnable
	// form. On low-coverage code, where more mutations fail to build and fewer
	// are genuinely caught, that inflated the headline rate exactly where an
	// honest number matters most. corral's product is a SIGNED, offline-
	// verifiable record asserting "this change's tests catch K% of injected
	// bugs"; an inflated K signs a false claim, which in a system whose value is
	// trust is not a rounding error.
	//
	// An invalid mutant is evidence about the GENERATOR, not the suite. It is
	// reported, never silently dropped — hiding it would trade one dishonesty
	// for another.
	Invalid []string
	// InvalidReasons maps an Invalid mutant's ID to what the compile checker
	// actually printed, when the Jail can report output (VerboseJail).
	//
	// The COUNT says the exam shrank; only the REASON says why. Live audits
	// rejected 56-92% of mutants and the count alone could not distinguish a
	// generator dropping a used import from one changing a signature — nor
	// could anything feed the mistake back so the model could correct it. A
	// plain Jail leaves this empty rather than failing.
	InvalidReasons map[string]string
	// PerMutant records what each GRADED mutant was actually graded with,
	// keyed by Mutant.ID. There is one entry per mutant that reached the
	// suite — a mutant the compile gate rejected is absent, because nothing
	// graded it and it has nothing to report.
	//
	// It used to be populated only under WithCommandFor. It is now filled on
	// every run because every graded mutant is TIMED, and a per-mutant
	// duration is the only thing that can say whether a slow file is one
	// pathological mutant or forty ordinary ones. TestsRun and Rule stay
	// zero for a caller that did not opt in, exactly as before, and nothing
	// downstream infers the grading MODE from this map (see the driver's
	// perMutantGraded, which is recorded from the call, not from here).
	PerMutant map[string]MutantGrading
	// MutantDurationMedian and MutantDurationMax summarize PerMutant's
	// durations — CONTENDED ones under concurrency, so median x graded can
	// exceed the pass they were measured in; see MutantGrading.Duration —
	// over the GRADED mutants — the compile-gate rejects are
	// excluded, for the same reason they are excluded from Total: they never
	// sat the exam, and a run of zeros folded into the spread would report a
	// file as faster than any mutant of it ever was.
	//
	// The median is the upper of the two middle values on an even count,
	// never their mean: it is a duration something actually took.
	MutantDurationMedian time.Duration
	MutantDurationMax    time.Duration
}

// MutantGrading is what a single mutant was actually graded with.
type MutantGrading struct {
	// TestsRun and Rule are set only when WithCommandFor chose a command for
	// this mutant; both stay zero on a run graded by one shared command.
	TestsRun int
	Rule     string
	// Duration is the wall clock of the ONE suite run that graded this
	// mutant — not the compile gate that preceded it (which is not the
	// exam), and not a timed-out mutant's second, baseline-reprobing run
	// (which measures the machine, not the mutant).
	//
	// CONTENDED under WithConcurrency(N>1): the run was one of N sharing the
	// box, so this is how long it took under that load, not how long it
	// would take alone. The durations of N mutants therefore OVERLAP, and
	// summing them (or multiplying the median by the graded count) exceeds
	// the wall clock of the pass they happened in. They are a cost
	// distribution — which mutants are expensive — never a budget.
	Duration time.Duration
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

	runCmd := func(rctx context.Context, code string, cmd []string) (bool, error) {
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return j.RunTest(rctx, files, cmd)
	}

	run := func(rctx context.Context, code string) (bool, error) {
		return runCmd(rctx, code, testCmd)
	}

	// runCmdVerbose keeps the checker's OUTPUT when the Jail can report it.
	// Mutant test runs deliberately stay on the plain path (their output is not
	// evidence), but a COMPILE rejection's output is the only thing that says
	// why — and is what a future repair round would feed back to the generator.
	runCmdVerbose := func(rctx context.Context, code string, cmd []string) (bool, string, error) {
		vj, ok := j.(VerboseJail)
		if !ok {
			pass, err := runCmd(rctx, code, cmd)
			return pass, "", err
		}
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return vj.RunTestVerbose(rctx, files, cmd)
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
			return Report{CompliantPass: false, BaselineOutput: baseOut, BaselineDuration: baseDur}, nil
		}
		return Report{}, err
	}
	rep := Report{CompliantPass: pass}
	rep.BaselineDuration = baseDur
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

	// rep.Total is NOT set from len(mutants) here: with the compile gate on,
	// the graded count is only known once invalid mutants have been separated
	// out (see the assignment after the assembly loop). Setting it up front is
	// how invalid mutants would sneak back into KillRate's denominator.

	// Results are collected into an INDEXED slice and assembled in mutants'
	// own order below — never appended as they finish. Score's contract is
	// that Killed/Survived follow the input order so the report is
	// reproducible, and this ledger is signed: an ordering that depended on
	// scheduling would make two runs of identical inputs disagree.
	type outcome struct {
		killed        bool
		err           error
		invalid       bool
		invalidReason string
		grading       *MutantGrading
	}
	outcomes := make([]outcome, len(mutants))

	scoreOne := func(i int, m Mutant) {
		// The compile gate runs FIRST and is cheap relative to a suite run, so
		// an invalid mutant costs one type-check instead of a full execution.
		if len(cfg.mutantCompileCheck) > 0 {
			gctx, gcancel := context.WithTimeout(ctx, perMutant)
			for _, cmd := range cfg.mutantCompileCheck {
				ok, out, err := runCmdVerbose(gctx, m.Code, cmd)
				if err != nil {
					// FAIL CLOSED. A gate that could not RUN says nothing about
					// the mutant; treating that as "invalid" would quietly erase
					// the exam whenever the jail misbehaved.
					gcancel()
					outcomes[i] = outcome{err: err}
					return
				}
				if !ok {
					gcancel()
					outcomes[i] = outcome{invalid: true, invalidReason: strings.TrimSpace(out)}
					return
				}
			}
			gcancel()
		}
		cmd := testCmd
		var grading *MutantGrading
		if cfg.commandFor != nil {
			mc := cfg.commandFor(m)
			if len(mc.Cmd) > 0 {
				cmd = mc.Cmd
			}
			grading = &MutantGrading{TestsRun: mc.Tests, Rule: mc.Rule}
		}
		if grading == nil {
			// Every graded mutant gets an entry, because every graded mutant
			// is timed. A caller that never set WithCommandFor still gets
			// TestsRun 0 / Rule "" here, which is what it always got.
			grading = &MutantGrading{}
		}
		mctx, cancel := context.WithTimeout(ctx, perMutant)
		// THE measurement: the single suite run that decides this mutant's
		// verdict. Recorded even when that run errors or times out, so a
		// mutant that ate its whole budget says so.
		mstart := time.Now()
		passed, err := runCmd(mctx, m.Code, cmd)
		grading.Duration = time.Since(mstart)
		cancel()
		if err != nil {
			if errors.Is(err, ErrTestTimeout) {
				// A timeout has TWO causes and they are opposite verdicts:
				//
				//   1. The mutant is non-terminating. The suite hanging IS a
				//      caught divergence from the healthy baseline — a kill.
				//   2. The MACHINE was slow. A loaded shared runner (which is
				//      exactly where the GitHub Action executes) can push an
				//      ordinary run past a timeout derived from a baseline
				//      measured when the box was idle.
				//
				// Counting both as kills silently INFLATES the kill rate on a
				// busy machine — the same defect class as scoring a mutant the
				// compiler rejected as caught: crediting the tests with a
				// divergence they never detected.
				//
				// Tell them apart by re-probing the COMPLIANT baseline under
				// the same budget, not by re-running the mutant. Re-running the
				// mutant makes a genuine infinite loop wait twice (it must
				// exhaust a second, larger budget before the kill is recorded),
				// which is why the fast-kill guarantee this scorer advertises
				// belongs to the baseline probe instead:
				//
				//   - baseline still finishes  -> the box is fine, so the
				//     mutant really does not terminate. Kill, immediately.
				//   - baseline ALSO times out  -> the box is slow, and this
				//     mutant's run proves nothing. Not a kill, and not a
				//     survivor either: it is UNMEASURED, and an error is the
				//     honest report.
				bctx, bcancel := context.WithTimeout(ctx, perMutant)
				_, berr := run(bctx, compliantCode)
				bcancel()
				if berr == nil {
					outcomes[i] = outcome{killed: true, grading: grading}
					return
				}
				if errors.Is(berr, ErrTestTimeout) {
					outcomes[i] = outcome{err: fmt.Errorf("mutant %s: run timed out AND the compliant baseline timed out under the same budget (%s) — the machine is too loaded to grade this mutant; nothing is inferred from it", m.ID, perMutant)}
					return
				}
				outcomes[i] = outcome{err: berr}
				return
			}
			outcomes[i] = outcome{err: err}
			return
		}
		// test PASSED on a violation => it did NOT catch it
		outcomes[i] = outcome{killed: !passed, grading: grading}
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
		if outcomes[i].invalid {
			rep.Invalid = append(rep.Invalid, m.ID)
			if r := outcomes[i].invalidReason; r != "" {
				if rep.InvalidReasons == nil {
					rep.InvalidReasons = map[string]string{}
				}
				rep.InvalidReasons[m.ID] = r
			}
			continue
		}
		if g := outcomes[i].grading; g != nil {
			if rep.PerMutant == nil {
				rep.PerMutant = map[string]MutantGrading{}
			}
			rep.PerMutant[m.ID] = *g
		}
		if outcomes[i].killed {
			rep.Killed = append(rep.Killed, m.ID)
			continue
		}
		rep.Survived = append(rep.Survived, m.ID)
	}
	// Total is the GRADED count. Set here, after the gate has run, rather than
	// from len(mutants) up front: dividing by the emitted count would put the
	// invalid mutants back into the denominator by the back door.
	rep.Total = len(rep.Killed) + len(rep.Survived)
	rep.MutantDurationMedian, rep.MutantDurationMax = mutantSpread(rep.PerMutant)
	return rep, nil
}

// mutantSpread is the middle and the worst of what grading one mutant cost,
// over the mutants that were actually graded. Both are zero when nothing was
// timed — a run whose baseline failed, or one on a Jail so fast every run
// rounds to nothing — and every reader stores that as NULL rather than as a
// file whose mutants were free.
func mutantSpread(per map[string]MutantGrading) (median, max time.Duration) {
	if len(per) == 0 {
		return 0, 0
	}
	ds := make([]time.Duration, 0, len(per))
	for _, g := range per {
		if g.Duration > 0 {
			ds = append(ds, g.Duration)
		}
	}
	if len(ds) == 0 {
		return 0, 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	// The UPPER of the two middle values on an even count, matching the
	// tests-per-mutant spread beside it: this is a duration some mutant
	// really took, not an average of two that nothing did.
	return ds[len(ds)/2], ds[len(ds)-1]
}
