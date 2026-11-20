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

// swiftCoverageBuildSystem is the one build system the coverage run uses: the
// only one whose export was measured to hold every test target's files (see
// CoverageCmd, THE COVERAGE BUILD USES THE NATIVE BUILD SYSTEM).
const swiftCoverageBuildSystem = "native"

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
// whose --filter selected no test exited 0 and wrote one. On Swift 6.4.2 with
// the native build system (below), measured 2026-11-20 on a copy of the
// fixture, a failing XCTest test and a build that failed each exited 1 and
// wrote no export either. So where the other
// reducers read a failing suite's coverage, Swift has none to read, and the
// script says so with the exit status, as unavailable, rather than print an
// empty report.
//
// THE COVERAGE BUILD HAS ITS OWN SCRATCH PATH, .build/corral-coverage,
// because in the default one each kind of build evicts the other. Measured
// 2026-11-19 on taplin/Lariat at be0c5fd with Swift 6.3.2, in Docker on one
// 32-core machine with the network off after its dependencies were resolved: an ordinary `swift build
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
// THE COVERAGE BUILD USES THE NATIVE BUILD SYSTEM (--build-system native,
// added unless the operator's command already names it), because SwiftPM's
// export under the other one leaves files out. Measured 2026-11-20 in Docker
// on the coverage fixture, and on a copy of it with a library target that only
// the XCTest target depends on and tests: Swift 6.4 made swiftbuild the
// default build system (6.3.2's default was native), and under it each test
// target is its own test product and the export SwiftPM writes covered one of
// them. On 6.4.2 the export held the swift-testing target's files and not the
// XCTest target's, nor the library only that target links, though its tests
// ran; on 6.3.2 with --build-system swiftbuild it held none of the project's
// files at all. A file missing from the report reads as never run. With
// --build-system native, on 6.3.2 and on 6.4.2 alike, the export held every
// file, with the same counts. Swift 6.4.2 prints a warning that native is
// deprecated and will be removed; when a toolchain no longer has it,
// --show-codecov-path fails and the report is disclosed as unavailable with
// that reason, never read from the other build system. The tests themselves
// (the row's runs, the mutants) build with whatever the operator's command
// names; only this run, in its own scratch path, is pinned.
//
// ok=false for anything but a `swift test` run the row also runs
// (swiftTestAt: no shell around it, no subcommand, no "--"), and for one
// carrying --package-path, --scratch-path or --build-path (the export's paths
// and the build directory would not be where the reducer places and skips
// them), --disable-code-coverage, --show-codecov-path, or a --build-system
// other than native (the operator chose a build system whose export is not
// whole, and corral does not override the choice).
func (swiftPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	if _, ok := swiftTestAt(testCmd); !ok {
		return nil, false
	}
	namesBuildSystem := false
	for i, a := range testCmd {
		name, value, hasValue := strings.Cut(a, "=")
		if slices.Contains(swiftCoverageMovedBuild, name) || a == "--disable-code-coverage" || a == "--show-codecov-path" {
			return nil, false
		}
		if name == "--build-system" {
			if !hasValue {
				if i+1 >= len(testCmd) {
					return nil, false
				}
				value = testCmd[i+1]
			}
			if value != swiftCoverageBuildSystem {
				return nil, false
			}
			namesBuildSystem = true
		}
	}
	run := slices.Clone(testCmd)
	if !slices.Contains(run, "--enable-code-coverage") {
		run = append(run, "--enable-code-coverage")
	}
	if !namesBuildSystem {
		run = append(run, "--build-system", swiftCoverageBuildSystem)
	}
	run = append(run, "--scratch-path", swiftCoverageScratch)

	setup := `[ -d "$d" ] || exit 1` + "\n" +
		`unavailable() { printf '%s %s\n' ` + shellQuote(swiftCoverageUnavailable) + ` "$1"; exit 0; }` + "\n" +
		`p=$("$@" --show-codecov-path 2>"$d/where") || unavailable "swift test --show-codecov-path, which names where SwiftPM writes the coverage report, failed (exit $?): $(tail -n 1 "$d/where")"` + "\n" +
		`p=$(printf '%s\n' "$p" | tail -n 1)` + "\n" +
		`[ -n "$p" ] || unavailable "swift test --show-codecov-path named no file"` + "\n" +
		`rm -f "$p" || exit 1` + "\n"
	reduce := "\n" +
		`[ -f "$p" ] || unavailable "swift test exited $rc and wrote no coverage report at $p; on Swift 6.3.2 and 6.4.2 a run with a failing test or a build that failed writes none"` + "\n" +
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
