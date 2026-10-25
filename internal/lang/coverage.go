// SPDX-License-Identifier: Elastic-2.0

package lang

// CoverageReporter is implemented by plugins that can report, in ONE
// instrumented suite run, which source files the suite actually executed.
//
// It answers the same question the canary answers per file, for every file
// at once — but it is an INSTRUMENT'S REPORT, not a proof. Coverage tooling
// has blind spots (native extensions, subprocesses, dynamic import), and a
// file it fails to see is not thereby untested. Callers must label a finding
// derived from this as coverage-grade, never as proven, and must never treat
// a missing, failing, or unparseable report as "nothing is covered".
type CoverageReporter interface {
	// CoverageCmd wraps the project's own test command in coverage
	// instrumentation, emitting a machine-readable report ON STDOUT.
	// ok=false means this language has no supported invocation — the caller
	// must report that it could not run, never assume an empty result.
	CoverageCmd(testCmd []string) (cmd []string, ok bool)

	// ParseCoverage extracts the repo-relative paths executed at least once.
	// modulePath is the language's path prefix to strip ("" when unused).
	// Pure. An unparseable report is an ERROR, never an empty map.
	ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error)

	// CoverageCounts reads the same report as per-file LINE COUNTS: how
	// many of the file's executable lines the run executed, out of how many
	// it measured. It keeps ParseCoverage's tri-state — present with
	// Executed>0 is executed, present with Executed==0 is measured and never
	// run, ABSENT is never measured — and ParseCoverage agrees with it
	// (executed[p] == (counts[p].Executed > 0)), which
	// TestParseCoverageAgreesWithCoverageCounts holds, with ONE allowed
	// divergence: Go's ParseCoverage may be true while Executed==0, and only
	// when every block of that file that ran has zero statements (an empty
	// function body, whose block is entered but adds nothing to a statement
	// count). That file read executed before counts existed and must not
	// move; evidence pairing reads Executed, so such a file is never
	// promoted by unit evidence. No other reporter may diverge. An unparseable report
	// is an ERROR, and a reporter never makes up a count it was not given.
	//
	// The counts exist so evidence can name the test unit that executed
	// MOST of a file, which a boolean cannot rank.
	CoverageCounts(stdout, modulePath string) (map[string]LineCounts, error)
}

// LineCounts is one file's line coverage from one report: Executed of its
// Executable lines ran. What counts as an executable line is each
// instrument's own answer (see each reporter's CoverageCounts), so counts
// rank units within one language and are never compared across languages.
type LineCounts struct {
	Executed, Executable int
}

// goPlugin implements CoverageReporter; verified against corral's own suite
// (see the task-1 report). Compile-time assertion — the pattern is optional
// per-plugin (type-asserted by callers, mirroring advpool's verboseJail), so
// this line, not the Plugin interface, is what keeps it from silently
// bit-rotting.
var _ CoverageReporter = goPlugin{}

// pyPlugin implements CoverageReporter too; verified against a real clone of
// pallets/flask (see the task-2 report).
var _ CoverageReporter = pyPlugin{}

// rubyPlugin implements CoverageReporter using Ruby's STDLIB `coverage`, so
// an audited project needs no SimpleCov in its Gemfile — see
// rubyPlugin.CoverageCmd for why RUBYOPT is the only window that works.
var _ CoverageReporter = rubyPlugin{}

// jsPlugin implements CoverageReporter using NODE_V8_COVERAGE, which is built
// into Node — no c8, no nyc, and no dev dependency added to the audited repo.
var _ CoverageReporter = jsPlugin{}

// tsPlugin implements CoverageReporter by delegating to jsPlugin: the
// instrument is the Node runtime, and Node strips TypeScript types natively.
var _ CoverageReporter = tsPlugin{}

// phpPlugin implements CoverageReporter via pcov (or Xdebug), injected with
// PHP_INI_SCAN_DIR so `vendor/bin/phpunit` and `composer test` are instrumented
// as readily as a bare `php`. PHP is the one language here that needs a runtime
// EXTENSION — see phpPlugin.CoverageCmd for why that is disclosed rather than
// worked around.
var _ CoverageReporter = phpPlugin{}

// rustPlugin implements CoverageReporter with the toolchain's own
// instrumentation (rustc's -C instrument-coverage and the llvm-tools rustup
// component), not cargo-llvm-cov: that is a separate install the audited
// repository may not have, and what it does here is three commands
// rustPlugin.CoverageCmd's script runs itself. A toolchain without
// llvm-tools is disclosed as unavailable, never read as an empty report.
var _ CoverageReporter = rustPlugin{}

// executedFromCounts is ParseCoverage derived from CoverageCounts, the one
// place the rule lives for every reporter but Go (whose one allowed
// divergence is stated on CoverageReporter.CoverageCounts): a file is executed when any of its lines ran, and the
// key set — the tri-state's third state — is carried over unchanged.
func executedFromCounts(counts map[string]LineCounts, err error) (map[string]bool, error) {
	if err != nil {
		return nil, err
	}
	executed := make(map[string]bool, len(counts))
	for p, c := range counts {
		executed[p] = c.Executed > 0
	}
	return executed, nil
}
