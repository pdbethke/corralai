// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"slices"
	"strings"
)

// LLVM-COV EXPORT, REDUCED ONCE FOR EVERY LANGUAGE THAT HAS ONE.
//
// Rust (rustc's -C instrument-coverage) and Swift (`swift test
// --enable-code-coverage`) both end in the JSON `llvm-cov export` writes, so
// both reduce it with the one program below: what differs between them is
// how the JSON is produced and where it lands, and that stays in each
// plugin's CoverageCmd. Rust asks llvm-cov for it with -summary-only; Swift
// reads the full export SwiftPM writes itself, segments, branches and
// functions as well (16.2 MB for one run of taplin/Lariat at be0c5fd, which
// this reducer read in 0.04 s with mawk and 0.3 s with gawk, measured
// 2026-11-19 with Swift 6.3.2 in a Docker container on one 32-core machine).
// The reducer
// reads only each file's "filename" and its summary's "lines", which both
// forms carry.

// llvmCovReduceShell is the shell that reduces the llvm-cov export at json
// (a shell word, quoted by the caller) to the shared line-count grammar
// (corralCoverageCounts) under header, with paths relative to the working
// directory and the files p declares as dependencies or build output left
// out (llvmCovSkipped).
//
// The JSON is reduced into "$d/reduced" before the header is printed, so a
// JSON that is empty or cut short prints no header at all: the reducer
// refuses it (see llvmCovExportReduce) and the script's own `unavailable`
// shell function, which every caller defines, prints the reason instead. A
// partial file list read from a truncated export would look like a
// measurement of the files before the cut, and say nothing of the rest.
func llvmCovReduceShell(p Plugin, header, json string) string {
	return `awk -v a="$(pwd -P)/" -v b="$PWD/" -v skip=` + shellQuote(llvmCovSkipped(p).String()) + ` ` + shellQuote(llvmCovExportReduce) + ` ` + json +
		` > "$d/reduced" || unavailable "the llvm-cov export is empty or cut short: it does not end the way a complete export ends"` + "\n" +
		`printf '%s\n' ` + shellQuote(header) + "\n" +
		`cat "$d/reduced"` + "\n"
}

// llvmCovSkipped matches the working-directory-relative paths the reducer
// leaves out: the directories p declares as dependencies (DependencyDirs:
// cargo vendor's vendor/, SwiftPM's .build/ with its checkouts and .swiftpm/)
// and as build output (BuildDirs: cargo's target/, where build-script output
// that a crate include!s lives; SwiftPM's .build/, where the test runner and
// test discovery sources it generates are compiled from), read from those
// declarations so a new one is skipped here too. Top level only: that is
// where both tools put them, and a module directory named vendor/ deeper in a
// crate's source is the crate's own code.
func llvmCovSkipped(p Plugin) *regexp.Regexp {
	var names []string
	add := func(name string) {
		if q := regexp.QuoteMeta(name); !slices.Contains(names, q) {
			names = append(names, q)
		}
	}
	for _, d := range p.DependencyDirs() {
		add(d.Name)
	}
	if b, ok := p.(BuildDirer); ok {
		for _, d := range b.BuildDirs() {
			add(d.Name)
		}
	}
	return regexp.MustCompile(`^(` + strings.Join(names, "|") + `)/`)
}

// llvmCovExportReduce reads an `llvm-cov export -format=text` JSON and prints
// one `<executed> <executable> <path>` line per file of the working directory
// (a and b: its physical and logical paths, each with a trailing slash). An
// executable line is what llvm-cov's line summary counts: a line some coverage
// region of compiled code starts on or spans.
//
// The export is one JSON object, which llvm-cov writes on one line with its
// keys sorted, so a complete one ends with its "type" and "version":
// `"type":"llvm.coverage.json.export","version":"<v>"}`. Anything that does
// not end that way (an empty file, one cut short) is refused with exit
// status 1 and nothing printed.
//
// The JSON is split on each file's "filename" key. Its keys are sorted, so in
// the full export the file's branches and expansions come before it and its
// segments and summary after it, and with -summary-only the summary alone
// follows it; either way the first "lines" object after the name is that
// file's (the totals' "lines" follows the last file, after its own). A
// function's "filenames" list is not matched.
//
// A path carrying a JSON escape (a quote or backslash in a file name) cannot
// be read by splitting on quotes, and a "lines" summary without both counts
// is not a count: each is disclosed as uncounted rather than guessed at. A
// file llvm-cov maps no line of (declarations only) is left out, as every
// reducer does: nothing in it could have run.
const llvmCovExportReduce = `{ buf = buf $0 "\n" }
END {
  if (buf !~ /"type":"llvm\.coverage\.json\.export","version":"[^"]*"\}[ \t\r\n]*$/) {
    print "the llvm-cov export is empty or cut short" > "/dev/stderr"
    exit 1
  }
  n = split(buf, parts, /"filename":"/)
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
