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
// there is none to read, followed by the reason: the toolchain's LLVM tools
// are not beside its swift, SwiftPM could not say where its coverage goes,
// the run did not pass or wrote no profile, or merging and exporting the
// profiles failed. It shares the header's family for Rust's reason
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

// CoverageCmd wraps a `swift test` run in LLVM's source-based coverage:
// SwiftPM's --enable-code-coverage builds the tests with the toolchain's
// profiling instrumentation, and each test process writes a raw profile into
// SwiftPM's codecov directory. corral merges those profiles and exports their
// counts itself, with the mechanism Rust's coverage shares
// (llvmCovMergeExportShell), rather than read the export SwiftPM writes. No
// tool beyond the Swift toolchain is needed.
//
// The script, in order:
//
//  1. Finds llvm-profdata, llvm-cov and llvm-objdump beside the swift the
//     command runs, after following its links (CI puts a link to the pinned
//     toolchain's swift first on PATH in a directory of its own). The
//     toolchain's own are the ones that read its profiles: an llvm-profdata
//     of another LLVM release refused a profile's format in CI ("raw profile
//     version mismatch", Rust's coverage, 2026-11-20). One missing is
//     disclosed as unavailable, with no other copy looked for, and the suite
//     is not run: a report nobody can read is only cost.
//  2. Asks `swift test ... --show-codecov-path` where SwiftPM writes its
//     coverage (it depends on the build configuration and the scratch path,
//     so it is asked with the same arguments). That file's directory is where
//     the run writes its profiles; any an earlier run left there are removed,
//     so they are never read as this run's. A command that cannot say where
//     is disclosed as unavailable, and the suite is not run.
//  3. Runs the operator's command with --enable-code-coverage and its own
//     scratch path (below) added after the operator's arguments, its output
//     sent to stderr, and prints the suite's exit status (SuiteExit).
//  4. Reads the run only if it passed and wrote a profile (below).
//  5. Lists the objects: every file in SwiftPM's products directory, the
//     codecov directory's parent, that carries a coverage map (an
//     __llvm_covmap section, by the toolchain's llvm-objdump). Then merges
//     the profiles, exports with -summary-only over those objects, and
//     reduces the export (llvmCovMergeExportShell) to one `<executed>
//     <executable> <path>` line per file, paths relative to the working
//     directory. Files outside it, and under its .build (dependency
//     checkouts, and the test runner and test discovery sources SwiftPM
//     generates and compiles into the suite) and .swiftpm, are not the
//     project's and are left out. An export that is empty or cut short is
//     disclosed as unavailable.
//
// WHY NOT THE EXPORT SWIFTPM WRITES. Swift 6.4 made swiftbuild SwiftPM's
// default build system (6.3.2's default was native), and under it each test
// target is its own test product, while SwiftPM's export covers one of them.
// Measured 2026-11-20 in Docker on the coverage fixture
// (testdata/coverage/swift) and on a copy of it with a library target only
// the XCTest target depends on and tests: on 6.4.2 under swiftbuild SwiftPM's
// export held the swift-testing target's files and not the XCTest target's,
// nor the library only it links, though its tests ran; on 6.3.2 with
// --build-system swiftbuild it held none of the project's files at all. A
// file missing from the report reads as never run. Pinning --build-system
// native gave a whole export on both versions, but 6.4.2 warns that native is
// deprecated and will be removed. Merging the profiles over every product
// works under either: the same day, on Swift 6.3.2 (swift:6.3.2-noble) and
// 6.4.2 (swift:6.4.2-resolute), under native and under swiftbuild, the steps
// above reduced the fixture to the committed report
// (testdata/coverage/swift-report.txt) and the copy to the same counts plus
// the library (3 of 3 lines) and the second XCTest test, all four ways alike.
// The objects were the one CalcPackageTests.xctest under native, and under
// swiftbuild each test target's .so and -test-runner plus each library
// target's .o; in every case the codecov directory's parent held them. With
// an executable target added that no test target depends on, its product
// carried a coverage map too, and its main.swift was reported with no line
// executed (0 of 1) where SwiftPM's export leaves the file out; either way it
// reads as never run, and every other file's counts were the same. So the
// build system is the operator's: none is added and none refused. Only Linux
// was measured. On macOS a test product is a bundle directory, which step 5
// does not list, so what it reports there is not known.
//
// A RUN THAT DID NOT PASS IS NOT READ. Measured the same day on both
// versions and both build systems, on copies of the fixture: a failing
// XCTest assertion exited 1 with every test process's profile written; a
// test that trapped (a failed precondition) exited 1 with no profile from
// the process that crashed (one of two under native, six of eight under
// swiftbuild); a build that failed exited 1 and wrote none. The exit status
// does not tell a failed assertion from a crash, so any run that did not pass
// is disclosed as unavailable with its status, where Rust's coverage reads a
// failing suite (cargo's own exit code tells that apart). A run whose
// --filter selected no test exited 0 and wrote profiles, and is read.
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
// ok=false for anything but a `swift test` run the row also runs
// (swiftTestAt: no shell around it, no subcommand, no "--"), and for one
// carrying --package-path, --scratch-path or --build-path (the export's paths
// and the build directory would not be where the reducer places and skips
// them), --disable-code-coverage, or --show-codecov-path.
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
		`t=$(command -v "$1") && t=$(readlink -f "$t") || unavailable "$1, the swift the command runs, could not be found and its links followed"` + "\n" +
		`pd="${t%/*}/llvm-profdata" cov="${t%/*}/llvm-cov" od="${t%/*}/llvm-objdump"` + "\n" +
		`for x in "$pd" "$cov" "$od"; do [ -x "$x" ] || unavailable "$x is missing: Swift coverage reads the run's profiles with the LLVM tools of the toolchain whose swift runs it ($t)"; done` + "\n" +
		`p=$("$@" --show-codecov-path 2>"$d/where") || unavailable "swift test --show-codecov-path, which names where SwiftPM writes the run's coverage, failed (exit $?): $(tail -n 1 "$d/where")"` + "\n" +
		`p=$(printf '%s\n' "$p" | tail -n 1)` + "\n" +
		`[ -n "$p" ] || unavailable "swift test --show-codecov-path named no file"` + "\n" +
		`c=${p%/*}` + "\n" +
		`rm -f "$c"/*.profraw || exit 1` + "\n"
	reduce := "\n" +
		`[ "$rc" -eq 0 ] || unavailable "swift test exited $rc, so its coverage is not read: a test process that crashes writes no profile, and the exit status does not tell a crash from a failing test"` + "\n" +
		`set -- "$c"/*.profraw` + "\n" +
		`[ -e "$1" ] || unavailable "swift test exited 0 and wrote no coverage profile in $c"` + "\n" +
		`for o in "${c%/*}"/*; do [ -f "$o" ] && "$od" --section-headers "$o" 2>/dev/null | grep -q __llvm_covmap && printf '%s\n' "$o"; done > "$d/objs"` + "\n" +
		llvmCovMergeExportShell(swiftPlugin{}, swiftCoverageHeaderV2, `"$c"/*.profraw`, "no file in SwiftPM's products directory carries a coverage map, so there is no test product to export the profiles over")
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
