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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
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
// doesn't turn "3x baseline" into a per-mutant wait that is itself most of
// the whole-run budget.
const (
	minMutantTimeout = 30 * time.Second
	maxMutantTimeout = 5 * time.Minute
	// mutantTimeoutMultiple is how many multiples of the healthy baseline's
	// own wall-clock a mutant run gets before it is treated as
	// non-terminating. It was 8. PIT — fifteen years of the same problem —
	// uses 1.25× plus a constant; 3 leaves room for the contention of six
	// trees grading at once (the baseline is measured alone) and is still a
	// third of what a hang used to cost: on psf/requests' models.py three
	// hung mutants ran to the five-minute ceiling and were fifteen of the
	// file's twenty-four dev-pass minutes. The kill is still only credited
	// when the compliant baseline passes under the same cap (see the
	// re-probe below), so a shorter cap cannot invent a kill on a slow box.
	mutantTimeoutMultiple = 3
)

// clampMutantTimeout derives a per-mutant timeout from how long the
// compliant run of THE SAME COMMAND took: mutantTimeoutMultiple x that,
// clamped to [minMutantTimeout, maxMutantTimeout]. Auto-adapts to any
// repo's suite with no operator tuning. Per command, not per file: under
// per-mutant selection one mutant's command runs two tests and another's
// three hundred, and a cap derived from the file's shared baseline gave the
// two-test mutant five minutes to hang in.
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
	// failureParser, when set AND the Jail implements DetailedJail, reads the
	// id of the first failing test out of each KILLED mutant's run output.
	// nil (the default) leaves every KilledBy empty and keeps the mutant runs
	// on the plain, output-discarding path they have always used.
	failureParser lang.FailureParser
	// failFast, when set, yields the runner's stop-at-first-failure arguments
	// for a MUTANT run's command. nil (the default) is today's behaviour.
	failFast FailFastFor
}

// FailFastFor returns the stop-at-first-failure arguments to append to one
// MUTANT run's command, or ok=false for a runner that has none. It is the
// language plugin's lang.FailFaster, passed in rather than looked up here so
// adequacy stays language-agnostic.
type FailFastFor func(cmd []string) (args []string, ok bool)

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

// WithFailureParser names, for each KILLED mutant, the first test the runner
// reported as failing — best effort, and only ever the id the output itself
// printed (see lang.FailureParser).
//
// It changes NOTHING that is measured. The kill/survive verdict is the same
// run and the same exit code; the only difference is that the run's output is
// kept and read instead of discarded. Two conditions must BOTH hold or the
// column simply stays empty: a parser is supplied here, and the Jail
// implements DetailedJail. A survivor never gets an id, because nothing
// caught it, and an output whose summary cannot be parsed gets "" rather than
// a guess.
func WithFailureParser(p lang.FailureParser) ScoreOption {
	return func(c *scoreConfig) { c.failureParser = p }
}

// WithMutantFailFast lets each MUTANT's run stop at its first failing test.
//
// A killed mutant needs exactly ONE failing test; scoring has always paid for
// the whole selected set to finish anyway, on every one of the ~42 runs a file
// costs. This is the largest per-mutant saving available that cannot move a
// verdict: "did any selected test fail" is the same question whether the
// runner stopped at the first failure or ran to the end, and killed_by already
// records the FIRST failure the output named.
//
// THREE THINGS ARE DELIBERATELY OUT OF ITS REACH:
//
//   - the compliant BASELINE, which must execute everything — a suite corral
//     certifies is a suite corral ran;
//   - the CANARY, which is a baseline run on invalid source;
//   - the compile gate, which is not a test run at all.
//
// AND IT IS PROVEN BEFORE IT IS USED. An argument the runner does not
// recognise makes it exit non-zero, which this scorer reads as a kill — so an
// unrecognised flag would silently take every kill rate to 1.00. Score
// therefore re-runs the compliant baseline ONCE with the args appended and
// only enables fail-fast if that run still passes; otherwise it is dropped and
// Report.FailFastNote says so. One extra suite run per file, against a saving
// proportional to the mutant count.
func WithMutantFailFast(f FailFastFor) ScoreOption {
	return func(c *scoreConfig) { c.failFast = f }
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
// A MUTANT IS ITS HUNK, not a copy of the file.
//
// It used to carry Code: the whole mutated file, materialised once at parse
// time and then dragged through every prompt, every recorded set and every
// event that touched it. Cost telemetry put the number on it — the writer
// seat spending ~0.5M tokens on one file — because a ten-line edit was being
// shipped as six hundred lines, forty-two times over. The generator already
// emits a SEARCH/REPLACE hunk; the whole-file copy was something corral built
// itself and then never threw away.
//
// So the hunk IS the representation, and the file exists only where a file is
// genuinely required: inside the jail, for the length of one run, via Apply.
type Mutant struct {
	ID string
	// ParentSHA256 is the hex SHA-256 of the EXACT original code this mutant was
	// derived from (empty for hand-built test fixtures). It ties each mutant to
	// the precise bytes under audit: a mutant is a faithful single-point edit of
	// that original, so `sha256(original) == ParentSHA256` and Apply against
	// those bytes reproduces the mutated file. Set by testgen's patch applier,
	// which drops any mutant that cannot be proven a clean single-region
	// derivative.
	ParentSHA256 string
	// Span is the 1-based, inclusive range of ORIGINAL lines this mutant's
	// SEARCH anchor occupied — the lines a test must reach to observe it.
	// Zero when the producer cannot say (hand-built fixtures): the scorer
	// then grades the mutant by the file's whole selection and says so.
	Span lang.LineRange
	// Search is the VERBATIM anchor the generator emitted — indentation and
	// line endings included, because that is what makes the edit provably
	// single-point against known bytes. Empty means the v1 whole-file shape:
	// a mutant read back from a corral-mutants-1 document, which recorded the
	// finished file and not the hunk that produced it.
	Search string
	// Replace is what Search becomes. When Search is empty, Replace IS the
	// whole mutated file.
	Replace string
}

// invalidReasonAnchor prefixes the InvalidReasons entry for a mutant whose
// hunk would not apply to the source being graded. It sits on the same
// accounting path as a compile-gate rejection because it is the same kind of
// fact: the mutant never sat the exam, so it belongs in neither numerator nor
// denominator — and it is DISCLOSED rather than dropped, since a set that
// silently loses mutants looks identical to a set that never had them.
const invalidReasonAnchor = "anchor"

// IsWholeFile reports the v1 shape: no anchor, so Replace is the entire
// mutated file rather than a hunk of it.
func (m Mutant) IsWholeFile() bool { return m.Search == "" }

// Apply materialises the mutated file.
//
// It is the ONLY place a mutant becomes a file, and it is byte-for-byte the
// algorithm testgen.applyMutation used to run at parse time.
//
// THE GUARANTEE IS THE PARENT HASH, not a round-trip. This used to end by
// undoing its own splice and demanding the original back, described as the
// thing a signed verdict rests on. It was not: mutated[:i] + Search +
// mutated[i+len(Replace):] is arithmetic over the string Apply built one line
// earlier, so it reconstructs `original` for ANY source the anchor occurs in
// and can only fail if Go's own slicing is wrong. It proved that the splice
// was a splice, which was never in doubt, and said nothing about WHICH bytes
// were spliced.
//
// So when the mutant records one (ParentSHA256, set by the generator's patch
// applier), the real check is made instead: sha256(original) must be the
// bytes this mutant is a single-point edit OF. A replay against a file that
// has changed since — the case --mutants exists to make safe — is refused on
// the anchor-invalid path rather than grading an exam nobody wrote. An EMPTY
// ParentSHA256 is not a claim (hand-built fixtures, pre-hash producers) and
// is checked against nothing.
//
// A whole-file mutant (Search == "") returns Replace verbatim, whatever
// original says — that is the v1 compatibility path, where the recorded
// document holds the finished file and there is nothing to splice.
//
// THE EMPTY SEARCH IS OVERLOADED, so be exact about who may construct one: an
// empty Search is the v1 whole-file shape and is only ever CONSTRUCTED by the
// v1 reader — the parser refuses an empty SEARCH from a model. Were it not,
// a generator that botched a hunk would produce a "whole-file mutant" whose
// entire content is the three lines it meant to substitute, and Apply would
// hand that to the jail as the file under audit.
//
// Otherwise Search must be non-empty, must DIFFER from Replace (a mutation
// that changes nothing is not a mutant), and must occur EXACTLY ONCE in
// original. Every violation is an error and never a silent no-op: an
// unanchored mutant scored as a survivor would be a coverage gap that does
// not exist, and scored as a kill would be credit for catching nothing.
func (m Mutant) Apply(original string) (string, error) {
	// BEFORE the whole-file shortcut, deliberately: the v1 path returns
	// Replace without ever reading original, so it was the one shape with no
	// tie to the bytes at all. A v1 document that recorded a parent hash
	// still names the only source its finished file is a derivative of.
	if m.ParentSHA256 != "" {
		sum := sha256.Sum256([]byte(original))
		if got := hex.EncodeToString(sum[:]); got != m.ParentSHA256 {
			return "", fmt.Errorf("adequacy: mutant %s is an edit of source %s, not the %s being graded — a mutant is a single-point edit of SPECIFIC bytes and re-applying it to different ones grades an exam nobody wrote", m.ID, short(m.ParentSHA256), short(got))
		}
	}
	if m.IsWholeFile() {
		return m.Replace, nil
	}
	if m.Search == m.Replace {
		return "", fmt.Errorf("adequacy: mutant %s has SEARCH identical to REPLACE — it changes nothing", m.ID)
	}
	i := strings.Index(original, m.Search)
	if i < 0 {
		return "", fmt.Errorf("adequacy: mutant %s does not anchor: its SEARCH is not in the source's bytes", m.ID)
	}
	if strings.Contains(original[i+len(m.Search):], m.Search) {
		return "", fmt.Errorf("adequacy: mutant %s does not anchor uniquely: its SEARCH occurs more than once", m.ID)
	}
	return original[:i] + m.Replace + original[i+len(m.Search):], nil
}

// short renders a hex digest for an error message. Full 64-hex digests in a
// one-line rejection push the sentence off the operator's terminal, and the
// first twelve are already unambiguous for telling "these two files differ"
// apart at a glance.
func short(hexDigest string) string {
	if len(hexDigest) <= 12 {
		return hexDigest
	}
	return hexDigest[:12]
}

// HunkSpan is the 1-based, inclusive range of ORIGINAL lines that search
// occupied — the lines a test must reach to observe the mutant.
//
// THE SPAN ALGORITHM LIVES HERE, next to Apply and nowhere else. Both are
// derived from the same two inputs (original, Search), and a second copy in
// the parser would be free to drift: the span is what a selection rule aims a
// test at, and Apply is what the jail actually grades, so a disagreement
// between them would aim tests at the wrong lines while every number still
// looked plausible.
//
// A zero LineRange means the anchor is absent (or empty) — the caller has
// nothing to say about where the edit lands.
func HunkSpan(original, search string) lang.LineRange {
	if search == "" {
		return lang.LineRange{}
	}
	i := strings.Index(original, search)
	if i < 0 {
		return lang.LineRange{}
	}
	start := strings.Count(original[:i], "\n") + 1
	end := start + strings.Count(strings.TrimSuffix(search, "\n"), "\n")
	return lang.LineRange{Start: start, End: end}
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
	// FailFast reports whether MUTANT runs actually stopped at their first
	// failing test (see WithMutantFailFast). False both when no fail-fast was
	// configured and when the probe below rejected it.
	FailFast bool
	// DuplicateMutants is how many of the graded mutants were byte-identical
	// edits of an earlier one and so were RUN once and answered twice (see
	// the collapse in Score). It changes no number in this report — every
	// duplicate is still in Killed/Survived, still has its own PerMutant
	// entry, and still counts in Total — it only says how many suite runs the
	// set did not have to pay for. Disclosed because a reader comparing wall
	// clock to mutant count is otherwise looking at an unexplained gap.
	DuplicateMutants int
	// FailFastNote explains a fail-fast that was asked for and NOT used —
	// empty when none was asked for, and empty when it was used. It is a
	// disclosure, not an error: the run is correct either way, just slower.
	FailFastNote string
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
	// Unmeasured lists mutants whose GRADING COMMAND fails on the compliant
	// code, so nothing that command says about the mutant is evidence. They
	// are neither killed nor survived and are excluded from Total.
	//
	// This category exists because its absence let a proof be fabricated.
	// The compliant baseline was proven for ONE command — the shared one —
	// while every mutant could be graded by a DIFFERENT command that was
	// never run against compliant code: a per-span narrowing in the dev pass,
	// or the authored test ALONE in the proving pass. A command that fails
	// for reasons that have nothing to do with the mutant then reads as a
	// kill. Reproduced with real pytest: an authored file whose class is
	// named CalcTest (pytest collects nothing, exit 5) marked a genuinely
	// unobservable mutant KILLED, and the driver signed it as a proven gap.
	Unmeasured        []string
	UnmeasuredReasons map[string]string
	// CompileGateNote is set when the compile gate rejected the COMPLIANT
	// file and was therefore disabled for this run — see Score.
	CompileGateNote string
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
	// KilledBy is the id of the FIRST test the runner reported as failing on
	// this mutant — the test that was awake. Set only for KILLED mutants, and
	// only when WithFailureParser supplied a parser AND the Jail could hand
	// back the run's output (DetailedJail); "" everywhere else, including for
	// a mutant killed by a TIMEOUT, where no test reported anything at all.
	//
	// Best effort and never inferred: it is lifted verbatim from the output's
	// own summary, so an unparsable run stores NULL rather than naming a test
	// that may not have run.
	KilledBy string
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

// maxDetailedOutput bounds what a DetailedJail hands back: the LAST 64 KiB of
// the run. A failing suite's summary is at the END of its output — pytest's
// "short test summary info" block and `go test`'s FAIL lines both trail
// everything else — so the tail is the half that can answer "which test", and
// a stack-trace-heavy run must not be able to hold megabytes per mutant.
const maxDetailedOutput = 64 << 10

// DetailedJail is a Jail that can hand back what a run PRINTED as bytes, for
// the ONE reader that needs them: the language plugin's failure parser, which
// turns the runner's own summary into the id of the first test that failed
// (lang.FailureParser). VerboseJail already returns output, but only the
// baseline and the compile gate go through it; this is the mutant path, and
// it is separated so that the mutant path's cost is opted into rather than
// inherited.
//
// Optional on purpose, exactly like VerboseJail: a Jail that only implements
// RunTest keeps working and simply reports no killer. Nothing MEASURED
// changes either way — the pass/fail that decides the verdict is the same
// run, the same exit code, the same semantics.
type DetailedJail interface {
	RunTestDetailed(ctx context.Context, files map[string]string, testCmd []string) (ok bool, output []byte, err error)
}

// tailBytes keeps the last n bytes of b, copying rather than aliasing so the
// runner's buffer can be reused.
func tailBytes(b []byte, n int) []byte {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
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

	// THE MUTANT PATH, when — and only when — both halves of the killed_by
	// feature are present: a parser to read the output and a jail that can
	// hand it over. Everything else about the run is identical to runCmd's,
	// including which exit code means killed.
	dj, detailed := j.(DetailedJail)
	nameKiller := cfg.failureParser != nil && detailed
	runCmdDetailed := func(rctx context.Context, code string, cmd []string) (bool, []byte, error) {
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return dj.RunTestDetailed(rctx, files, cmd)
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
	// capFor is the per-COMMAND cap: a narrowed command's own compliant
	// duration (measured by the proving loop below) × the multiple, or the
	// file-level cap when nothing narrower was measured. An explicit
	// --test-timeout overrides both.
	commandBaseline := map[string]time.Duration{}
	capFor := func(cmd []string) time.Duration {
		if cfg.mutantTimeout > 0 {
			return cfg.mutantTimeout
		}
		if d, ok := commandBaseline[strings.Join(cmd, "\x00")]; ok {
			return clampMutantTimeout(d)
		}
		return perMutant
	}

	// FAIL-FAST, PROVEN BEFORE IT IS USED. The args come from the language
	// plugin's own runner knowledge, but "the runner accepts this flag" is a
	// fact about the TARGET's installed toolchain, not about the plugin — and
	// a rejected flag exits non-zero, which every mutant run below would score
	// as a kill. So the compliant baseline is re-run once with the args
	// appended: if it still passes, the runner took the flag and a mutant's
	// non-zero exit still means a failing test. If it does not, fail-fast is
	// dropped for this file and the reason is disclosed. Never attempted with
	// no mutants to score (there is nothing to save) and never applied to the
	// baseline or the canary above, which must run everything.
	failFast := func(cmd []string) []string { return cmd }
	if cfg.failFast != nil && len(mutants) > 0 {
		if args, ok := cfg.failFast(testCmd); ok {
			probeCmd := lang.AppendFailFast(testCmd, args)
			if len(probeCmd) == len(testCmd) {
				rep.FailFastNote = "the test command already stops at the first failure"
			} else {
				pctx, pcancel := context.WithTimeout(ctx, perMutant)
				ffPass, ffErr := runCmd(pctx, compliantCode, probeCmd)
				pcancel()
				switch {
				case ffErr != nil:
					rep.FailFastNote = fmt.Sprintf("fail-fast (%s) not used: the probe run failed: %v", strings.Join(args, " "), ffErr)
				case !ffPass:
					rep.FailFastNote = fmt.Sprintf("fail-fast (%s) not used: the healthy suite does not pass with it, so the runner does not accept it", strings.Join(args, " "))
				default:
					rep.FailFast = true
					failFast = func(cmd []string) []string { return lang.AppendFailFast(cmd, args) }
				}
			}
		} else {
			rep.FailFastNote = "this runner has no stop-at-first-failure flag corral is sure of"
		}
		// SAY IT. FailFastNote had no reader anywhere — the probe spent one
		// extra suite run per file and its answer reached nobody, which is
		// this repository's own named recurring defect: a real measurement
		// computed and then dropped on the floor.
		//
		// A log line rather than a Verdict field: the note explains why a run
		// is SLOWER than the operator expected, which is a thing to say while
		// they are waiting, not a thing to sign. The verdict is identical
		// either way — fail-fast changes the clock, not the answer, and
		// internal/adequacy's own verdict-identity acceptance proves it.
		if rep.FailFastNote != "" {
			log.Printf("adequacy: %s — every mutant runs the whole suite, so this file costs more wall clock than one with fail-fast", rep.FailFastNote)
		}
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
		killed           bool
		err              error
		invalid          bool
		invalidReason    string
		unmeasured       bool
		unmeasuredReason string
		grading          *MutantGrading
	}
	outcomes := make([]outcome, len(mutants))

	// EVERY GRADING COMMAND IS PROVEN ON COMPLIANT CODE BEFORE IT MAY DECIDE
	// ANYTHING. The shared testCmd was proven by the baseline above; a
	// per-mutant command (WithCommandFor) was not, and a command that fails
	// on the unmutated code cannot distinguish a mutant from itself — its
	// non-zero exit is a fact about the command, not about the mutant.
	//
	// Three reproduced shapes, all real pytest, all previously counted as
	// kills: an authored test file pytest does not collect (exit 5, "no tests
	// ran"); an order-dependent dev test that only passes after a sibling the
	// narrowing dropped; and `addopts = --cov-fail-under=90` in pytest.ini,
	// which fails any subset covering less than the whole. In the proving
	// pass each of those signed a "proven missed" the authored test never
	// earned.
	//
	// One probe per DISTINCT command, not per mutant: the authored pass uses
	// one command for every survivor, and the dev pass reuses narrowings
	// across mutants sharing a span, so this is a handful of extra runs
	// against a suite already being run once per mutant.
	// THE COMPILE GATE IS PROBED ON THE COMPLIANT FILE FIRST. A gate that
	// rejects the UNMUTATED source is a broken gate for this file, not a
	// verdict on the mutants: a pre-existing `fmt.Sprintf("x")` statement
	// anywhere in a Go file — which `go test` runs happily — made `go vet`
	// reject every mutant with a reason pointing at a line no mutant
	// touched, the exam reported Total 0, and the report blamed the
	// GENERATOR ("evidence about the generator"). That is a plausible cause
	// of the 56-92% rejection rates seen in live audits. A gate the pristine
	// file cannot pass is switched off for the run and the report says so;
	// every mutant is then graded by execution, which is the measurement
	// that matters anyway.
	if len(cfg.mutantCompileCheck) > 0 {
		gctx, gcancel := context.WithTimeout(ctx, perMutant)
		for _, cmd := range cfg.mutantCompileCheck {
			ok, out, gerr := runCmdVerbose(gctx, compliantCode, cmd)
			if gerr != nil {
				gcancel()
				return Report{}, fmt.Errorf("adequacy: probing the compile gate on compliant code: %w", gerr)
			}
			if !ok {
				rep.CompileGateNote = fmt.Sprintf("compile gate DISABLED for this file: %s rejects the UNMUTATED source, so it cannot judge a mutant — every mutant graded by execution instead. Gate output: %s",
					strings.Join(cmd, " "), strings.TrimSpace(lastLines(out, 6)))
				cfg.mutantCompileCheck = nil
				break
			}
		}
		gcancel()
	}

	commandFailsOnCompliant := map[string]string{}
	if cfg.commandFor != nil {
		sharedKey := strings.Join(testCmd, "\x00")
		distinct := map[string][]string{}
		for _, m := range mutants {
			mc := cfg.commandFor(m)
			if len(mc.Cmd) == 0 {
				continue
			}
			key := strings.Join(mc.Cmd, "\x00")
			if key == sharedKey {
				continue // proven by the baseline
			}
			distinct[key] = mc.Cmd
		}
		for key, cmd := range distinct {
			pctx, pcancel := context.WithTimeout(ctx, perMutant)
			pstart := time.Now()
			passed, out, perr := runCmdVerbose(pctx, compliantCode, failFast(cmd))
			pcancel()
			// The proof doubles as this command's baseline: how long its
			// compliant run takes is what its mutants' cap is derived from.
			commandBaseline[key] = time.Since(pstart)
			if perr != nil {
				// FAIL CLOSED, as the compile gate does: a probe that could
				// not RUN says nothing about the command, and treating it as
				// failed would quietly discard the exam.
				return Report{}, fmt.Errorf("adequacy: proving grading command %v on compliant code: %w", cmd, perr)
			}
			if !passed {
				commandFailsOnCompliant[key] = strings.TrimSpace(lastLines(out, 12))
			}
		}
	}

	// DUPLICATE HUNKS ARE GRADED ONCE AND ANSWERED TWICE.
	//
	// Two mutants with the same (span, SEARCH, REPLACE, parent) are the same
	// edit of the same bytes: the jail would build the identical file and the
	// suite would return the identical answer, at the cost of a second full
	// suite run. Sharded generation produces them routinely (several seats
	// aimed at overlapping regions; a model asked for n distinct mutations
	// emitting one twice).
	//
	// THE DENOMINATOR IS NOT TOUCHED. Every duplicate still appears in
	// Killed/Survived, still gets its own PerMutant entry with the same
	// killed_by, and still counts in Total — it is simply not RUN a second
	// time to be told what its twin already proved. That is what keeps this a
	// speed change and not a measurement change: collapsing the DENOMINATOR
	// instead would move the kill rate whenever a set contained a repeat,
	// which is a different exam, not a faster one.
	//
	// Never across files by construction: every mutant here is a single-point
	// edit of the same compliantCode.
	repOf := make([]int, len(mutants))
	firstSeen := make(map[MutantIdentity]int, len(mutants))
	dupes := 0
	for i, m := range mutants {
		id := IdentityOf(m)
		if j, ok := firstSeen[id]; ok {
			repOf[i] = j
			dupes++
			continue
		}
		firstSeen[id] = i
		repOf[i] = i
	}
	rep.DuplicateMutants = dupes

	scoreOne := func(i int, m Mutant) {
		// THE FILE IS MATERIALISED HERE AND NOWHERE ELSE. A mutant is its hunk;
		// this is the one moment it has to become source, for exactly as long
		// as the jail needs it.
		//
		// An anchor that does not apply is an INVALID mutant, on the same
		// accounting path the compile gate uses — never a kill (nothing was
		// caught) and never a survivor (nothing was missed). It cannot happen
		// for a mutant this run generated, whose anchor was proven unique
		// against these exact bytes; it CAN happen for a replayed set whose
		// parent hash was somehow satisfied by different source, and that is
		// precisely the case that must not quietly become a number.
		mutantCode, aerr := m.Apply(compliantCode)
		if aerr != nil {
			outcomes[i] = outcome{invalid: true, invalidReason: invalidReasonAnchor + ": " + aerr.Error()}
			return
		}
		// The compile gate runs FIRST and is cheap relative to a suite run, so
		// an invalid mutant costs one type-check instead of a full execution.
		if len(cfg.mutantCompileCheck) > 0 {
			gctx, gcancel := context.WithTimeout(ctx, perMutant)
			for _, cmd := range cfg.mutantCompileCheck {
				ok, out, err := runCmdVerbose(gctx, mutantCode, cmd)
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
		if why, unsound := commandFailsOnCompliant[strings.Join(cmd, "\x00")]; unsound {
			outcomes[i] = outcome{
				unmeasured: true,
				unmeasuredReason: fmt.Sprintf("the command that would grade this mutant FAILS ON THE COMPLIANT CODE, so its verdict is not evidence about the mutant (command: %s): %s",
					strings.Join(cmd, " "), why),
				grading: grading,
			}
			return
		}
		// FAIL-FAST APPLIES HERE AND ONLY HERE: one mutant's own suite run.
		// A no-op unless the probe above proved the runner takes the flag.
		cmd = failFast(cmd)
		mutantCap := capFor(cmd)
		mctx, cancel := context.WithTimeout(ctx, mutantCap)
		// THE measurement: the single suite run that decides this mutant's
		// verdict. Recorded even when that run errors or times out, so a
		// mutant that ate its whole budget says so.
		mstart := time.Now()
		var (
			passed   bool
			err      error
			killedBy string
		)
		if nameKiller {
			var out []byte
			passed, out, err = runCmdDetailed(mctx, mutantCode, cmd)
			if err == nil && !passed {
				// A kill, and the output is the only thing entitled to say
				// which test made it one. "" when the summary says nothing.
				killedBy = cfg.failureParser.FirstFailure(out)
			}
		} else {
			passed, err = runCmd(mctx, mutantCode, cmd)
		}
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
				bctx, bcancel := context.WithTimeout(ctx, mutantCap)
				bpassed, berr := run(bctx, compliantCode)
				bcancel()
				if berr == nil {
					// THE RE-PROBE MUST PASS, not merely return. This read
					// `_, berr :=` and credited a kill whenever the compliant
					// run came back within budget — including when it came
					// back FAILING. A suite that is failing at that moment
					// (flaky, or the box is unhealthy) proves nothing about
					// the mutant, and "timed out, then compliant failed" is
					// evidence the environment is wrong, not that the mutant
					// was caught.
					if !bpassed {
						outcomes[i] = outcome{err: fmt.Errorf("mutant %s: run timed out, and the compliant baseline FAILED when re-run under the same budget (%s) — the suite is not stable right now, so nothing is inferred from this mutant", m.ID, mutantCap)}
						return
					}
					outcomes[i] = outcome{killed: true, grading: grading}
					return
				}
				if errors.Is(berr, ErrTestTimeout) {
					outcomes[i] = outcome{err: fmt.Errorf("mutant %s: run timed out AND the compliant baseline timed out under the same budget (%s) — the machine is too loaded to grade this mutant; nothing is inferred from it", m.ID, mutantCap)}
					return
				}
				outcomes[i] = outcome{err: berr}
				return
			}
			outcomes[i] = outcome{err: err}
			return
		}
		// test PASSED on a violation => it did NOT catch it
		if !passed {
			grading.KilledBy = killedBy
		}
		outcomes[i] = outcome{killed: !passed, grading: grading}
	}

	if cfg.concurrency > 1 && len(mutants) > 1 {
		sem := make(chan struct{}, cfg.concurrency)
		var wg sync.WaitGroup
		for i, m := range mutants {
			if repOf[i] != i {
				continue
			}
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
			if repOf[i] != i {
				continue
			}
			scoreOne(i, m)
		}
	}

	// Every duplicate inherits its representative's outcome VERBATIM — the
	// same verdict, the same killed_by, the same invalid reason — because it
	// is the same edit and would have produced exactly that. Duration is
	// inherited too and is the ONE field that is a claim about a run that did
	// not happen; it is the twin's real measurement of the identical work, and
	// mutantSpread reads a set of durations, not a bill.
	for i := range mutants {
		j := repOf[i]
		if j == i {
			continue
		}
		o := outcomes[j]
		if o.grading != nil {
			g := *o.grading
			o.grading = &g
		}
		outcomes[i] = o
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
		if outcomes[i].unmeasured {
			rep.Unmeasured = append(rep.Unmeasured, m.ID)
			if rep.UnmeasuredReasons == nil {
				rep.UnmeasuredReasons = map[string]string{}
			}
			rep.UnmeasuredReasons[m.ID] = outcomes[i].unmeasuredReason
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

// lastLines returns the final n lines of s — a failing command's summary is at
// the end of its output, which is the half that explains itself.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
