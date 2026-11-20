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

// swiftCodecovPathOptions are `swift test`'s option that prints where SwiftPM
// writes its coverage, and runs no test, under each of its names: `swift test
// --help` lists the last two as aliases of the first on Swift 6.3.2 and
// 6.4.2, and each printed the same path and ran no test (2026-11-20, on the
// coverage fixture). CoverageCmd asks with the first, and refuses a command
// carrying any of them: run with one, the suite would not run at all.
var swiftCodecovPathOptions = []string{"--show-codecov-path", "--show-code-coverage-path", "--show-coverage-path"}

// CoverageCmd wraps a `swift test` run in LLVM's source-based coverage:
// SwiftPM's --enable-code-coverage builds the tests with the toolchain's
// profiling instrumentation, and each test process writes a raw profile into
// SwiftPM's codecov directory. corral merges those profiles and exports their
// counts itself, with the mechanism Rust's coverage shares
// (llvmCovMergeExportShell), rather than read the export SwiftPM writes.
//
// The script, in order:
//
//  1. Finds llvm-profdata and llvm-cov beside the swift the command runs,
//     after following its links (CI puts a link to the pinned toolchain's
//     swift first on PATH in a directory of its own), and readelf on PATH.
//     The toolchain's own LLVM tools are the ones that read its profiles: an
//     llvm-profdata of another LLVM release refused a profile's format in CI
//     ("raw profile version mismatch", Rust's coverage, 2026-11-20). readelf
//     only reads an ELF note, which has no such version; it is binutils',
//     which swift.org lists among the Swift toolchain's Linux dependencies.
//     So an install whose llvm-profdata and llvm-cov are not beside the
//     resolved swift, or a host with no readelf, gets coverage disclosed as
//     unavailable, with no other copy looked for, and the suite is not run: a
//     report nobody can read is only cost.
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
//  5. Lists the objects: the binaries THIS RUN's profiles name. Each raw
//     profile carries the GNU build ID of the binary that wrote it
//     (`llvm-profdata show --binary-ids`); the objects are the files of
//     SwiftPM's products directory, the codecov directory's parent, whose
//     build ID (`readelf -n`) is one of those. Every build ID the profiles
//     name must be found there: a binary that ran and cannot be found, or
//     profiles that name none, are disclosed as unavailable rather than read
//     without it. Then merges the profiles, exports with -summary-only over
//     those objects, and reduces the export to one `<executed> <executable>
//     <path>` line per file, paths relative to the working directory. Files
//     outside it, and under its .build (dependency checkouts, and the test
//     runner and test discovery sources SwiftPM generates and compiles into
//     the suite) and .swiftpm, are not the project's and are left out. An
//     export that is empty or cut short is disclosed as unavailable.
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
// deprecated and will be removed. So the build system is the operator's: none
// is added and none refused.
//
// WHY THE OBJECTS ARE CHOSEN BY BUILD ID. The coverage build directory
// outlives a run (the workspace substrate reuses it, an operator re-runs, CI
// may cache .build), and under swiftbuild a removed test target's product
// stays in the products directory with the coverage map of the code it was
// built from. Measured 2026-11-20 on Swift 6.3.2 and 6.4.2, each under
// native and swiftbuild, on the fixture plus a test target ExtraTests, run
// once, then run again in the same build directory after ExtraTests was
// removed and Calc.swift lost `sub`:
//   - every file in the products directory that carries a coverage map, as
//     this step first took them: under swiftbuild the stale ExtraTests.so and
//     ExtraTests-test-runner were taken, the report held ExtraTests.swift
//     (0 of 5) and read Calc.swift as 3 of 6 lines where a fresh build
//     directory gives 3 of 3; under native the one CalcPackageTests.xctest is
//     relinked and nothing was stale. Every current product had been
//     relinked by the second run, so the stale lines came from the leftover
//     products alone.
//   - by build ID: the profiles named exactly the current products (one under
//     native; the two .so and two -test-runner under swiftbuild, not the
//     library's .o, which no process runs and whose code they link), and the
//     report was the fresh directory's, all four ways.
//   - only files newer than a mark made before the run: ruled out, because an
//     unchanged rerun rewrote no product on either build system.
//   - deleting the products directory's files before the run: SwiftPM
//     restored every current one and not the stale ones, and the report was
//     right, but it relinks every product on every run (an unchanged rerun of
//     the fixture took 0.3 s against 0.7 s under native, 0.5-0.7 s against
//     1.3-1.7 s under swiftbuild) and deletes files in a directory SwiftPM
//     owns.
//   - naming the products from the manifest would mean knowing each build
//     system's file names for them; the build ID needs none.
//
// The products directory being the codecov directory's parent is the one
// fact of SwiftPM's layout this relies on (it held on all four), and if it
// stopped holding, the run's binaries would not be found and the report
// would be disclosed as unavailable, not read short. With the fixture,
// the copy with a library target and the ExtraTests scenario, the script's
// reports were the fresh directory's on 6.3.2 and 6.4.2 under native and
// swiftbuild; an executable target no test runs writes no profile, so its
// files are absent from the report, as from SwiftPM's export, and read as
// never run.
//
// Only Linux was run. On macOS the code would disclose coverage as
// unavailable at one of its checks (no llvm-profdata or llvm-cov beside the
// swift there, which is a shim on Xcode's toolchain; no readelf; or Mach-O
// binaries, which carry no GNU build ID readelf reads, so the profiles'
// binaries are not found); none of that was run.
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
// them), --disable-code-coverage, or --show-codecov-path under any of its
// names (swiftCodecovPathOptions).
func (swiftPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	return swiftCoverageScript(testCmd, llvmCovReduceShell(swiftPlugin{}, swiftCoverageHeaderV2, `"$d/export.json"`))
}

// SwiftCoverageExportCmd is CoverageCmd's script, every step the same, ending
// with the raw llvm-cov export (after the `# suite-exit` line) instead of its
// reduction. It exists for scripts/capture-runner-fixtures.sh, which records
// that export as a fixture (testdata/runners/swift-codecov.json) through
// scripts/swiftcoverageexport, so the capture runs the plugin's own steps and
// not a copy of them. Nothing in corral's audit runs it.
func SwiftCoverageExportCmd(testCmd []string) (cmd []string, ok bool) {
	return swiftCoverageScript(testCmd, `cat "$d/export.json"`+"\n")
}

// swiftCoverageScript builds CoverageCmd's script (see there) for testCmd,
// ending with tail once "$d/export.json" holds the export.
func swiftCoverageScript(testCmd []string, tail string) (cmd []string, ok bool) {
	if _, ok := swiftTestAt(testCmd); !ok {
		return nil, false
	}
	for _, a := range testCmd {
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains(swiftCoverageMovedBuild, name) || a == "--disable-code-coverage" || slices.Contains(swiftCodecovPathOptions, name) {
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
		`pd="${t%/*}/llvm-profdata" cov="${t%/*}/llvm-cov"` + "\n" +
		`for x in "$pd" "$cov"; do [ -x "$x" ] || unavailable "$x is missing: Swift coverage reads the run's profiles with the LLVM tools of the toolchain whose swift runs it ($t)"; done` + "\n" +
		`command -v readelf >/dev/null 2>&1 || unavailable "readelf (binutils) is not on PATH: Swift coverage reads each product's build ID with it, to export over only the binaries this run's profiles name"` + "\n" +
		`p=$("$@" ` + swiftCodecovPathOptions[0] + ` 2>"$d/where") || unavailable "swift test ` + swiftCodecovPathOptions[0] + `, which names where SwiftPM writes the run's coverage, failed (exit $?): $(tail -n 1 "$d/where")"` + "\n" +
		`p=$(printf '%s\n' "$p" | tail -n 1)` + "\n" +
		`[ -n "$p" ] || unavailable "swift test ` + swiftCodecovPathOptions[0] + ` named no file"` + "\n" +
		`c=${p%/*}` + "\n" +
		`rm -f "$c"/*.profraw || exit 1` + "\n"
	read := "\n" +
		`[ "$rc" -eq 0 ] || unavailable "swift test exited $rc, so its coverage is not read: a test process that crashes writes no profile, and the exit status does not tell a crash from a failing test"` + "\n" +
		`set -- "$c"/*.profraw` + "\n" +
		`[ -e "$1" ] || unavailable "swift test exited 0 and wrote no coverage profile in $c"` + "\n" +
		// The build IDs this run's profiles name, one per line.
		`bad=; for f in "$c"/*.profraw; do "$pd" show --binary-ids "$f" > "$d/show" || { brc=$?; bad=$f; break; }; awk '/^Binary IDs:/ { on = 1; next } on && /^[0-9a-f]+$/ { print; next } { on = 0 }' "$d/show"; done > "$d/rawids"` + "\n" +
		// The failure is disclosed after the loop, and the IDs sorted after it:
		// inside it, unavailable's notice would go where the loop's output goes
		// and be read as a build ID (and in a pipe its exit would end only the
		// pipe's subshell).
		`[ -z "$bad" ] || unavailable "$pd show --binary-ids failed (exit $brc) on $bad"` + "\n" +
		`sort -u "$d/rawids" > "$d/ids" || exit 1` + "\n" +
		`[ -s "$d/ids" ] || unavailable "this run's profiles name no binary build ID, so the products that ran cannot be told from ones an earlier build left"` + "\n" +
		// Each product whose build ID is one of them: "<id> <path>".
		`for o in "${c%/*}"/*; do [ -f "$o" ] || continue; i=$(LC_ALL=C readelf -n "$o" 2>/dev/null | sed -n 's/^ *Build ID: *//p'); [ -n "$i" ] && grep -qx "$i" "$d/ids" && printf '%s %s\n' "$i" "$o"; done > "$d/found"` + "\n" +
		`cut -d ' ' -f 2- "$d/found" > "$d/objs"` + "\n" +
		`cut -d ' ' -f 1 "$d/found" | sort -u | comm -23 "$d/ids" - > "$d/missing"` + "\n" +
		`[ ! -s "$d/missing" ] || unavailable "$(wc -l < "$d/missing" | tr -d ' ') binary(ies) that wrote a profile in this run are not in SwiftPM's products directory ${c%/*} (build ID $(tr '\n' ' ' < "$d/missing")), so the report would leave out what they ran"` + "\n" +
		llvmCovMergeExportShell(`"$c"/*.profraw`, "no product in SwiftPM's products directory has a build ID this run's profiles name") +
		tail
	return coverageRunAndReduce(setup, "", run, read), true
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
