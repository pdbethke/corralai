#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-runner-fixtures.sh [--swift-only|--swift-coverage-only|--swift-hook-only] [--keep] [DIR] — runs the
# real jest, vitest, mocha, node --test, rspec, phpunit, go test, gotestsum,
# cargo test, cargo nextest and swift test against the fixture suites in
# internal/lang/testdata/runners/src, writes what each runner's LIST command
# prints into internal/lang/testdata/runners/, and checks that each runner's
# one-test command really runs one test.
#
# Why: a list parser tested against output we typed is tested against what we
# expect the runner to print. These fixtures are what it printed (spec:
# docs/superpowers/specs/2026-10-15-runner-table-design.md §2.4, "a capability
# ships only after it ran against the real runner"). The commands below are
# the ones the runner rows build (internal/lang/runner_*.go); keep them equal.
#
# Needs the network (npm, composer, go install) and node, ruby with rspec,
# php, go, cargo, cargo-nextest and swift (or SWIFT, below). DIR
# defaults to a temp dir, removed on exit unless --keep; a kept DIR is what
# CORRALAI_RUNNER_ROOT points TestRealRunnersListAndRunOne at. RSPEC overrides
# the rspec command (a box whose rspec shim is broken can pass `ruby -S ...`).
#
# SWIFT overrides the swift command, as RSPEC does rspec's, for a box with no
# Swift toolchain: it may be a container command line, run from the fixture
# package (DIR/swift) for every swift command below. Such a command must mount
# DIR at its own path (so the paths it prints are the host's, which the
# captures replace), start in DIR/swift, run as the invoking user (so the
# build directory it writes into DIR can be removed), and pass the two
# variables that plant the fixture's failures through, for example:
#   SWIFT="docker run --rm -u $(id -u):$(id -g) -e HOME=/tmp \
#     -e CORRAL_FAIL_TESTING -e CORRAL_FAIL_XCTEST -v /tmp/rf:/tmp/rf \
#     -w /tmp/rf/swift swift:6.3.2-noble swift" \
#     scripts/capture-runner-fixtures.sh --swift-only /tmp/rf
# --swift-coverage-only captures swift-codecov.json alone, the same way, and
# --swift-hook-only the failure hook's swift-*-<version>.txt captures alone
# (capture_swift_hook), once per toolchain, each run in a DIR of its own.
# --swift-only captures the Swift fixtures alone, needs nothing but SWIFT, and
# leaves PROVENANCE.txt as it is (a full run rewrites it, the Swift paragraph
# included, from swift_provenance). Either run checks `$SWIFT --version` before
# it writes anything, so a box without Swift stops there with every capture
# as it was, as a box without node stops at the npm pin check.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="$PWD"
OUT="$REPO/internal/lang/testdata/runners"

SWIFT_ONLY=0 SWIFT_COVERAGE_ONLY=0 SWIFT_HOOK_ONLY=0 KEEP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --swift-only) SWIFT_ONLY=1; shift ;;
    --swift-coverage-only) SWIFT_COVERAGE_ONLY=1; shift ;;
    --swift-hook-only) SWIFT_HOOK_ONLY=1; shift ;;
    --keep) KEEP=1; shift ;;
    *) break ;;
  esac
done
DIR="${1:-$(mktemp -d)}"
[ "$KEEP" = 1 ] || trap 'rm -rf "$DIR"' EXIT
RSPEC="${RSPEC:-rspec}"
SWIFT="${SWIFT:-swift}"

JEST=30.5.2 VITEST=5.1.0 MOCHA=12.0.3 PHPUNIT=11.5.57 PHPUNIT9=9.6.39 PHPUNIT10=10.5.67 PHPUNIT12=12.5.44

mkdir -p "$DIR"
cp -r "$OUT/src/." "$DIR/"
cd "$DIR"
# Before any capture is written (and after DIR exists, which a container
# SWIFT mounts): a missing toolchain must not leave empty or "command not
# found" captures in place of the committed ones.
$SWIFT --version >/dev/null 2>&1 || { echo "swift: \`$SWIFT --version\` failed; set SWIFT (see the header). Nothing was captured." >&2; exit 1; }

fail=0
want() { # want <runner> <expected> <got>
  if [ "$2" = "$3" ]; then echo "ok   $1: ran $3"; else echo "FAIL $1: ran $3, want $2"; fail=1; fi
}

# --- swift test (internal/lang/swift_runner.go) ----------------------------
# The package in src/swift has two test targets: CalcTests (swift-testing,
# five tests) and CalcXCTests (XCTest, three). Run from DIR/swift; $SWIFT may
# be a container (see the header), which is why every command is $SWIFT and
# none is `cd`-relative to it.
capture_swift() (
  cd "$DIR/swift"
  local pdir
  pdir="$(pwd -P)"
  # swiftout <fixture> <cmd...>: stdout+stderr, the capture directory as $DIR.
  swiftout() {
    local f="$1"; shift
    "$@" 2>&1 | sed -e "s#$pdir#\$DIR/swift#g" > "$OUT/$f" || true
  }
  # swiftran: the tests a run executed, from its own summaries: every
  # swift-testing "Test run with N tests" plus every XCTest run's total, the
  # "Executed N tests" that closes its outermost suite (the suites nested in
  # it repeat their part). Swift 6.4.2 under swiftbuild runs each test product
  # on its own and prints one summary of each framework per product, so they
  # are summed; reading the last of each counted 5 where 8 ran. The same
  # count as swiftRan in internal/lang/swift_runner_test.go.
  swiftran() {
    awk '
      /^Test Suite .* started at / { depth++; next }
      /^Test Suite .* (passed|failed) at / { depth--; outer = (depth == 0); next }
      /^[ \t]*Executed [0-9]+ tests?, with/ { if (outer) n += $2; outer = 0; next }
      /Test run with [0-9]+ tests? / { s = $0; sub(/.*Test run with /, "", s); sub(/ .*/, "", s); n += s }
      END { print n + 0 }'
  }
  # swiftcmd <fixture> <argv> <cmd...>: `# argv:` the operator's command, `$ `
  # the row's command (cmd, whose first word is swift), the run of cmd under
  # $SWIFT, then `# exit:` its exit status. Keep cmd equal to what the row
  # builds; the tests compare.
  swiftcmd() {
    local f="$1" argv="$2"; shift 2
    { echo "# argv: $argv"; echo "\$ $*"; $SWIFT "${@:2}" 2>&1; echo "# exit: $?"; } | sed -e "s#$pdir#\$DIR/swift#g" > "$OUT/$f" || true
  }
  $SWIFT --version 2>/dev/null > "$OUT/toolchain-swift-version.txt"
  swiftout swift-test-help.txt $SWIFT test --help
  # The listing builds the tests first; its build lines go to stderr, which
  # the enumerator does not read.
  $SWIFT test list 2>/dev/null > "$OUT/swift-list.txt"
  # `swift test list` takes no --filter or --skip: it lists all eight tests
  # whatever the command selects (swiftListCmd refuses such a command).
  want swift-list-ignores-filter 8 "$($SWIFT test list --filter parses 2>/dev/null | wc -l | tr -d ' ')"
  # The unit listing, under ListUnitsCmd's working-directory prelude, with the
  # capture directory written as /fixture.
  sh -c 'printf '"'"'corral-units-cwd %s\n'"'"' "$(pwd -P)" && exec "$@"' corral-units $SWIFT package describe --type json 2>/dev/null |
    sed -e "s#$pdir#/fixture/swift#g" > "$OUT/swift-units.txt" || true
  swiftout swift-pass.txt $SWIFT test
  CORRAL_FAIL_TESTING=1 swiftout swift-fail-testing.txt $SWIFT test
  CORRAL_FAIL_XCTEST=1 swiftout swift-fail-xctest.txt $SWIFT test
  # A build failure: the source no longer compiles, so no test runs.
  cp Sources/Calc/Calc.swift "$DIR/Calc.swift.orig"
  printf 'public func broken() -> Int { "not an Int" }\n' >> Sources/Calc/Calc.swift
  swiftout swift-build-fail.txt $SWIFT test
  cp "$DIR/Calc.swift.orig" Sources/Calc/Calc.swift
  # One test alone (swiftSingleCmd): the shorter of two ids sharing a prefix,
  # in each framework, each running exactly one test.
  swiftcmd swift-single-testing.txt "swift test" swift test --filter '^CalcTests\.parses\(\)(?:/|$)'
  want swift-single-testing 1 "$(sed '1,2d' "$OUT/swift-single-testing.txt" | swiftran)"
  swiftcmd swift-single-xctest.txt "swift test" swift test --filter '^CalcXCTests\.AdderTests/testAdds(?:/|$)'
  want swift-single-xctest 1 "$(sed '1,2d' "$OUT/swift-single-xctest.txt" | swiftran)"
  # The other two listed shapes: a test in a suite (with a display name) and
  # a parameterised test (its labels carry a ":"), each alone.
  swiftcmd swift-single-suite.txt "swift test" swift test --filter '^CalcTests\.Arithmetic/adds\(\)(?:/|$)'
  want swift-single-suite 1 "$(sed '1,2d' "$OUT/swift-single-suite.txt" | swiftran)"
  swiftcmd swift-single-param.txt "swift test" swift test --filter '^CalcTests\.Arithmetic/doubles\(value:\)(?:/|$)'
  want swift-single-param 1 "$(sed '1,2d' "$OUT/swift-single-param.txt" | swiftran)"
  # Why the pattern does not end in `$` alone: swift-testing matches --filter
  # against the id followed by "/<file>:<line>:<column>", so an id anchored
  # at the end selects nothing, and a run of nothing passes (exit 0).
  swiftcmd swift-filter-dollar.txt "swift test" swift test --filter '^CalcTests\.parses\(\)$'
  want swift-filter-dollar-runs-none "0 exit 0" "$(sed '1,2d' "$OUT/swift-filter-dollar.txt" | swiftran) exit $(sed -n 's/^# exit: //p' "$OUT/swift-filter-dollar.txt")"
  # Two --filter options select the union, so a command with its own filter
  # has no one-test or one-unit form (swiftNarrowable).
  want swift-filters-union 2 "$($SWIFT test --filter '^CalcTests\.parses\(\)(?:/|$)' --filter '^CalcXCTests\.AdderTests/testAdds(?:/|$)' 2>&1 | swiftran)"
  # Test targets (swiftUnitsCmd): one, then both.
  swiftcmd swift-units-one.txt "swift test" swift test --filter '^(?:CalcXCTests)\.'
  want swift-unit-one 3 "$(sed '1,2d' "$OUT/swift-units-one.txt" | swiftran)"
  swiftcmd swift-units-multi.txt "swift test" swift test --filter '^(?:CalcTests|CalcXCTests)\.'
  want swift-units-multi 8 "$(sed '1,2d' "$OUT/swift-units-multi.txt" | swiftran)"
  # The compile check (swiftPlugin.CompileCheck) builds one target. `# argv:`
  # is "-": the check is corral's own command, not built from the operator's.
  swiftcmd swift-build-target-test.txt - swift build --target CalcXCTests
  swiftcmd swift-build-target-source.txt - swift build --target Calc
  # A sibling test target that does not compile is not built with this one;
  # a scratch path of its own, so nothing built above stands in for a build.
  cp Tests/CalcTests/CalcTests.swift "$DIR/CalcTests.swift.orig"
  printf 'let broken: Int = "not an Int"\n' >> Tests/CalcTests/CalcTests.swift
  swiftcmd swift-build-target-scoped.txt - swift build --target CalcXCTests --scratch-path .build-scoped
  cp "$DIR/CalcTests.swift.orig" Tests/CalcTests/CalcTests.swift
  rm -rf .build-scoped
  # A mutant that does not compile fails the test target's build.
  printf 'public func broken() -> Int { "not an Int" }\n' >> Sources/Calc/Calc.swift
  swiftcmd swift-build-target-mutant.txt - swift build --target CalcXCTests
  cp "$DIR/Calc.swift.orig" Sources/Calc/Calc.swift
  want swift-build-target-exits "0 0 0 1" "$(for f in test source scoped mutant; do sed -n 's/^# exit: //p' "$OUT/swift-build-target-$f.txt"; done | tr '\n' ' ' | sed 's/ $//')"
  # The writer's one-type rule (swiftPlugin.TestWriterSystem): a corral-named
  # type in each framework builds and runs beside the target's own tests; a
  # top-level test named like one of the target's does not build.
  cp "$DIR/swift-authored/wrapped/CalcTests/Calc_corralTests.swift" Tests/CalcTests/
  cp "$DIR/swift-authored/wrapped/CalcXCTests/Calc_corralTests.swift" Tests/CalcXCTests/
  swiftcmd swift-authored-wrapped.txt "swift test" swift test --filter '^(?:CalcTests|CalcXCTests)\.CorralCalcTests/'
  want swift-authored-wrapped 3 "$(sed '1,2d' "$OUT/swift-authored-wrapped.txt" | swiftran)"
  rm Tests/CalcTests/Calc_corralTests.swift Tests/CalcXCTests/Calc_corralTests.swift
  cp "$DIR/swift-authored/collision/CalcTests/Calc_corralTests.swift" Tests/CalcTests/
  swiftcmd swift-authored-collision.txt - swift build --target CalcTests
  want swift-authored-collision-exit 1 "$(sed -n 's/^# exit: //p' "$OUT/swift-authored-collision.txt")"
  rm Tests/CalcTests/Calc_corralTests.swift
  # Declaration shapes the static test list reads (swift_names.go) that the
  # fixture's own tests do not show, listed as `swift test list` prints them.
  cp "$DIR/swift-authored/shapes/CalcTests/Shapes.swift" Tests/CalcTests/
  $SWIFT test list 2>/dev/null > "$OUT/swift-shapes-list.txt"
  want swift-shapes-listed 12 "$(wc -l < "$OUT/swift-shapes-list.txt" | tr -d ' ')"
  rm Tests/CalcTests/Shapes.swift
  # This body is a subshell (for its cd), so a failed want is its exit status.
  return "$fail"
)

# --- swift test's failure hook (internal/lang/swift_record.go) ---------------
# swift-testing's own JUnit record, which the row has it write with
# --xunit-output. Its file name and some of its behaviour differ between
# releases, so every capture here is named for the release that made it
# (swift-*-<version>.txt) and the function is run once per toolchain CI pins
# (6.3.2 and 6.4.2), each with its own DIR. The graded command is taken from
# the row (scripts/reportcmd), never copied; TestSwiftHookCaptureIsTheRowsCommand
# holds these lines to it. It runs where $SWIFT runs: every word of $SWIFT but
# the last, then the graded command, whose first word after the wrapper is
# swift. Needs Go on the host.
capture_swift_hook() (
  cd "$DIR/swift"
  local pdir v words
  pdir="$(pwd -P)"
  v="$($SWIFT --version 2>/dev/null | sed -n 's/^Swift version \([0-9][0-9.]*\).*/\1/p' | head -1)"
  [ -n "$v" ] || { echo "FAIL swift-hook: no version from \`$SWIFT --version\`"; return 1; }
  read -ra words <<<"$SWIFT"
  local in=("${words[@]:0:${#words[@]}-1}")
  local GRADED=()
  mapfile -d '' GRADED < <(cd "$REPO" && go run ./scripts/reportcmd swift swift test)
  [ "${#GRADED[@]}" -gt 0 ] || { echo "scripts/reportcmd gave no command for swift test" >&2; return 1; }
  # hookrun <fixture> <cmd...>: stdout+stderr of cmd run where $SWIFT runs,
  # then `# exit:` its status, the capture directory written as $DIR.
  hookrun() {
    local f="$1"; shift
    { "${in[@]}" "$@" 2>&1; echo "# exit: $?"; } | sed -e "s#$pdir#\$DIR/swift#g" > "$OUT/$f" || true
  }
  exitof() { sed -n 's/^# exit: //p' "$OUT/$1"; }
  # Where swift-testing writes its record, measured for the hook's suffixes
  # and for what it declines (swiftRecordWraps): the option alone, with
  # --parallel, given twice, and pointed beside /dev/null on a passing run.
  rm -rf xunit && mkdir -p xunit/plain xunit/parallel xunit/twice
  {
    "${in[@]}" swift test --xunit-output=xunit/plain/rec.xml >/dev/null 2>&1; echo "plain exit $?: $(cd xunit/plain && ls | tr '\n' ' ')"
    "${in[@]}" swift test --parallel --xunit-output=xunit/parallel/rec.xml >/dev/null 2>&1; echo "parallel exit $?: $(cd xunit/parallel && ls | tr '\n' ' ')"
    "${in[@]}" swift test --xunit-output=xunit/twice/operator.xml --xunit-output=xunit/twice/corral.xml >/dev/null 2>&1; echo "twice exit $?: $(cd xunit/twice && ls | tr '\n' ' ')"
    "${in[@]}" swift test --xunit-output=/dev/null.xml >/dev/null 2>&1; echo "devnull exit $?"
  } > "$OUT/swift-xunit-files-$v.txt"
  rm -rf xunit
  # A passing run, hooked and not: the same exit code. Then the hook unable to
  # make its directory (a mktemp that fails, put first on PATH; a TMPDIR
  # naming no directory fails swift itself): the option is left out, the run
  # passes, and no record is printed.
  hookrun "swift-hook-pass-$v.txt" "${GRADED[@]}"
  hookrun "swift-hook-unhooked-pass-$v.txt" swift test
  want "swift-hook-pass-exit-$v" "0 0" "$(exitof "swift-hook-pass-$v.txt") $(exitof "swift-hook-unhooked-pass-$v.txt")"
  mkdir -p "$DIR/nomktemp" && printf '#!/bin/sh\nexit 1\n' > "$DIR/nomktemp/mktemp" && chmod +x "$DIR/nomktemp/mktemp"
  # shellcheck disable=SC2016 # expanded by that sh, not here
  hookrun "swift-hook-fallback-$v.txt" sh -c 'PATH=$0:$PATH exec "$@"' "$DIR/nomktemp" "${GRADED[@]}"
  want "swift-hook-fallback-exit-$v" 0 "$(exitof "swift-hook-fallback-$v.txt")"
  want "swift-hook-fallback-says-$v" 1 "$(grep -c 'leaves its record option out' "$OUT/swift-hook-fallback-$v.txt")"
  want "swift-hook-fallback-no-block-$v" 0 "$(grep -c '^corral-record-begin' "$OUT/swift-hook-fallback-$v.txt" || true)"
  # Both frameworks failing, hooked and not (the printed part must match,
  # TestSwiftHookLeavesThePrintedOutputAlone), and XCTest alone failing.
  CORRAL_FAIL_TESTING=1 CORRAL_FAIL_XCTEST=1 hookrun "swift-hook-fail-$v.txt" "${GRADED[@]}"
  CORRAL_FAIL_TESTING=1 CORRAL_FAIL_XCTEST=1 hookrun "swift-hook-unhooked-$v.txt" swift test
  want "swift-hook-fail-exit-$v" "1 1" "$(exitof "swift-hook-fail-$v.txt") $(exitof "swift-hook-unhooked-$v.txt")"
  want "swift-hook-fail-block-$v" 1 "$(grep -c '^corral-record-end' "$OUT/swift-hook-fail-$v.txt")"
  CORRAL_FAIL_XCTEST=1 hookrun "swift-hook-xctest-$v.txt" "${GRADED[@]}"
  # Failure shapes the fixture does not show (src/swift-authored/edge), and
  # the ids `swift test list` gives them.
  cp "$DIR/swift-authored/edge/CalcTests/Edge.swift" Tests/CalcTests/
  hookrun "swift-hook-edge-$v.txt" "${GRADED[@]}"
  "${in[@]}" swift test list 2>/dev/null > "$OUT/swift-hook-edge-list-$v.txt" || true
  rm Tests/CalcTests/Edge.swift
  # A test that crashes the process after the planted failures have printed
  # (src/swift-authored/crash): what the record then holds.
  cp "$DIR/swift-authored/crash/CalcTests/Crash.swift" Tests/CalcTests/
  CORRAL_FAIL_TESTING=1 hookrun "swift-hook-crash-$v.txt" "${GRADED[@]}"
  rm Tests/CalcTests/Crash.swift
  want "swift-hook-crash-exit-$v" 1 "$(exitof "swift-hook-crash-$v.txt")"
  return "$fail"
)

# --- swift coverage (internal/lang/swift_coverage.go) -----------------------
# The llvm-cov export of one coverage run of the coverage fixture
# (internal/lang/testdata/coverage/swift: Calc.swift tested in part, Dead.swift
# compiled and never called, Shape.swift with no executable line,
# Unbuilt/Orphan.swift in no target), made by swiftPlugin.CoverageCmd's own
# script: scripts/swiftcoverageexport prints it (lang.SwiftCoverageExportCmd,
# the same steps ending with the raw export instead of its reduction), and it
# runs here where $SWIFT runs (every word of $SWIFT but the last, then sh;
# its last word is the swift the script is built for), started in a copy of
# the fixture. There is no second copy of those steps to keep equal, and the
# script's own refusals apply: a --show-codecov-path that fails or names no
# file stops it before anything is removed or run. It needs Go on the host.
# The capture directory is written as /fixture in the JSON.
capture_swift_coverage() (
  local pkg pdir words rc=0
  pkg="$DIR/swift-coverage"
  rm -rf "$pkg" && cp -r "$REPO/internal/lang/testdata/coverage/swift" "$pkg"
  pdir="$(cd "$pkg" && pwd -P)"
  read -ra words <<<"$SWIFT"
  (cd "$REPO" && go run ./scripts/swiftcoverageexport "${words[${#words[@]}-1]}" test) > "$DIR/swift-export-cmd" || {
    echo "FAIL swift-coverage-export-cmd: scripts/swiftcoverageexport failed"
    return 1
  }
  local script=()
  mapfile -d '' -t script < "$DIR/swift-export-cmd"
  # shellcheck disable=SC2016 # expanded by that sh, not here
  "${words[@]:0:${#words[@]}-1}" sh -c 'cd "$0" && exec "$@"' "$pkg" "${script[@]}" > "$DIR/swift-export.out" || rc=$?
  want swift-coverage-script-exit 0 "$rc"
  want swift-coverage-run-exit "# suite-exit 0" "$(head -n 1 "$DIR/swift-export.out")"
  want swift-coverage-available "" "$(grep '^corral-swift-coverage: unavailable' "$DIR/swift-export.out" || true)"
  # A capture that failed a check above is not written over the committed one.
  [ "$fail" -eq 0 ] || return "$fail"
  sed -e '1d' -e "s#$pdir/#/fixture/swift-coverage/#g" "$DIR/swift-export.out" > "$OUT/swift-codecov.json"
  want swift-coverage-files 4 "$(grep -o '"filename":"/fixture/swift-coverage/[ST][^"]*"' "$OUT/swift-codecov.json" | wc -l | tr -d ' ')"
  return "$fail"
)

# swift_provenance is PROVENANCE.txt's Swift paragraph, from the version the
# capture ran (toolchain-swift-version.txt). --swift-only leaves the file to
# the operator; a full run writes this.
swift_provenance() {
  # How swift was run: the host's, or a container's image (the word before
  # the final "swift" of a container SWIFT).
  local how="the host's swift" words
  read -ra words <<<"$SWIFT"
  if [ "${#words[@]}" -gt 2 ]; then how="a container of ${words[${#words[@]}-2]} run as the invoking user"; fi
  cat <<PROV
swift-*.txt and toolchain-swift-version.txt (added 2026-11-19; $(head -1 "$OUT/toolchain-swift-version.txt"),
$(sed -n 2p "$OUT/toolchain-swift-version.txt")): scripts/capture-runner-fixtures.sh
through $how, on src/swift, whose test targets are CalcTests (swift-testing, five tests) and
CalcXCTests (XCTest, three, in class AdderTests). swift-list.txt is the stdout
of \`swift test list\`; swift-units.txt the stdout of \`swift package describe
--type json\` under the unit-listing prelude, the capture directory written as
/fixture; every other file is stdout+stderr, not a TTY, with the capture
directory written as \$DIR. swift-fail-testing.txt and swift-fail-xctest.txt
are \`swift test\` with CORRAL_FAIL_TESTING or CORRAL_FAIL_XCTEST set, which
the fixture reads to plant its failures; swift-build-fail.txt is \`swift test\`
after a line that does not compile was appended to Sources/Calc/Calc.swift.
swift-build-target-*.txt are the compile check's \`swift build --target\`:
of a test target, of the source target, of a test target while a sibling
test target did not compile (with a scratch path of its own), and of a test
target after a line that does not compile was appended to the source.
swift-authored-wrapped.txt runs the two files under
src/swift-authored/wrapped, copied into the test targets, and
swift-authored-collision.txt builds CalcTests with
src/swift-authored/collision's file copied into it. swift-shapes-list.txt is
the stdout of \`swift test list\` with src/swift-authored/shapes's file copied
into CalcTests.
swift-single-*.txt, swift-filter-dollar.txt, swift-units-*.txt,
swift-build-target-*.txt and swift-authored-*.txt open with
\`# argv:\` (the operator's command, \`-\` for corral's own compile check)
and \`$ \` (the command corral builds) and end with \`# exit:\` (its exit status). swift-testing runs its tests in
parallel, so the order of its lines differs between captures.
swift-codecov.json is the \`llvm-cov export -format=text -summary-only\`
that swiftPlugin.CoverageCmd's own script (scripts/swiftcoverageexport) made
for one \`swift test\` run of a copy of ../coverage/swift: the profiles the
run wrote, over the products whose build ID they name; the capture directory
written as /fixture.
swift-hook-*-<version>.txt, swift-hook-edge-list-<version>.txt and
swift-xunit-files-<version>.txt (added 2026-11-25): capture_swift_hook in
scripts/capture-runner-fixtures.sh, run once on Swift 6.3.2 (image
swift:6.3.2-noble) and once on Swift 6.4.2 (image corral-swift-642-go-gawk),
each in a container run as the invoking user, on src/swift. Each
swift-hook-*.txt is stdout+stderr of one run, not a TTY, ending with
\`# exit:\` (its exit status), the capture directory written as \$DIR: the
\`swift test\` row's graded command (the record wrapper and
--xunit-output=<token>.xml, taken from the row by scripts/reportcmd) for
pass, fail (CORRAL_FAIL_TESTING and CORRAL_FAIL_XCTEST set), xctest
(CORRAL_FAIL_XCTEST alone), edge (src/swift-authored/edge's file copied
into CalcTests), crash (src/swift-authored/crash's file copied into
CalcTests, CORRAL_FAIL_TESTING set) and fallback (a mktemp that fails put
first on PATH); unhooked and unhooked-pass are plain \`swift test\` with the
fail and pass variables. swift-hook-edge-list is the stdout of \`swift test
list\` with the edge file in place. swift-xunit-files lists the files \`swift
test\` wrote for --xunit-output=<dir>/rec.xml alone, with --parallel, and
given twice (operator.xml then corral.xml), with each run's exit status, and
the exit status of a passing run given --xunit-output=/dev/null.xml.
PROV
}

if [ "$SWIFT_COVERAGE_ONLY" = 1 ]; then
  capture_swift_coverage || fail=1
  exit "$fail"
fi
if [ "$SWIFT_HOOK_ONLY" = 1 ]; then
  capture_swift_hook || fail=1
  exit "$fail"
fi
if [ "$SWIFT_ONLY" = 1 ]; then
  capture_swift || fail=1
  capture_swift_hook || fail=1
  capture_swift_coverage || fail=1
  exit "$fail"
fi
# The runners come from scripts/npm-pins/runner-fixtures/package-lock.json via
# `npm ci`, so every package is pinned by its integrity hash. They are installed
# beside the fixtures, not into them: a lockfile in the copied src/ would be one
# more file the runner tests see. The guard keeps the lockfile equal to the
# variables the manifest reports.
PINS="$REPO/scripts/npm-pins/runner-fixtures"
for pin in "jest $JEST" "vitest $VITEST" "mocha $MOCHA"; do
  set -- $pin
  got=$(node -e 'console.log(require(process.argv[1]).packages["node_modules/"+process.argv[2]].version)' "$PINS/package-lock.json" "$1")
  [ "$got" = "$2" ] || { echo "$1 is $got in $PINS/package-lock.json but $2 here" >&2; exit 1; }
done
TOOLS="$(mktemp -d)"
cp "$PINS/package.json" "$PINS/package-lock.json" "$TOOLS/"
npm ci --prefix "$TOOLS" --silent --no-audit --no-fund >/dev/null
mv "$TOOLS/node_modules" "$DIR/node_modules"
rm -rf "$TOOLS"
[ -f vendor/bin/phpunit ] || composer require -q --dev "phpunit/phpunit:$PHPUNIT"

# --- list commands: stdout only, as the enumerator keeps it ---------------
npx jest --json --testNamePattern '^\b$' calc.jest.test.js 2>/dev/null | node -e \
  'const j=JSON.parse(require("fs").readFileSync(0));process.stdout.write(JSON.stringify({testResults:j.testResults.map(f=>({assertionResults:f.assertionResults.map(a=>({ancestorTitles:a.ancestorTitles,title:a.title,fullName:a.fullName,status:a.status}))}))},null,1))' \
  > "$OUT/jest-list.json"
npx vitest list calc.vitest.test.mjs 2>/dev/null > "$OUT/vitest-list.txt"
npx mocha --dry-run --reporter json calc.mocha.js 2>/dev/null | node -e \
  'const j=JSON.parse(require("fs").readFileSync(0));process.stdout.write(JSON.stringify({tests:j.tests.map(t=>({title:t.title,fullTitle:t.fullTitle}))},null,1))' \
  > "$OUT/mocha-list.json"
node --test --test-reporter=tap calc.node.test.js 2>/dev/null | grep -v duration_ms > "$OUT/node-tap.txt" || true
node --test --test-reporter=tap broken.node.test.js 2>/dev/null | grep -vE "duration_ms|$DIR" > "$OUT/node-tap-loadfail.txt" || true
$RSPEC --dry-run --format json spec/calc_spec.rb 2>/dev/null | ruby -rjson -e \
  'j=JSON.parse(STDIN.read); puts JSON.pretty_generate({"examples"=>j["examples"].map{|e| e.slice("id","description","full_description")}})' \
  > "$OUT/rspec-list.json"
$RSPEC --dry-run --format json spec/join_spec.rb 2>/dev/null | ruby -rjson -e \
  'j=JSON.parse(STDIN.read); puts JSON.pretty_generate({"examples"=>j["examples"].map{|e| e.slice("id","description","full_description")}})' \
  > "$OUT/rspec-join-list.json"
vendor/bin/phpunit --list-tests tests/CalcTest.php > "$OUT/phpunit-list.txt"

# --- unit listings (internal/lang/runner_units.go): ListUnitsCmd's command --
# runs under a prelude printing the working directory, which jest and vitest
# need because they list absolute paths. The directory is written as /fixture:
# these files are public.
PRELUDE='printf '"'"'corral-units-cwd %s\n'"'"' "$(pwd -P)" && exec "$@"'
PDIR="$(pwd -P)"
units() { # units <fixture> <cmd...>: the wrapped listing's stdout
  local f="$1"; shift
  sh -c "$PRELUDE" corral-units "$@" 2>/dev/null | sed -e "s#$PDIR#/fixture#g" -e "s#$HOME#~#g" -e '/duration_ms/d' > "$OUT/$f" || true
}
# unitsjail <fixture> <cmd...>: the same listing with stderr on the SAME stream,
# which is how the bwrap jail returns a run's output (sandbox.Result.Output).
# cargo's progress lines then sit between its JSON messages.
unitsjail() {
  local f="$1"; shift
  sh -c "$PRELUDE" corral-units "$@" 2>&1 | sed -e "s#$PDIR#/fixture#g" -e "s#$HOME#~#g" -e '/duration_ms/d' > "$OUT/$f" || true
}
units jest-units.txt npx --no-install jest --ci units/jest --listTests --json
units vitest-units.txt npx --no-install vitest list --reporter=verbose units/vitest --filesOnly --json
units vitest-units-collide.txt npx --no-install vitest list units/collide --filesOnly --json
units node-units.txt node --test --test-only --test-reporter=tap --test-concurrency 1 units/node/a.node.test.js units/node/b.node.test.js
units node-units-only.txt node --test --test-only --test-reporter=tap 'units/node-only/*.test.js' 'units/node/*.test.js'

# --- one-test commands: each must run exactly the tests named --------------
want jest 1 "$(npx jest --json --testNamePattern '^Calc adds \(a\+b\)$' calc.jest.test.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.numPassedTests+j.numFailedTests)')"
# The -t patterns below are the ones corral builds (vitestName): either joiner,
# since vitest 3 matched titles joined by a space and vitest 5 by " > ".
# vitest 5 writes its JSON report to a file, so the count is read from one.
npx vitest run --reporter=json --outputFile=vitest-one.json -t '^Calc(?: | > )neg(?: | > )adds$' calc.vitest.test.mjs >/dev/null 2>&1 || true
want vitest 1 "$(node -e 'const j=JSON.parse(require("fs").readFileSync("vitest-one.json"));console.log(j.numPassedTests+j.numFailedTests)')"
want mocha-fullname-collision 2 "$(npx mocha --reporter spec --reporter json --grep '^Calc adds$' calc.mocha.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.stats.tests)')"
want mocha 1 "$(npx mocha --reporter spec --reporter json --grep '^Calc neg adds$' calc.mocha.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.stats.tests)')"
want node 1 "$(node --test --test-reporter=tap --test-name-pattern '^Calc neg adds$' calc.node.test.js 2>/dev/null | sed -n 's/^# pass //p')"
want node-leaf-collision 2 "$(node --test --test-reporter=tap --test-name-pattern '^adds$' calc.node.test.js 2>/dev/null | sed -n 's/^# pass //p')"
want rspec 1 "$($RSPEC --format json -E '\ACalc adds \(a\+b\)\z' spec/calc_spec.rb 2>/dev/null | ruby -rjson -e 'puts JSON.parse(STDIN.read)["summary"]["example_count"]')"
want phpunit 1 "$(vendor/bin/phpunit --filter '/(?:^|\\)CalcTest::testAdds(?: with data set .*)?$/' tests/CalcTest.php | sed -n 's/^OK (\([0-9]*\) test.*/\1/p')"
want phpunit-dataset 2 "$(vendor/bin/phpunit --filter '/(?:^|\\)CalcTest::testWith(?: with data set .*)?$/' tests/CalcTest.php | sed -n 's/^OK (\([0-9]*\) test.*/\1/p')"

# One-unit commands (runner_units.go): units/*/a.* holds two tests and b.* one,
# so a run of the unit alone passes 2. The second line of each pair is why the
# row is shaped as it is: jest's --runTestsByPath also runs a file the operator
# named; vitest's file filter is a substring of the path below its root, here
# a.vitest.test.mjs of xa.vitest.test.mjs; mocha adds a configured spec to
# the files on its command line, so mocha has no unit form.
unitran() { grep -oE '(Tests:? +[0-9]+ passed)|(^# pass [0-9]+)|([0-9]+ passing)' | grep -oE '[0-9]+' | head -1; }
want jest-unit 2 "$(npx --no-install jest --ci units/jest --runTestsByPath units/jest/a.jest.test.js '--testPathIgnorePatterns=^(?!.*\/units\/jest\/a\.jest\.test\.js$)' 2>&1 | unitran)"
# Under projects each project has its own rootDir: a pattern anchored at
# <rootDir> matches nothing and, with --passWithNoTests, passes having run no
# test. The row's form names the unit by its path from the working directory.
want jest-rootDir-pattern-under-projects "no test, exit 0" "$(cd units/projects && npx --no-install jest --passWithNoTests '--testPathIgnorePatterns=^(?!<rootDir>\/p1\/a\.jest\.test\.js$)' >/dev/null 2>&1 && echo "no test, exit 0")"
(cd units/projects && npx --no-install jest --passWithNoTests --runTestsByPath p1/a.jest.test.js '--testPathIgnorePatterns=^(?!.*\/p1\/a\.jest\.test\.js$)' 2>&1) | sed -e 's/\x1b\[[0-9;]*m//g' -e "s#$DIR#\$DIR#g" -e '/^Time: \|^Ran all/d' > "$OUT/jest-unit-projects.txt"
want jest-unit-projects 2 "$(unitran < "$OUT/jest-unit-projects.txt")"
# A name filter set inside a bundle of short options filters too, which the
# rows refuse (jestNarrowable, vitestNarrowable): each ran 1 of a's 2 tests.
npx --no-install jest units/jest/a.jest.test.js -ct 'a one' 2>&1 | sed -e 's/\x1b\[[0-9;]*m//g' -e '/^Time: \|^Ran all/d' > "$OUT/jest-bundled-filter.txt"
jestpassed() { sed -n 's/^Tests:.* \([0-9]*\) passed.*/\1/p'; }
want jest-bundled-short-t-filters 1 "$(jestpassed < "$OUT/jest-bundled-filter.txt")"
want jest-it-filters 1 "$(npx --no-install jest units/jest/a.jest.test.js -it 'a one' 2>&1 | jestpassed)"
want vitest-ut-filters 1 "$(npx --no-install vitest run units/vitest/a.vitest.test.mjs -ut 'a one' </dev/null 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
# A shard of one file can be empty, and an empty run passes; UnitCmd drops it.
want node-shard-of-one-file-is-empty "0 exit 0" "$(out=$(node --test --test-reporter=tap --test-shard=2/2 units/node/a.node.test.js 2>&1) && echo "$(printf '%s' "$out" | unitran) exit 0")"
want jest-runTestsByPath-adds-named-file 3 "$(npx --no-install jest units/jest/b.jest.test.js --runTestsByPath units/jest/a.jest.test.js 2>&1 | unitran)"
want vitest-unit 2 "$(npx --no-install vitest run --reporter=verbose units/vitest/a.vitest.test.mjs 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
want vitest-unit-substring-collision 3 "$(npx --no-install vitest run --root units/collide units/collide/a.vitest.test.mjs 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | unitran)"
want node-unit 2 "$(node --test --test-reporter=tap --test-concurrency 1 units/node/a.node.test.js 2>&1 | unitran)"
want mocha-config-spec-merges 2 "$(cd units/mocharc && npx --no-install mocha t/a.mocha.js 2>&1 | unitran)"

# --- Go, Ruby, PHP and Rust units (runner_units.go's other rows) -----------
# Each listing is ListUnitsCmd's command for the operator command named in
# the comment above it; each want is a one-unit run, or a measurement behind
# the row's shape. a.* / package a / units-a's lib hold two tests, the rest one.
help() { # help <fixture> <cmd...>: a runner's --help, the tables in runner_unit_options.go
  local f="$1"; shift
  "$@" 2>&1 | sed -e "s#$DIR#\$DIR#g" -e 's#/tmp/go-build[0-9]*/b[0-9]*/exe/#$GOBUILD/#g' > "$OUT/$f" || true
}
GOTESTSUM=1.13.0
[ "$GOTESTSUM" = "1.13.0" ] || { echo "GOTESTSUM drifted from the install line" >&2; exit 1; }
GOBIN="$DIR/bin" go install gotest.tools/gotestsum@v1.13.0
# goUnitTemplate (go_units.go)
GOT='{{if .Error}}error {{.ImportPath}}{{else if or .TestGoFiles .XTestGoFiles}}unit {{.ImportPath}}{{else}}none {{.ImportPath}}{{end}}'
# go test -count=1 ./...  /  go test -tags extra ./...  /  go test ./nope
(cd units/gomod && units go-units.txt go list -e -f "$GOT" ./...)
(cd units/gomod && units go-units-tags.txt go list -tags extra -e -f "$GOT" ./...)
(cd units/gomod && units go-units-error.txt go list -e -f "$GOT" ./nope)
gopass() { grep -c -- '--- PASS' || true; }
want go-unit 2 "$(cd units/gomod && go test -v -count=1 example.com/units/a 2>&1 | gopass)"
want go-unit-xtest 1 "$(cd units/gomod && go test -v -count=1 example.com/units/b 2>&1 | gopass)"
want gotestsum-no-args-runs "go test -json ./..." "$(cd units/gomod && "$DIR/bin/gotestsum" --debug 2>&1 | sed -n 's/^exec: \[\(.*\)\]$/\1/p')"
want gotestsum-unit 2 "$(cd units/gomod && "$DIR/bin/gotestsum" --format standard-verbose -- -count=1 example.com/units/a 2>&1 | gopass)"
help gotestsum-help.txt "$DIR/bin/gotestsum" --help

# rspec units/rspec
units rspec-units.txt $RSPEC units/rspec --dry-run --format json
rbran() { grep -oE '[0-9]+ examples?' | grep -oE '[0-9]+' | tail -1; }
want rspec-unit 2 "$($RSPEC units/rspec/spec/a_spec.rb 2>&1 | rbran)"
want rspec-attached-e-filters 1 "$($RSPEC -eone units/rspec/spec/a_spec.rb 2>&1 | rbran)"
want rspec-paths-union 3 "$($RSPEC units/rspec units/rspec/spec/a_spec.rb 2>&1 | rbran)"
want rspec-dotrspec-path-not-added 2 "$(cd units/rspecrc && $RSPEC spec/a_spec.rb 2>&1 | rbran)"
want rspec-profile-reads-next-word 3 "$(cd units/rspec && $RSPEC -p spec/a_spec.rb 2>&1 | rbran)"
want rspec-no-profile-reads-no-word 2 "$(cd units/rspec && $RSPEC --no-profile spec/a_spec.rb 2>&1 | rbran)"
help rspec-help.txt $RSPEC --help

# vendor/bin/phpunit -c units/phpunit/phpunit.xml --testsuite unit  /  ... --group slow
phpran() { sed -n 's/^OK (\([0-9]*\) tests\{0,1\}.*/\1/p'; }
PHPC="vendor/bin/phpunit -c units/phpunit/phpunit.xml"
# PHPUnit's listing runs under the row's own setup (phpunitUnitsSetup, which
# has PHP name each test class's file), so its script comes from the row and
# is not copied here.
PHPPRELUDE="$(cd "$REPO" && go run ./scripts/listunitscmd php vendor/bin/phpunit)"
phpunits() { # phpunits <fixture> <cmd...>: units, under the PHPUnit row's listing script
  local f="$1"; shift
  sh -c "$PHPPRELUDE" corral-units "$@" 2>/dev/null | sed -e "s#$PDIR#/fixture#g" -e "s#$HOME#~#g" > "$OUT/$f" || true
}
phpunits phpunit-units.txt $PHPC --testsuite unit --list-tests
phpunits phpunit-units-group.txt $PHPC --group slow --list-tests
# The same listing on the other PHPUnit lines, each installed beside the
# fixtures. 9 and 10 have no --list-test-files, which is why the row lists
# tests and not files; under a group option both say they ignore it, and
# the row reads that as no listing (phpunit9-units-group.txt).
for line in "9 $PHPUNIT9" "10 $PHPUNIT10" "12 $PHPUNIT12"; do
  set -- $line
  [ -f "pv/$1/vendor/bin/phpunit" ] || { mkdir -p "pv/$1" && composer require -q --working-dir "pv/$1" --dev "phpunit/phpunit:$2"; }
  phpunits "phpunit$1-units.txt" "pv/$1/vendor/bin/phpunit" -c units/phpunit/phpunit.xml --testsuite unit --list-tests
  want "phpunit$1-unit" 2 "$("pv/$1/vendor/bin/phpunit" -c units/phpunit/phpunit.xml --testsuite unit units/phpunit/tests/ATest.php 2>&1 | phpran)"
done
phpunits phpunit9-units-group.txt pv/9/vendor/bin/phpunit -c units/phpunit/phpunit.xml --group slow --list-tests
want phpunit-unit 2 "$($PHPC --testsuite unit units/phpunit/tests/ATest.php 2>&1 | phpran)"
want phpunit-args-union 5 "$($PHPC units/phpunit/tests units/phpunit/tests/ATest.php 2>&1 | phpran)"
want phpunit-file-replaces-testsuite 2 "$($PHPC --testsuite other units/phpunit/tests/ATest.php 2>&1 | phpran)"
want phpunit-option-after-file 1 "$($PHPC units/phpunit/tests/ATest.php --group slow 2>&1 | phpran)"
want phpunit-exclude-group-lists-no-empty-file 0 "$($PHPC --exclude-group slow --list-tests units/phpunit/excl units/phpunit/tests 2>&1 | grep -c ' - DTest' || true)"
want phpunit-colors-takes-no-next-word 'Test file "never" not found' "$($PHPC --colors never units/phpunit/tests/ATest.php 2>&1 | grep -o 'Test file "never" not found')"
help phpunit-help.txt vendor/bin/phpunit --help

# cargo test -p units-a  /  cargo test --workspace  /  cargo nextest run --workspace [integrates]
cargoran() { grep -oE 'test result: ok\. [0-9]+ passed' | grep -oE '[0-9]+' | awk '{s+=$1} END {print s+0}'; }
nextran() { sed -n 's/^ *Summary .* \([0-9]*\) tests\{0,1\} run:.*/\1/p'; }
(cd units/cargo && units cargo-units.txt cargo test -p units-a --no-run --message-format=json)
(cd units/cargo && units cargo-units-workspace.txt cargo test --workspace --no-run --message-format=json)
(cd units/cargo && cargo clean -q && unitsjail cargo-units-jail.txt cargo test -p units-a --no-run --message-format=json)
(cd units/cargo && units nextest-units.txt cargo nextest list --workspace --message-format json)
(cd units/cargo && units nextest-units-filter.txt cargo nextest list --workspace integrates --message-format json)
want cargo-unit-lib 2 "$(cd units/cargo && cargo test -p units-a --lib 2>&1 | cargoran)"
want cargo-unit-bin 1 "$(cd units/cargo && cargo test -p units-a --bin units-a 2>&1 | cargoran)"
want cargo-unit-test 1 "$(cd units/cargo && cargo test -p units-a --test it 2>&1 | cargoran)"
want cargo-targets-union 3 "$(cd units/cargo && cargo test -p units-a --test it --lib 2>&1 | cargoran)"
want cargo-target-name-across-packages 2 "$(cd units/cargo && cargo test --workspace --test it 2>&1 | cargoran)"
# -p adds to --workspace rather than narrowing it, so a unit run replaces the
# operator's package selection with -p <its package>; each runs only its own.
want cargo-workspace-plus-p-adds 3 "$(cd units/cargo && cargo test --workspace -p units-a --lib 2>&1 | cargoran)"
want cargo-unit-b-lib 1 "$(cd units/cargo && cargo test -p units-b --lib 2>&1 | cargoran)"
want cargo-unit-b-test 1 "$(cd units/cargo && cargo test -p units-b --test it 2>&1 | cargoran)"
want nextest-unit 2 "$(cd units/cargo && cargo nextest run --workspace -E 'binary_id(=units-a)' 2>&1 | nextran)"
want nextest-filtersets-union 3 "$(cd units/cargo && cargo nextest run --workspace -E 'binary_id(=units-a)' -E 'test(negates)' 2>&1 | nextran)"
want nextest-name-filter-and-binary 0 "$(cd units/cargo && cargo nextest run --workspace integrates -E 'binary_id(=units-a)' 2>&1 | nextran)"
# KillerCmd: nextest names a failure `<binary id> <test>`, and its killer selects
# that test of that binary alone; cargo test's names no binary, so a test of the
# same path in another binary runs too (integrates is in units-a's and units-b's).
want nextest-killer 1 "$(cd units/cargo && cargo nextest run --workspace -E 'binary_id(=units-a::it) & test(=integrates)' 2>&1 | nextran)"
want cargo-killer-same-path 2 "$(cd units/cargo && cargo test --workspace -- --exact integrates 2>&1 | cargoran)"
want cargo-killer 1 "$(cd units/cargo && cargo test --workspace -- --exact tests::adds 2>&1 | cargoran)"
# Salvage from a unit command (DeselectCmd on UnitsCmd's output): the unit's
# tests less the failing one. nextest folds the unit's filterset into the
# exclusion, since it unions filtersets; cargo's --exact keeps tests::adds_more,
# which a substring --skip of tests::adds would drop.
# TestRustDeselectCapturesAreTheRowsCommands holds these commands equal to the rows'.
want nextest-deselect-from-unit 1 "$(cd units/cargo && cargo nextest run --workspace -E '((binary_id(=units-a))) & not ((binary_id(=units-a) & test(=tests::adds)))' 2>&1 | nextran)"
want cargo-deselect-from-unit 1 "$(cd units/cargo && cargo test -p units-a --lib -- --exact --skip tests::adds 2>&1 | cargoran)"
# An -E written attached is a filterset too, ORed with the unit's.
want nextest-attached-filterset-ors 2 "$(cd units/cargo && cargo nextest run --workspace '-Etest(negates)' -E 'binary_id(=units-a::it)' 2>&1 | nextran)"
# Every run-only option (nextestRunOnly, runner_unit_options.go) is refused by list.
refused=0
for o in "-R x" "--rerun x" "--no-run" "-j 1" "--test-threads 1" "--retries 1" "--flaky-result pass" "--fail-fast" \
  "--no-fail-fast" "--max-fail 1" "--debugger gdb" "--tracer strace" "--no-tests pass" "--no-capture" "--stress-count 1" \
  "--stress-duration 1s" "--failure-output final" "--success-output never" "--status-level all" "--final-status-level all" \
  "--no-output-indent" "--hide-progress-bar" "--no-input-handler" "--max-progress-running 1" "--message-format libtest-json" \
  "--message-format-version 0.1"; do
  # shellcheck disable=SC2086 # each option and its value are two words
  out="$(cd units/cargo && cargo nextest list --workspace $o 2>&1 || true)"
  case "$out" in *"unexpected argument"* | *"invalid value"*) refused=$((refused + 1)) ;; esac
done
want nextest-list-refuses-every-run-only 26 "$refused"
help cargo-test-help.txt cargo test --help
help libtest-help.txt sh -c 'cd units/cargo && cargo test -q -p units-a --test it -- --help 2>/dev/null'
help nextest-run-help.txt cargo nextest run --help
help nextest-list-help.txt cargo nextest list --help

# --- multi-unit commands (UnitsCmd, runner_units_multi_test.go) ------------
# Each row's command for units a and b of three: units/multi/* holds a with one
# test, b two, c four, so a run's count names the set it ran (3 = a and b);
# Go reuses units/gomod under -tags extra (a two, b one, tagged one), Rust the
# cargo workspace. Each fixture is `# argv:` the operator's command, `$ ` the
# row's command for the units (keep it equal to what the row builds; the
# test compares them), then the run's stdout+stderr.
# The agent variables are unset because jest 30 prints only its summary, not
# the tests it ran, when it sees one (measured under Claude Code: AI_AGENT).
multi() { # multi <fixture> <operator argv> <cmd...>
  local f="$1" argv="$2"; shift 2
  { echo "# argv: $argv"; echo "\$ $*"; env -u AI_AGENT -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT "$@" 2>&1; } |
    sed -e 's/\x1b\[[0-9;]*m//g' -e "s#$PDIR#/fixture#g" -e "s#$DIR#/fixture#g" -e "s#$HOME#~#g" \
    -e '/duration_ms\|^Time: \|^Ran all\|Duration \|Start at \|^Finished in\|Finished `test` profile\|Running \|Compiling \|Starting \|Nextest run ID\|^Randomized with seed/d' \
    -e 's/ ([0-9.]* \?m\?s)$//' -e 's/ [0-9]*ms$//' -e 's/\t[0-9.]*s$//' -e 's/ in [0-9.]*s$//' -e 's/ \[ *[0-9.]*s\]/ [time]/' > "$OUT/$f" || true
}
multiran() { sed -e '1,2d' "$OUT/$1" | grep -oE '(Tests:? +[0-9]+ passed)|(^# pass [0-9]+)|([0-9]+ examples?, 0 failures)|(OK \([0-9]+ tests?)|(--- PASS)|(test result: ok\. [0-9]+ passed)|( [0-9]+ tests? run: [0-9]+ passed)' |
  awk '/--- PASS/ {n++; next} {match($0, /[0-9]+/); n += substr($0, RSTART, RLENGTH)} END {print n+0}'; }
(cd units/gomod && multi go-units-multi.txt "go test -v -count=1 -tags extra ./..." \
  go test -v -count=1 -tags extra example.com/units/a example.com/units/b)
want go-units-multi 3 "$(multiran go-units-multi.txt)"
(cd units/gomod && multi gotestsum-units-multi.txt "$DIR/bin/gotestsum --format standard-verbose -- -count=1 -tags extra ./..." \
  "$DIR/bin/gotestsum" --format standard-verbose -- -count=1 -tags extra example.com/units/a example.com/units/b)
want gotestsum-units-multi 3 "$(multiran gotestsum-units-multi.txt)"
multi jest-units-multi.txt "npx --no-install jest --ci --verbose units/multi/jest" \
  npx --no-install jest --ci --verbose units/multi/jest --runTestsByPath units/multi/jest/a.jest.test.js units/multi/jest/b.jest.test.js \
  '--testPathIgnorePatterns=^(?!.*(?:\/units\/multi\/jest\/a\.jest\.test\.js|\/units\/multi\/jest\/b\.jest\.test\.js)$)'
want jest-units-multi 3 "$(multiran jest-units-multi.txt)"
multi vitest-units-multi.txt "npx --no-install vitest run --reporter=verbose units/multi/vitest" \
  npx --no-install vitest run --reporter=verbose units/multi/vitest/a.vitest.test.mjs units/multi/vitest/b.vitest.test.mjs
want vitest-units-multi 3 "$(multiran vitest-units-multi.txt)"
multi node-units-multi.txt "node --test --test-reporter=tap --test-concurrency 1 units/multi/node/*.node.test.js" \
  node --test --test-reporter=tap --test-concurrency 1 units/multi/node/a.node.test.js units/multi/node/b.node.test.js
want node-units-multi 3 "$(multiran node-units-multi.txt)"
# shellcheck disable=SC2086 # RSPEC may be two words (ruby -S rspec)
multi rspec-units-multi.txt "$RSPEC --format documentation units/multi/rspec" \
  $RSPEC --format documentation units/multi/rspec/spec/a_spec.rb units/multi/rspec/spec/b_spec.rb
want rspec-units-multi 3 "$(multiran rspec-units-multi.txt)"
multi phpunit-units-multi.txt "vendor/bin/phpunit --testdox units/multi/phpunit/tests" \
  vendor/bin/phpunit --testdox units/multi/phpunit/tests/MultiATest.php units/multi/phpunit/tests/MultiBTest.php
want phpunit-units-multi 3 "$(multiran phpunit-units-multi.txt)"
(cd units/cargo && multi cargo-units-multi.txt "cargo test --workspace" cargo test -p units-a -p units-b --lib)
want cargo-units-multi 3 "$(multiran cargo-units-multi.txt)"
# cargo selects targets across the packages it is given: units-a/lib with
# units-b/test:it also runs units-a's it and units-b's lib (cargoUnitsCoruns).
(cd units/cargo && multi cargo-units-multi-coruns.txt "cargo test --workspace" cargo test -p units-a -p units-b --lib --test it)
want cargo-units-multi-coruns 5 "$(multiran cargo-units-multi-coruns.txt)"
(cd units/cargo && multi nextest-units-multi.txt "cargo nextest run --workspace" \
  cargo nextest run --workspace -E 'binary_id(=units-a)' -E 'binary_id(=units-b::it)')
want nextest-units-multi 3 "$(multiran nextest-units-multi.txt)"

# --- failing suites: what each DEFAULT reporter prints, and the skip commands --
# (internal/lang/runner_failures.go). $DIR and $HOME are replaced: these files are public.
fails() { # fails <fixture> <cmd...>: stdout+stderr, ANSI stripped, agent variables unset (see units_multi)
  local f="$1"; shift
  env -u AI_AGENT -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT "$@" 2>&1 | sed -e 's/\x1b\[[0-9;]*m//g' -e "s#$DIR#\$DIR#g" -e "s#$HOME#~#g" -e '/duration_ms\|^Finished in\|^Time: \|Duration \|Start at \|^Run options: --seed\|runs\/s\|Nextest run ID /d' > "$OUT/$f" || true
}
fails jest-fail.txt npx jest fail/f.jest.test.js
fails jest-suitefail.txt npx jest fail/broken.jest.test.js
fails vitest-fail.txt npx vitest run fail/f.vitest.test.mjs
fails vitest-suitefail.txt npx vitest run fail/broken.vitest.test.mjs
fails mocha-fail.txt npx mocha fail/f.mocha.js
fails mocha-hookfail.txt npx mocha fail/hook.mocha.js
fails node-fail.txt node --test fail/f.node.test.js
# node 23+ prints the spec reporter off a TTY too (measured on node 24.22); node
# 22's --test-reporter=spec prints the same tree.
fails node-spec-fail.txt node --test --test-reporter=spec fail/f.node.test.js
fails node-spec-loadfail.txt node --test --test-reporter=spec broken.node.test.js
fails rspec-fail.txt $RSPEC fail/spec/f_spec.rb
fails rspec-loadfail.txt $RSPEC fail/spec/broken_spec.rb
fails minitest-fail.txt ruby fail/f_test.rb
fails phpunit-fail.txt vendor/bin/phpunit fail/tests/FTest.php
# Data sets whose printed argument suffix is not their position, with parentheses
# and quotes in a name and in an argument: phpFailures drops the suffix.
fails phpunit-datasets-fail.txt vendor/bin/phpunit fail/datasets/DTest.php
# The PHPUnit row's failure hook (internal/lang/phpunit_record.go): the command a
# graded run runs, `--log-junit` and the record wrapper included, is taken from
# the row (scripts/reportcmd), never copied here; TestPHPUnitHookCaptureIsTheRowsCommand
# holds these lines to it. The second run's phpunit.xml sets testdox="true", which
# replaces the printed `N)` lines; the record still names every failure.
phphook() { # phphook <array name> <phpunit command...>: the row's graded command, NUL-separated
  local name="$1"; shift
  mapfile -d '' "$name" < <(cd "$REPO" && go run ./scripts/reportcmd php "$@")
  local -n words="$name"
  [ "${#words[@]}" -gt 0 ] || { echo "scripts/reportcmd gave no command for $*" >&2; exit 1; }
}
phphook PHPHOOK vendor/bin/phpunit fail/tests/FTest.php fail/datasets/DTest.php fail/ns/NTest.php
fails phpunit-hook-fail.txt "${PHPHOOK[@]}"
# The hook keeps PHPUnit's exit code: 2 (an error) with it and without it.
want phpunit-hook-exit "2 2" "$(vendor/bin/phpunit fail/tests/FTest.php fail/datasets/DTest.php fail/ns/NTest.php >/dev/null 2>&1; echo -n "$? "; "${PHPHOOK[@]}" >/dev/null 2>&1; echo $?)"
phphook PHPHOOKTESTDOX vendor/bin/phpunit -c report/phpunit/phpunit.xml fail/tests/FTest.php
fails report-phpunit-testdox-hooked.txt "${PHPHOOKTESTDOX[@]}"
# The same files unhooked: the hooked capture's printed part must equal it
# (TestPHPUnitHookLeavesThePrintedOutputAlone).
fails phpunit-hook-unhooked.txt vendor/bin/phpunit fail/tests/FTest.php fail/datasets/DTest.php fail/ns/NTest.php
# What the record leaves out (fail/edge): a setUpBeforeClass that throws and a
# data provider that throws are printed as numbered errors and are not in the
# JUnit record; a risky test is printed in its own numbered section and is a
# plain testcase in the record. FTest gives the record failures of its own.
phphook PHPHOOKEDGE vendor/bin/phpunit fail/tests/FTest.php fail/edge/SetupTest.php fail/edge/ProviderTest.php fail/edge/RiskyTest.php
fails phpunit-hook-edge.txt "${PHPHOOKEDGE[@]}"
# What the disclosure says the hook costs: PHPUnit writes one JUnit file, so an
# operator's own --log-junit, and a phpunit.xml <junit> entry, are written
# without the hook and not with it.
junit_written() { # junit_written <file> <cmd...>: present|absent after running cmd
  local f="$1"; shift
  rm -f "$f"; "$@" >/dev/null 2>&1 || true
  if [ -e "$f" ]; then echo present; else echo absent; fi
}
phphook PHPHOOKOPLOG vendor/bin/phpunit --log-junit operator-junit.xml fail/tests/FTest.php
want phpunit-operator-junit-unhooked present "$(junit_written operator-junit.xml vendor/bin/phpunit --log-junit operator-junit.xml fail/tests/FTest.php)"
want phpunit-operator-junit-hooked absent "$(junit_written operator-junit.xml "${PHPHOOKOPLOG[@]}")"
phphook PHPHOOKCFGLOG vendor/bin/phpunit -c report/phpunit-junit/phpunit.xml fail/tests/FTest.php
want phpunit-config-junit-unhooked present "$(junit_written report/phpunit-junit/project-junit.xml vendor/bin/phpunit -c report/phpunit-junit/phpunit.xml fail/tests/FTest.php)"
want phpunit-config-junit-hooked absent "$(junit_written report/phpunit-junit/project-junit.xml "${PHPHOOKCFGLOG[@]}")"
(cd fail/cargo && cargo build --tests -q 2>/dev/null; fails cargo-test-fail.txt cargo test)
(cd fail/cargo && fails cargo-nextest-fail.txt cargo nextest run)

# --- report flags: a project config that hides the lines the readers take, run
# as the project runs it (-hidden) and with the runner row's ReportArgs appended
# (-shown). The flags here must be the row's (internal/lang/runner.go);
# TestReportFlagCapturesUseTheRowsFlags reads this file to hold them equal.
(cd report/jest && fails report-jest-hidden.txt npx jest f.jest.test.js)
(cd report/jest && fails report-jest-shown.txt npx jest f.jest.test.js --reporters=default)
(cd report/vitest && fails report-vitest-hidden.txt npx vitest run f.vitest.test.mjs)
(cd report/vitest && fails report-vitest-shown.txt npx vitest run f.vitest.test.mjs --reporter=default)
(cd report/mocha && fails report-mocha-hidden.txt npx mocha f.mocha.js)
(cd report/mocha && fails report-mocha-shown.txt npx mocha f.mocha.js --reporter spec)
(cd report/rspec && fails report-rspec-hidden.txt $RSPEC spec/f_spec.rb)
(cd report/rspec && fails report-rspec-shown.txt $RSPEC spec/f_spec.rb --format progress)
(cd report/nextest && cargo build --tests -q 2>/dev/null; fails report-cargo-nextest-hidden.txt cargo nextest run)
(cd report/nextest && fails report-cargo-nextest-shown.txt cargo nextest run --status-level pass --final-status-level flaky)

# --- fail-fast: nextest's row adds --fail-fast to mutant runs, placed before
# the runner's own `--` (FailFastCmd). The fixture's profile turns fail-fast
# off and holds two failing tests before a passing one; -j 1 makes the count
# exact. The second command is FailFastCmd of `cargo nextest run -j 1 --
# tests::` and the third is the flag appended after the `--` instead;
# TestFailFastCaptureIsTheRowsCommand reads this file to hold them equal.
nextran_of() { grep -oE '[0-9]+(/[0-9]+)? tests? run' | head -1 | grep -oE '^[0-9]+'; }
(cd fail/cargo && cargo build --tests -q 2>/dev/null)
want nextest-profile-no-fail-fast 3 "$(cd fail/cargo && cargo nextest run -j 1 -- tests:: 2>&1 | nextran_of)"
want nextest-fail-fast-before-separator 1 "$(cd fail/cargo && cargo nextest run -j 1 --fail-fast -- tests:: 2>&1 | nextran_of)"
want nextest-fail-fast-after-separator-refused 96 "$(cd fail/cargo && cargo nextest run -j 1 -- tests:: --fail-fast >/dev/null 2>&1; echo $?)"

# Each killer command (KillerCmd: the failing run's own command, narrowed to the
# test its failure reader named, no file added) runs that test and nothing else.
killed() { grep -oE '([0-9]+ examples?, [0-9]+ failures?)|(Tests: [0-9]+, Assertions: [0-9]+, (Failures|Errors): [0-9]+)' | head -1; }
want rspec-killer "1 example, 1 failure" "$($RSPEC fail/spec/f_spec.rb -E '\ACalc adds \(a\+b\)\z' 2>&1 | killed)"
want rspec-killer-nested "1 example, 1 failure" "$($RSPEC fail/spec/f_spec.rb -E '\AMyApp::Calc top level\z' 2>&1 | killed)"
want phpunit-killer "Tests: 1, Assertions: 1, Failures: 1" "$(vendor/bin/phpunit fail/tests/FTest.php --filter '/(?:^|\\)FTest::testAdds(?: with data set .*)?$/' 2>&1 | killed)"
want phpunit-killer-dataset "Tests: 2, Assertions: 2, Failures: 1" "$(vendor/bin/phpunit fail/tests/FTest.php --filter '/(?:^|\\)FTest::testData(?: with data set .*)?$/' 2>&1 | killed)"
# A failing data set's id (phpFailures, the argument suffix dropped) runs that
# data set alone. With the suffix kept, the filter selected nothing (PHPUnit
# 11.5.57: "No tests executed!").
want phpunit-killer-one-dataset "Tests: 1, Assertions: 1, Failures: 1" "$(vendor/bin/phpunit fail/tests/FTest.php --filter '/(?:^|\\)FTest::testData with data set "two"(?: with data set .*)?$/' 2>&1 | killed)"
want phpunit-killer-one-dataset-parens "Tests: 1, Assertions: 1, Failures: 1" "$(vendor/bin/phpunit fail/datasets/DTest.php --filter '/(?:^|\\)DTest::testNamed with data set "x \(y\)"(?: with data set .*)?$/' 2>&1 | killed)"
want phpunit-killer-one-dataset-unnamed "Tests: 1, Assertions: 1, Failures: 1" "$(vendor/bin/phpunit fail/datasets/DTest.php --filter '/(?:^|\\)DTest::testUnnamed with data set #2(?: with data set .*)?$/' 2>&1 | killed)"

# Each skip command names the three failing tests; exactly the one passing test
# must run, and pass.
passed() { grep -oE '([0-9]+ (passed|passing|examples?|runs,))|# pass [0-9]+|OK \([0-9]+ test' | grep -oE '[0-9]+' | head -1; }
skipok() { # skipok <name> <cmd...>
  local name="$1" out rc; shift
  out="$("$@" 2>&1)" && rc=0 || rc=$?
  want "$name-skip" "1 exit 0" "$(printf '%s' "$out" | sed 's/\x1b\[[0-9;]*m//g' | passed) exit $rc"
}
skipok jest npx jest fail/f.jest.test.js --testNamePattern '^(?!(?:Calc adds \(a\+b\)|Calc neg adds|top level)$)'
skipok vitest npx vitest run fail/f.vitest.test.mjs -t '^(?!(?:Calc(?: | > )adds \(a\+b\)|Calc(?: | > )neg(?: | > )adds|top level)$)'
skipok mocha npx mocha fail/f.mocha.js --grep '^(?:Calc adds \(a\+b\)|Calc neg adds|top level)$' --invert
skipok node node --test --test-skip-pattern '^(?:Calc adds \(a\+b\)|Calc neg adds|top level)$' fail/f.node.test.js
# node reads options only before the first file (runner_node.go nodeWith): after it, ignored.
want node-option-after-file-ignored 4 "$(node --test fail/f.node.test.js --test-skip-pattern '^(?:Calc adds \(a\+b\)|Calc neg adds|top level)$' 2>/dev/null | sed -n 's/^# tests //p')"
skipok rspec $RSPEC fail/spec/f_spec.rb -E '\A(?!(?:Calc adds \(a\+b\)|Calc neg adds|MyApp::Calc top level)\z)'
skipok minitest ruby fail/f_test.rb --exclude '/\A(?:CalcTest#test_adds|CalcTest#test_boom|MyApp::NegTest#test_x)\z/'
skipok phpunit vendor/bin/phpunit fail/tests/FTest.php --exclude-filter '/(?:^|\\)(?:FTest::testBoom|FTest::testAdds|FTest::testData)(?: with data set .*)?$/'
skipok npm-test npm test -- fail/f.jest.test.js --testNamePattern '^(?!(?:Calc adds \(a\+b\)|Calc neg adds|top level)$)'
skipok composer-test composer test -- fail/tests/FTest.php --exclude-filter '/(?:^|\\)(?:FTest::testBoom|FTest::testAdds|FTest::testData)(?: with data set .*)?$/'
# fail/cargo holds two failing tests and one passing: the Rust deselect
# commands (DeselectCmd) name the two; TestRustDeselectCapturesAreTheRowsCommands
# holds these lines equal to what the rows build from the captured failures.
skipok cargo-test env -C fail/cargo cargo test -- --exact --skip tests::a_fails --skip tests::b_fails
skipok cargo-nextest env -C fail/cargo cargo nextest run -E 'not ((binary_id(=fail-cargo) & test(=tests::a_fails)) | (binary_id(=fail-cargo) & test(=tests::b_fails)))'

# --- inline landing (Rust): corral's authored test appended to the source
# itself as `#[cfg(test)] mod corral_tests { #[allow(unused_imports)]
# use super::*; … }` (LandTest at
# depth 0, adequacy.AppendBlock). inline/cargo is one crate: src/lib.rs (the
# crate root), src/extra.rs (a module, whose ids carry `extra::`) and
# src/named.rs (already declaring corral_tests, so its block is
# corral_tests_2). inline/composed holds each source as a grading run writes
# it: the original, a mutant (inline/mutants) or, for the positive control,
# the reach canary in place of the block, with the block appended.
# TestRustInlineCapturesAreWhatCorralComposes holds those files equal to what
# corral composes from inline/authored, the select lines equal to what the
# rows' SelectCmd builds from InlineTestIDs, and the failing ids captured here
# equal to the ids InlineTestIDs gives (a salvage drops failing tests by them).
inline() { # inline <case> (<src file> <file under inline/>)...: inline/cargo copied to inline/<case>, each file at src/<src file>
  local d="inline/$1"; shift
  cp -r inline/cargo "$d"
  while [ $# -gt 0 ]; do cp "inline/$2" "$d/src/$1"; shift 2; done
}
pf() { # "<passed> passed <failed> failed": libtest's result lines summed, or nextest's summary
  sed 's/\x1b\[[0-9;]*m//g' | awk '/^test result: |^ *Summary / {
    if (match($0, /[0-9]+ passed/)) p += substr($0, RSTART, RLENGTH) + 0
    if (match($0, /[0-9]+ failed/)) f += substr($0, RSTART, RLENGTH) + 0
  } END { print p + 0 " passed " f + 0 " failed" }'
}
inline lib-pass lib.rs composed/lib.rs
inline lib-kill lib.rs composed/lib.mutant.rs
inline lib-canary lib.rs composed/lib.canary.rs
inline lib-dev lib.rs mutants/lib.rs
inline extra-pass extra.rs composed/extra.rs
inline extra-kill extra.rs composed/extra.mutant.rs
inline extra-canary extra.rs composed/extra.canary.rs
inline extra-dev extra.rs mutants/extra.rs
inline named-pass named.rs composed/named.rs
inline deny-lib-canary lib.rs composed/deny.canary.rs
inline deny-extra-canary lib.rs deny/lib.rs extra.rs composed/extra.canary.rs
# The mutant survives the file's own tests: only the landed block can kill it.
want inline-cargo-test-dev-misses-lib "5 passed 0 failed" "$(cd inline/lib-dev && cargo test 2>&1 | pf)"
want inline-cargo-test-dev-misses-extra "5 passed 0 failed" "$(cd inline/extra-dev && cargo test 2>&1 | pf)"
want inline-nextest-dev-misses-lib "5 passed 0 failed" "$(cd inline/lib-dev && cargo nextest run 2>&1 | pf)"
want inline-nextest-dev-misses-extra "5 passed 0 failed" "$(cd inline/extra-dev && cargo nextest run 2>&1 | pf)"
# The composition builds and passes on the original, under the operator's
# own command; named.rs's block composes as corral_tests_2 beside its own.
want inline-cargo-test-passes-lib "7 passed 0 failed" "$(cd inline/lib-pass && cargo test 2>&1 | pf)"
want inline-nextest-passes-lib "7 passed 0 failed" "$(cd inline/lib-pass && cargo nextest run 2>&1 | pf)"
want inline-cargo-test-passes-extra "7 passed 0 failed" "$(cd inline/extra-pass && cargo test 2>&1 | pf)"
want inline-nextest-passes-extra "7 passed 0 failed" "$(cd inline/extra-pass && cargo nextest run 2>&1 | pf)"
want inline-cargo-test-passes-named "6 passed 0 failed" "$(cd inline/named-pass && cargo test 2>&1 | pf)"
want inline-nextest-passes-named "6 passed 0 failed" "$(cd inline/named-pass && cargo nextest run 2>&1 | pf)"
# With the mutant in place the block's test fails: it kills. The failing runs
# are kept (nextest without fail-fast, so every test reports) for their ids.
(cd inline/lib-kill && fails inline-cargo-test-kill-lib.txt cargo test)
(cd inline/lib-kill && fails inline-nextest-kill-lib.txt cargo nextest run --no-fail-fast)
(cd inline/extra-kill && fails inline-cargo-test-kill-extra.txt cargo test)
(cd inline/extra-kill && fails inline-nextest-kill-extra.txt cargo nextest run --no-fail-fast)
want inline-cargo-test-kills-lib "6 passed 1 failed" "$(pf < "$OUT/inline-cargo-test-kill-lib.txt")"
want inline-nextest-kills-lib "6 passed 1 failed" "$(pf < "$OUT/inline-nextest-kill-lib.txt")"
want inline-cargo-test-kills-extra "6 passed 1 failed" "$(pf < "$OUT/inline-cargo-test-kill-extra.txt")"
want inline-nextest-kills-extra "6 passed 1 failed" "$(pf < "$OUT/inline-nextest-kill-extra.txt")"
# SelectCmd: the unit command narrowed to the block's ids and the reach
# canary's, by exact name, runs the block's tests and nothing else (not the
# file's own, not named.rs's `named::corral_tests::zero_is_zero`, which
# `corral_tests::zero_is_zero` is a substring of): on the original they pass,
# on the mutant one fails, and with the canary in place of the block the
# canary alone runs, and fails (the positive control reaches it).
# Without --exact, libtest's filter is a substring: the same names run named.rs's test too.
want inline-cargo-test-substring-collides "3 passed 0 failed" "$(cd inline/lib-pass && cargo test -p inline-cargo --lib -- corral_tests::zero_is_zero corral_tests::unit_is_reached 2>&1 | pf)"
want inline-cargo-test-select-lib "2 passed 0 failed" "$(cd inline/lib-pass && cargo test -p inline-cargo --lib -- --exact corral_tests::zero_is_zero corral_tests::unit_is_reached corral_tests::corral_reach_canary 2>&1 | pf)"
want inline-cargo-test-select-kills-lib "1 passed 1 failed" "$(cd inline/lib-kill && cargo test -p inline-cargo --lib -- --exact corral_tests::zero_is_zero corral_tests::unit_is_reached corral_tests::corral_reach_canary 2>&1 | pf)"
want inline-cargo-test-select-canary-lib "0 passed 1 failed" "$(cd inline/lib-canary && cargo test -p inline-cargo --lib -- --exact corral_tests::zero_is_zero corral_tests::unit_is_reached corral_tests::corral_reach_canary 2>&1 | pf)"
want inline-nextest-select-lib "2 passed 0 failed" "$(cd inline/lib-pass && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=corral_tests::zero_is_zero) | test(=corral_tests::unit_is_reached) | test(=corral_tests::corral_reach_canary))' 2>&1 | pf)"
want inline-nextest-select-kills-lib "1 passed 1 failed" "$(cd inline/lib-kill && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=corral_tests::zero_is_zero) | test(=corral_tests::unit_is_reached) | test(=corral_tests::corral_reach_canary))' 2>&1 | pf)"
want inline-nextest-select-canary-lib "0 passed 1 failed" "$(cd inline/lib-canary && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=corral_tests::zero_is_zero) | test(=corral_tests::unit_is_reached) | test(=corral_tests::corral_reach_canary))' 2>&1 | pf)"
want inline-cargo-test-select-extra "2 passed 0 failed" "$(cd inline/extra-pass && cargo test -p inline-cargo --lib -- --exact extra::corral_tests::above extra::corral_tests::within_is_identity extra::corral_tests::corral_reach_canary 2>&1 | pf)"
want inline-cargo-test-select-kills-extra "1 passed 1 failed" "$(cd inline/extra-kill && cargo test -p inline-cargo --lib -- --exact extra::corral_tests::above extra::corral_tests::within_is_identity extra::corral_tests::corral_reach_canary 2>&1 | pf)"
want inline-cargo-test-select-canary-extra "0 passed 1 failed" "$(cd inline/extra-canary && cargo test -p inline-cargo --lib -- --exact extra::corral_tests::above extra::corral_tests::within_is_identity extra::corral_tests::corral_reach_canary 2>&1 | pf)"
want inline-nextest-select-extra "2 passed 0 failed" "$(cd inline/extra-pass && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=extra::corral_tests::above) | test(=extra::corral_tests::within_is_identity) | test(=extra::corral_tests::corral_reach_canary))' 2>&1 | pf)"
want inline-nextest-select-kills-extra "1 passed 1 failed" "$(cd inline/extra-kill && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=extra::corral_tests::above) | test(=extra::corral_tests::within_is_identity) | test(=extra::corral_tests::corral_reach_canary))' 2>&1 | pf)"
want inline-nextest-select-canary-extra "0 passed 1 failed" "$(cd inline/extra-canary && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=extra::corral_tests::above) | test(=extra::corral_tests::within_is_identity) | test(=extra::corral_tests::corral_reach_canary))' 2>&1 | pf)"
want inline-cargo-test-select-named "1 passed 0 failed" "$(cd inline/named-pass && cargo test -p inline-cargo --lib -- --exact named::corral_tests_2::doubles named::corral_tests_2::corral_reach_canary 2>&1 | pf)"
want inline-nextest-select-named "1 passed 0 failed" "$(cd inline/named-pass && cargo nextest run -E '((binary_id(=inline-cargo))) & (test(=named::corral_tests_2::doubles) | test(=named::corral_tests_2::corral_reach_canary))' 2>&1 | pf)"
# Under #![deny(warnings)] (inline/deny: src/lib.rs with that first line, as a
# crate that denies warnings or builds with RUSTFLAGS=-Dwarnings) the
# reach canary's block still builds, and the canary fails AS A TEST: the
# landing's glob carries #[allow(unused_imports)] (rustGlobAllow), and the
# canary uses nothing it imports. The failing runs are kept for the canary's
# id, which the positive control requires the row's failure reader to name.
pfexit() { # pfexit <cmd...>: pf of the command's output, then its exit status
  local out rc
  out="$("$@" 2>&1)" && rc=0 || rc=$?
  echo "$(printf '%s\n' "$out" | pf) exit $rc"
}
want inline-cargo-test-deny-canary-lib "5 passed 1 failed exit 101" "$(cd inline/deny-lib-canary && pfexit cargo test)"
want inline-nextest-deny-canary-lib "5 passed 1 failed exit 100" "$(cd inline/deny-lib-canary && pfexit cargo nextest run --no-fail-fast)"
want inline-cargo-test-deny-canary-extra "5 passed 1 failed exit 101" "$(cd inline/deny-extra-canary && pfexit cargo test)"
want inline-nextest-deny-canary-extra "5 passed 1 failed exit 100" "$(cd inline/deny-extra-canary && pfexit cargo nextest run --no-fail-fast)"
(cd inline/deny-lib-canary && fails inline-cargo-test-deny-canary-lib.txt cargo test)
(cd inline/deny-lib-canary && fails inline-nextest-deny-canary-lib.txt cargo nextest run --no-fail-fast)
(cd inline/deny-extra-canary && fails inline-cargo-test-deny-canary-extra.txt cargo test)
(cd inline/deny-extra-canary && fails inline-nextest-deny-canary-extra.txt cargo nextest run --no-fail-fast)
# Without the allow the same file does not build: the glob alone breaks it.
cp -r inline/deny-lib-canary inline/deny-no-allow && sed -i '/^#\[allow(unused_imports)\]$/d' inline/deny-no-allow/src/lib.rs
want inline-cargo-test-deny-without-allow "error: unused import: \`super::*\`" "$(cd inline/deny-no-allow && cargo test 2>&1 | grep -m1 -o 'error: unused import: .*')"

# --- package scripts: how each launcher hands extra arguments to the script --
# (internal/lang/invocation.go's launcher table; keep the separators equal)
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
YARN=1.22.22 PNPM=9.15.9
jestran() { grep -oE '[0-9]+ passed' | head -1 | cut -d' ' -f1; }
want npm-test-list 5 "$(npm test --silent -- --json --testNamePattern '^\b$' calc.jest.test.js 2>/dev/null | node -e 'const j=JSON.parse(require("fs").readFileSync(0));console.log(j.numTotalTests)')"
want npm-test-single 1 "$(npm test -- --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want npm-run-env-single 1 "$(npm run test:env -- --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want npm-test-bail 0 "$(npm test -- --bail calc.jest.test.js >/dev/null 2>&1; echo $?)"
want yarn-test-single 1 "$(corepack "yarn@$YARN" test --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want pnpm-test-single 1 "$(corepack "pnpm@$PNPM" test -- --testNamePattern '^top level$' calc.jest.test.js 2>&1 | jestran)"
want composer-test-single 1 "$(composer test -- --filter '/(?:^|\\)CalcTest::testAdds(?: with data set .*)?$/' tests/CalcTest.php 2>&1 | sed -n 's/^OK (\([0-9]*\) test.*/\1/p')"

capture_swift || fail=1
capture_swift_hook || fail=1
capture_swift_coverage || fail=1

{
  echo "Captured by scripts/capture-runner-fixtures.sh on $(date -u +%Y-%m-%d)."
  echo "jest $JEST, vitest $VITEST, mocha $MOCHA (npm); PHPUnit $PHPUNIT (composer)"
  echo "$(node --version | sed 's/^/node /'); $(ruby -e 'require "rspec/core"; print "rspec-core ", RSpec::Core::Version::STRING')"
  echo "Launchers: npm $(npm --version) and pnpm $PNPM take extra script arguments after --,"
  echo "yarn $YARN takes them directly, composer $(composer --version 2>/dev/null | awk '{print $3}') after --."
  echo "Suites: src/. Each *-list file is the stdout of that runner's list command"
  echo "(*-units files, added 2026-10-25 on node $(node --version): the unit listing under"
  echo "its working-directory prelude, the capture directory written as /fixture)"
  echo "(jest and mocha JSON reduced to the fields the parsers read)."
  echo "Each *-fail, *-suitefail, *-loadfail and *-hookfail file is stdout+stderr of"
  echo "the runner's DEFAULT command on src/fail, not a TTY, as the jail runs it;"
  echo "$(ruby -e 'require "minitest"; print "minitest ", Minitest::VERSION')."
  echo "Go, Ruby, PHP and Rust units and the *-help files (added 2026-10-25): $(go version | awk '{print $3}'),"
  echo "gotestsum $GOTESTSUM (go install), $(php -r 'echo "PHP ", PHP_VERSION;'),"
  echo "$(cargo --version | awk '{print "cargo", $2}'), $(cargo nextest --version 2>/dev/null | head -1)."
  echo "*-help files are each runner's --help; libtest-help.txt is a cargo-built test binary's."
  swift_provenance
} > "$OUT/PROVENANCE.txt"

exit "$fail"
