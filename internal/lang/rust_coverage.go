// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// rustCoverageHeaderV2 opens the reduced report rustPlugin.CoverageCmd
// prints: the shared line-count grammar (see corralCoverageCounts), whose
// version is 2 in every language that speaks it. Rust never emitted the
// retired boolean version 1, and starting its header at v2 keeps one number
// meaning one grammar.
//
// rustCoverageUnavailable opens the line printed INSTEAD of a report when
// the instrument cannot run at all (no llvm-tools for the toolchain, or a
// step of the reduction failing), followed by the reason. It shares the
// header's family on purpose: corralReportBody would otherwise read it as a
// stale reducer, and the operator would be told about a version instead of
// the missing component.
const (
	rustCoverageHeaderV2    = "corral-rust-coverage: v2"
	rustCoverageUnavailable = "corral-rust-coverage: unavailable"
)

// CoverageCmd wraps a `cargo test` command in source-based coverage.
//
// The script, in order:
//
//  1. Finds llvm-profdata and llvm-cov: first in the llvm-tools component of
//     the toolchain the suite will build with (`rustc [+toolchain] --print
//     sysroot`, under lib/rustlib/<host>/bin — where rustup puts them, never
//     on PATH), then on PATH. Neither is a DISCLOSURE: the
//     rustCoverageUnavailable line with the reason, printed before the suite
//     runs, because an instrumented build nobody can read is only cost. A
//     PATH llvm-cov from another LLVM release than rustc's can fail to read
//     the profile; that failure is disclosed the same way, at step 4.
//  2. Runs the operator's own command with RUSTFLAGS gaining
//     `-C instrument-coverage` (any RUSTFLAGS already set are kept) and one
//     profile per process (%p) and binary (%m) under the temp dir. Exit 0 and
//     101 — cargo's code for failing tests and for a failed build alike — fall
//     through: a failing suite's profiles are complete. Any other code (a
//     signal, a killed runner) is re-raised, because a test binary killed
//     mid-run writes no profile and the rest would read as the whole suite.
//     No profile at all prints a header with no entries, which the parser
//     refuses as a run that measured nothing (the build most likely failed).
//  3. Lists the test executables the run used: the same cargo arguments
//     (everything before `--`) with `--no-run --message-format=json`, which
//     rebuilds nothing, and each artifact's "executable".
//  4. Merges the profiles and has `llvm-cov export -summary-only` count each
//     file's lines over those executables, then reduces the JSON in awk to
//     one `<executed> <executable> <path>` line per file, paths relative to
//     the working directory (the jail runs the suite in a copy, so an
//     absolute path would be outside the caller's root). Files outside the
//     working directory (the cargo registry, git dependencies) and under its
//     vendor/ and target/ (vendored crates, build-script output; see
//     rustCoverageSkipped) are dependencies and are left out. A file
//     llvm-cov maps no line of (declarations only) is left out as well, as
//     every reducer does: nothing in it could have run.
//
// CARGO_TARGET_DIR becomes <the target dir>/corral-coverage. Instrumented
// artifacts and ordinary ones then sit in different directories, so neither
// build evicts the other's cache on the workspace substrate (cargo-llvm-cov
// keeps a separate llvm-cov-target for the same reason); the cost is disk
// for a second build. Like BuildDirs, it follows CARGO_TARGET_DIR but not a
// build.target-dir in the project's config.
//
// RUSTFLAGS takes precedence over a build.rustflags in the project's cargo
// config (cargo's documented order), so a project that sets flags only there
// is built for coverage without them; CARGO_ENCODED_RUSTFLAGS, when set,
// wins over RUSTFLAGS and leaves the build uninstrumented, which reaches the
// parser as a run that measured nothing. Both are cargo's rules, stated here
// because they change what is measured.
//
// ok=false for anything but `cargo [+toolchain] test`: step 3 re-reads the
// command's cargo arguments, so a shell wrapper, cargo-nextest (whose flags
// `cargo test` does not take), a global flag before the subcommand, or a
// command already carrying --no-run or --message-format cannot be listed
// faithfully, and is refused rather than measured with the wrong binaries.
func (rustPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	toolchain, ok := rustCoverageShape(testCmd)
	if !ok {
		return nil, false
	}
	head, _ := splitAtDashDash(testCmd)

	// rustc beside an absolute cargo is the toolchain that cargo builds
	// with; a bare cargo resolves its rustc from PATH the same way.
	rustcSetup, rustc := "", shellQuote("rustc")
	if filepath.IsAbs(testCmd[0]) {
		rustcSetup = `r=` + shellQuote(filepath.Join(filepath.Dir(testCmd[0]), "rustc")) + `; [ -x "$r" ] || r=rustc` + "\n"
		rustc = `"$r"`
	}
	if toolchain != "" {
		rustc += " " + shellQuote(toolchain)
	}

	listArgs := append(slices.Clone(head), "--no-run", "--message-format=json")

	script := `d=$(mktemp -d) && trap 'rm -rf "$d"' EXIT || exit 1` + "\n" +
		`unavailable() { printf '%s %s\n' ` + shellQuote(rustCoverageUnavailable) + ` "$1"; exit 0; }` + "\n" +
		rustcSetup +
		`host=$(` + rustc + ` -vV 2>/dev/null | sed -n 's/^host: //p')` + "\n" +
		`lt="$(` + rustc + ` --print sysroot 2>/dev/null)/lib/rustlib/$host/bin"` + "\n" +
		`if [ -n "$host" ] && [ -x "$lt/llvm-profdata" ] && [ -x "$lt/llvm-cov" ]; then pd="$lt/llvm-profdata"; cov="$lt/llvm-cov"` + "\n" +
		`elif command -v llvm-profdata >/dev/null 2>&1 && command -v llvm-cov >/dev/null 2>&1; then pd=llvm-profdata; cov=llvm-cov` + "\n" +
		`else unavailable "llvm-profdata and llvm-cov are in neither the toolchain's llvm-tools ($lt) nor PATH: install them with 'rustup component add llvm-tools' for the toolchain the suite builds with"` + "\n" +
		`fi` + "\n" +
		`mkdir -p "$d/prof" || exit 1` + "\n" +
		`export RUSTFLAGS="${RUSTFLAGS:+$RUSTFLAGS }-C instrument-coverage" LLVM_PROFILE_FILE="$d/prof/%p-%m.profraw" CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-target}/corral-coverage"` + "\n" +
		shellJoin(testCmd) + ` >&2; rc=$?; ` + suiteExitTrailer + "\n" +
		`case $rc in 0|101) ;; *) exit $rc ;; esac` + "\n" +
		`set -- "$d"/prof/*.profraw` + "\n" +
		`if [ ! -e "$1" ]; then printf '%s\n' ` + shellQuote(rustCoverageHeaderV2) + `; exit 0; fi` + "\n" +
		shellJoin(listArgs) + ` > "$d/build.json" || unavailable "cargo test --no-run, which lists the test executables, failed (exit $?)"` + "\n" +
		`sed -n 's/.*"executable":"\([^"]*\)".*/\1/p' "$d/build.json" | sort -u > "$d/objs"` + "\n" +
		`set --; while IFS= read -r o; do set -- "$@" -object "$o"; done < "$d/objs"` + "\n" +
		`[ $# -gt 0 ] || unavailable "cargo test --no-run listed no test executable"` + "\n" +
		`"$pd" merge -sparse -o "$d/merged.profdata" "$d"/prof/*.profraw >&2 || unavailable "$pd merge failed (exit $?); its message is on stderr"` + "\n" +
		`"$cov" export -format=text -summary-only -instr-profile="$d/merged.profdata" "$@" > "$d/export.json" || unavailable "$cov export failed (exit $?); its message is on stderr"` + "\n" +
		`printf '%s\n' ` + shellQuote(rustCoverageHeaderV2) + "\n" +
		`awk -v a="$(pwd -P)/" -v b="$PWD/" -v skip=` + shellQuote(rustCoverageSkipped().String()) + ` ` + shellQuote(rustCoverageReduce) + ` "$d/export.json"` + "\n"
	return []string{"sh", "-c", script}, true
}

// rustCoverageShape accepts `cargo [+toolchain] test|t [args] [-- args]` and
// returns the toolchain override ("" for none). See CoverageCmd for why every
// other shape is refused.
func rustCoverageShape(testCmd []string) (toolchain string, ok bool) {
	if len(testCmd) < 2 || filepath.Base(testCmd[0]) != "cargo" {
		return "", false
	}
	sub := 1
	if strings.HasPrefix(testCmd[1], "+") {
		toolchain, sub = testCmd[1], 2
	}
	if sub >= len(testCmd) || (testCmd[sub] != "test" && testCmd[sub] != "t") {
		return "", false
	}
	head, _ := splitAtDashDash(testCmd)
	for _, a := range head[sub+1:] {
		if a == "--no-run" || a == "--message-format" || strings.HasPrefix(a, "--message-format=") {
			return "", false
		}
	}
	return toolchain, true
}

// shellJoin quotes every word of argv for a POSIX shell.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// rustCoverageSkipped matches the working-directory-relative paths the
// reducer leaves out: the directories rustPlugin declares as dependencies
// (DependencyDirs: cargo vendor's vendor/) and as build output (BuildDirs:
// target/, where build-script output that a crate include!s lives), read
// from those declarations so a new one is skipped here too. Top level only:
// that is where cargo puts them, and a module directory named vendor/ deeper
// in a crate's source is the crate's own code.
func rustCoverageSkipped() *regexp.Regexp {
	var names []string
	for _, d := range (rustPlugin{}).DependencyDirs() {
		names = append(names, regexp.QuoteMeta(d.Name))
	}
	for _, d := range (rustPlugin{}).BuildDirs() {
		names = append(names, regexp.QuoteMeta(d.Name))
	}
	return regexp.MustCompile(`^(` + strings.Join(names, "|") + `)/`)
}

// rustCoverageReduce reads `llvm-cov export -format=text -summary-only` and
// prints one `<executed> <executable> <path>` line per file of the working
// directory (a and b: its physical and logical paths, each with a trailing
// slash). An executable line is what llvm-cov's line summary counts: a line
// some coverage region of compiled code starts on or spans. The JSON is
// split on each file's "filename" key; the first "lines" object after it is
// that file's (the totals' "lines" follows the last file, after its own).
//
// A path carrying a JSON escape (a quote or backslash in a file name) cannot
// be read by splitting on quotes, and a "lines" summary without both counts
// is not a count: each is disclosed as uncounted rather than guessed at.
const rustCoverageReduce = `{
  n = split($0, parts, /\{"filename":"/)
  for (i = 2; i <= n; i++) {
    s = parts[i]
    f = s; sub(/".*/, "", f)
    if (index(f, a) == 1) f = substr(f, length(a) + 1)
    else if (index(f, b) == 1) f = substr(f, length(b) + 1)
    else continue
    if (f ~ skip) continue
    if (f ~ /\\/) { print "# uncounted " f ": its path carries a JSON escape this reducer does not decode"; continue }
    if (!match(s, /"lines":\{[^}]*\}/)) { print "# uncounted " f ": llvm-cov gave no line summary"; continue }
    l = substr(s, RSTART, RLENGTH)
    c = l; if (!sub(/.*"count":/, "", c)) c = ""; sub(/[^0-9].*/, "", c)
    v = l; if (!sub(/.*"covered":/, "", v)) v = ""; sub(/[^0-9].*/, "", v)
    if (c == "" || v == "") { print "# uncounted " f ": llvm-cov's line summary has no count"; continue }
    if (c + 0 == 0) continue
    print (v + 0) " " (c + 0) " " f
  }
}`

// ParseCoverage is derived from CoverageCounts: a file is executed when any
// of its lines ran. Rust has no divergence from that rule.
func (p rustPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	return executedFromCounts(p.CoverageCounts(stdout, modulePath))
}

// CoverageCounts reads the report CoverageCmd prints (the shared grammar,
// corralCoverageCounts). A rustCoverageUnavailable line is an ERROR carrying
// its reason: an instrument that could not run measured nothing, and saying
// why is the whole of what the run produced.
func (rustPlugin) CoverageCounts(stdout, modulePath string) (map[string]LineCounts, error) {
	if reason, ok := CoverageUnavailable(stdout); ok {
		return nil, fmt.Errorf("lang: rust coverage is unavailable: %s", reason)
	}
	return corralCoverageCounts(stdout, rustCoverageHeaderV2, "rust", modulePath)
}
