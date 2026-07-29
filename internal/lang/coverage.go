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
}

// goPlugin implements CoverageReporter; verified against corral's own suite
// (see the task-1 report). Compile-time assertion — the pattern is optional
// per-plugin (type-asserted by callers, mirroring advpool's verboseJail), so
// this line, not the Plugin interface, is what keeps it from silently
// bit-rotting.
var _ CoverageReporter = goPlugin{}
