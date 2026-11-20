// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"slices"
	"strings"
)

// swiftCoverageHeaderV2 opens the reduced report swiftPlugin.CoverageCmd
// prints: the shared line-count grammar (corralCoverageCounts), version 2 as
// in every language that speaks it.
//
// swiftCoverageUnavailable opens the line printed INSTEAD of a report when
// there is none to read, followed by the reason: SwiftPM could not say where
// it writes the report, the run wrote none, or what it wrote is not a whole
// export. It shares the header's family for Rust's reason
// (rustCoverageUnavailable): read as a header it would be named a stale
// reducer, and the operator would be told about a version instead of what
// went wrong.
const (
	swiftCoverageHeaderV2    = "corral-swift-coverage: v2"
	swiftCoverageUnavailable = "corral-swift-coverage: unavailable"
)

// swiftCoverageScratch is the build directory of the coverage run, inside
// the package's own .build so that everything under it is SwiftPM's (the
// reducer skips .build, llvmCovSkipped) and is gone with it.
const swiftCoverageScratch = ".build/corral-coverage"

// swiftCoverageMovedBuild are the options that put the package or its build
// directory somewhere other than the working directory and its .build.
var swiftCoverageMovedBuild = []string{"--package-path", "--scratch-path", "--build-path"}

// CoverageCmd wraps a `swift test` run in SwiftPM's own coverage:
// --enable-code-coverage, which builds the tests with the toolchain's
// profiling instrumentation and, after the run, has the toolchain's llvm-cov
// export the counts as JSON. No tool beyond the Swift toolchain is needed.
//
// The script, in order:
//
//  1. Asks `swift test ... --show-codecov-path` where the export will be
//     written (it depends on the build configuration and the scratch path,
//     so it is asked with the same arguments) and removes any export an
//     earlier run left there, so a run that writes none is never read as one
//     that did. A command that cannot say where is disclosed as
//     unavailable, and the suite is not run: a report nobody can find is
//     only cost.
//  2. Runs the operator's command with --enable-code-coverage and its own
//     scratch path (below) added after the operator's arguments, its output
//     sent to stderr, and prints the suite's exit status (SuiteExit).
//  3. Reduces the export with the reducer Rust shares (llvmCovReduceShell)
//     to one `<executed> <executable> <path>` line per file, paths relative
//     to the working directory. Files outside it, and under its .build
//     (dependency checkouts, and the test runner and test discovery sources
//     SwiftPM generates and compiles into the suite) and .swiftpm, are not
//     the project's and are left out. An export that is empty or cut short
//     is disclosed as unavailable.
//
// A RUN THAT FAILS WRITES NO REPORT. Measured on Swift 6.3.2 with the
// coverage fixture (testdata/coverage/swift): a run with a failing test
// exited 1 and wrote no export, as did a build that failed (exit 1); a run
// whose --filter selected no test exited 0 and wrote one. So where the other
// reducers read a failing suite's coverage, Swift has none to read, and the
// script says so with the exit status, as unavailable, rather than print an
// empty report.
//
// THE COVERAGE BUILD HAS ITS OWN SCRATCH PATH, .build/corral-coverage,
// because in the default one each kind of build evicts the other. Measured
// on taplin/Lariat at be0c5fd with Swift 6.3.2, in Docker with the network
// off after its dependencies were resolved: an ordinary `swift build
// --build-tests` that rebuilt nothing took 2.7 s; after one
// `swift test --enable-code-coverage` (60 s, recompiling) the same ordinary
// build recompiled again (1,026 compile steps, 56.2 s). With the coverage run
// in its own scratch path the ordinary build after it recompiled nothing
// (0.4 s), and a second coverage run recompiled nothing either (4.6 s); the
// first one there took 81.0 s. The cost is disk for a second build, as for
// cargo's target/corral-coverage (rustPlugin.CoverageCmd). That first run
// resolved the dependencies into the new scratch path from SwiftPM's own
// cache in the home directory ("Fetching ... from cache"), with the network
// off; on a machine whose cache does not hold them it needs the network.
//
// ok=false for anything but a `swift test` run the row also runs
// (swiftTestAt: no shell around it, no subcommand, no "--"), and for one
// carrying --package-path, --scratch-path or --build-path (the export's paths
// and the build directory would not be where the reducer places and skips
// them), --disable-code-coverage or --show-codecov-path.
func (swiftPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	if _, ok := swiftTestAt(testCmd); !ok {
		return nil, false
	}
	for _, a := range testCmd {
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains(swiftCoverageMovedBuild, name) || a == "--disable-code-coverage" || a == "--show-codecov-path" {
			return nil, false
		}
	}
	run := slices.Clone(testCmd)
	if !slices.Contains(run, "--enable-code-coverage") {
		run = append(run, "--enable-code-coverage")
	}
	run = append(run, "--scratch-path", swiftCoverageScratch)

	setup := `[ -d "$d" ] || exit 1` + "\n" +
		`unavailable() { printf '%s %s\n' ` + shellQuote(swiftCoverageUnavailable) + ` "$1"; exit 0; }` + "\n" +
		`p=$("$@" --show-codecov-path 2>"$d/where") || unavailable "swift test --show-codecov-path, which names where SwiftPM writes the coverage report, failed (exit $?): $(tail -n 1 "$d/where")"` + "\n" +
		`p=$(printf '%s\n' "$p" | tail -n 1)` + "\n" +
		`[ -n "$p" ] || unavailable "swift test --show-codecov-path named no file"` + "\n" +
		`rm -f "$p" || exit 1` + "\n"
	reduce := "\n" +
		`[ -f "$p" ] || unavailable "swift test exited $rc and wrote no coverage report at $p; on Swift 6.3.2 a run with a failing test or a build that failed writes none"` + "\n" +
		llvmCovReduceShell(swiftPlugin{}, swiftCoverageHeaderV2, `"$p"`)
	return coverageRunAndReduce(setup, "", run, reduce), true
}

// ParseCoverage is derived from CoverageCounts: a file is executed when any
// of its lines ran. Swift has no divergence from that rule.
func (p swiftPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	return executedFromCounts(p.CoverageCounts(stdout, modulePath))
}

// CoverageCounts reads the report CoverageCmd prints (the shared grammar,
// corralCoverageCounts). A swiftCoverageUnavailable line is an ERROR carrying
// its reason: there was no report to read, and saying why is the whole of
// what the run produced.
func (swiftPlugin) CoverageCounts(stdout, modulePath string) (map[string]LineCounts, error) {
	if reason, ok := CoverageUnavailable(stdout); ok {
		return nil, fmt.Errorf("lang: swift coverage is unavailable: %s", reason)
	}
	return corralCoverageCounts(stdout, swiftCoverageHeaderV2, "swift", modulePath)
}
