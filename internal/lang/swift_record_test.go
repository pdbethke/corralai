// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The `swift test` row's graded command is the record wrapper, told that
// swift-testing derives its record's name from the path (6.4.2:
// `-swift-testing.xml`; 6.3.2: the path itself) and that a fallback leaves
// the option out, with one `--xunit-output=<token>.xml` word after the
// command's options. A command the row derives from the operator's (one test
// alone, one target) is hooked the same way.
func TestSwiftHookWrapsASwiftTestRun(t *testing.T) {
	p, _ := ByName("swift")
	for _, operator := range [][]string{
		{"swift", "test"},
		{"swift", "test", "--package-path", "pkg", "--filter", "^CalcTests\\."},
		{"xcrun", "swift", "test", "-c", "release"},
	} {
		got, ok := ReportCmd(p, Cmd(operator), operator)
		want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJUnit, "drop", "-swift-testing.xml/.xml"},
			append(slices.Clone(operator), "--xunit-output="+recordToken+".xml")...)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want the record wrapper with --xunit-output appended", operator, got[min(len(got), 5):], ok)
		}
	}
	single, ok := swiftSingleCmd([]string{"swift", "test"}, "", "CalcTests.parses()")
	if !ok {
		t.Fatal("no one-test command")
	}
	if got, ok := ReportCmd(p, Cmd(single), single); !ok || got[len(got)-1] != "--xunit-output="+recordToken+".xml" || !slices.Contains(got, "--filter") {
		t.Errorf("one-test command graded as %q ok=%v, want it hooked", got, ok)
	}
}

// What the hook leaves alone, and why: a command that is no `swift test`
// run (the compile check's `swift build`, a listing); one whose tests run in
// parallel, where XCTest writes the path itself (Swift 6.4.2) or one file
// is written by both frameworks (6.3.2), so the file read would not be
// swift-testing's alone; one with its own --xunit-output, which 6.3.2
// writes in place of corral's (the first given wins) and 6.4.2 drops for
// corral's (the last wins), so wrapping it would lose either corral's record
// or the operator's; and the shapes ReportCmd never reads. Each keeps its
// words and says nothing.
func TestSwiftHookDeclines(t *testing.T) {
	p, _ := ByName("swift")
	for _, operator := range [][]string{
		{"swift", "build", "--target", "CalcTests"},
		{"swift", "test", "list"},
		{"swift", "test", "--parallel"},
		{"swift", "test", "--num-workers", "2"},
		{"swift", "test", "--num-workers=2"},
		{"swift", "test", "--xunit-output", "mine.xml"},
		{"swift", "test", "--xunit-output=mine.xml"},
		{"swift", "test", "--", "x"},
		{"sh", "-c", "swift test"},
	} {
		if got, ok := ReportCmd(p, Cmd(operator), operator); ok || !slices.Equal(got, operator) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want it unchanged", operator, got, ok)
		}
		if lines := RunDisclosures(p, t.TempDir(), Cmd(operator)); slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "--xunit-output") }) {
			t.Errorf("%q: the hook is disclosed though corral does not wrap it: %q", operator, lines)
		}
	}
}

// The scan line says what corral adds to a `swift test` run it wraps, and
// that XCTest's failures are still read from the printed lines.
func TestRunDisclosuresSayWhenTheSwiftHookWraps(t *testing.T) {
	p, _ := ByName("swift")
	lines := RunDisclosures(p, t.TempDir(), Cmd([]string{"swift", "test"}))
	i := slices.IndexFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "swift test: corral runs every graded run through `sh -c`")
	})
	if i < 0 {
		t.Fatalf("no disclosure of the record corral adds: %q", lines)
	}
	for _, want := range []string{"--xunit-output=", "swift-testing", "XCTest", "printed", "exit code are unchanged"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("the disclosure does not say %q: %s", want, lines[i])
		}
	}
}

// swiftHookToolchains are the releases the hook's captures were made on, one
// set each (capture-runner-fixtures.sh capture_swift_hook): the two CI pins.
var swiftHookToolchains = []string{"6.3.2", "6.4.2"}

// sameSet reports whether a and b hold the same strings, each once.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// The record names each failing swift-testing test by the id `swift test
// list` prints for it, on both releases: a test in a suite, a parameterised
// one (once, whatever case failed), a top-level one, one in a nested suite
// and one in a suite with a display name (the id carries the type's name,
// not the display name).
func TestSwiftRecordIDsAreListedIDs(t *testing.T) {
	for _, v := range swiftHookToolchains {
		t.Run(v, func(t *testing.T) {
			for _, c := range []struct {
				fixture, list string
				want          []string
			}{
				{"swift-hook-fail-" + v + ".txt", "swift-list.txt", []string{"CalcTests.Arithmetic/adds()", "CalcTests.Arithmetic/doubles(value:)", "CalcTests.Arithmetic/fails()"}},
				{"swift-hook-edge-" + v + ".txt", "swift-hook-edge-list-" + v + ".txt", []string{"CalcTests.throwsAnError()", "CalcTests.Outer/Inner/nested()", "CalcTests.Pretty/inPretty()", "CalcTests.ThrowingInit/neverRuns()"}},
			} {
				got, ok := swiftRecordedFailures(readFixture(t, c.fixture))
				if !ok || !sameSet(got, c.want) {
					t.Errorf("%s: record ids %q ok=%v, want %q", c.fixture, got, ok, c.want)
				}
				listed := swiftParseList(readFixture(t, c.list))
				for _, id := range got {
					if !slices.Contains(listed, id) {
						t.Errorf("%s: record id %q is not one `swift test list` printed (%s)", c.fixture, id, c.list)
					}
				}
			}
		})
	}
}

// With a record the readers take its ids first, then every printed name no
// recorded id stands for: XCTest's, which swift-testing's record never
// holds, and a display name, which the record does not carry. A printed
// function name a recorded id ends in is that test, already named.
func TestSwiftFailuresReadTheRecordFirst(t *testing.T) {
	for _, v := range swiftHookToolchains {
		t.Run(v, func(t *testing.T) {
			out := readFixture(t, "swift-hook-fail-"+v+".txt")
			got := swiftFailures(out)
			recorded := []string{"CalcTests.Arithmetic/adds()", "CalcTests.Arithmetic/doubles(value:)", "CalcTests.Arithmetic/fails()"}
			if len(got) != 5 || !sameSet(got[:3], recorded) || !slices.Equal(got[3:], []string{"AdderTests.testFails", `"adds two numbers"`}) {
				t.Errorf("swiftFailures = %q, want the record's %q, then AdderTests.testFails and the display name", got, recorded)
			}
			if first := swiftFirstFailure([]byte(out)); first != got[0] || !slices.Contains(recorded, first) {
				t.Errorf("swiftFirstFailure = %q, want the record's first, %q", first, got[0])
			}
		})
	}
}

// When only XCTest fails, swift-testing's record names nothing and the
// failure is read from XCTest's printed line, as before the hook.
func TestSwiftXCTestFailureIsReadFromThePrintedLines(t *testing.T) {
	for _, v := range swiftHookToolchains {
		out := readFixture(t, "swift-hook-xctest-"+v+".txt")
		if ids, ok := swiftRecordedFailures(out); !ok || len(ids) != 0 {
			t.Errorf("%s: record %q ok=%v, want a record naming no failure", v, ids, ok)
		}
		if got := swiftFailures(out); !slices.Equal(got, []string{"AdderTests.testFails"}) {
			t.Errorf("%s: swiftFailures = %q, want AdderTests.testFails", v, got)
		}
	}
}

// The record is not every swift-testing failure: a test that crashes the
// test process leaves Swift 6.4.2's record without that product's suite, a
// well-formed record naming none of the failures printed before the crash,
// and 6.3.2's record cut short, which is not read. Either way the printed
// names are the answer, so a crash costs no name the printed lines gave.
func TestSwiftReadersTakeWhatTheRecordOmits(t *testing.T) {
	want := []string{`"adds two numbers"`, "fails()", "doubles(value:)"}
	out := readFixture(t, "swift-hook-crash-6.4.2.txt")
	if ids, ok := swiftRecordedFailures(out); !ok || len(ids) != 0 {
		t.Fatalf("6.4.2: record %q ok=%v, want a well-formed record naming nothing; the fixture no longer shows what it omits", ids, ok)
	}
	if got := swiftFailures(out); !sameSet(got, want) {
		t.Errorf("6.4.2: swiftFailures = %q, want the printed %q", got, want)
	}
	out = readFixture(t, "swift-hook-crash-6.3.2.txt")
	if _, ok := swiftRecordedFailures(out); ok {
		t.Fatal("6.3.2: a record was read from one the crash cut short")
	}
	if got := swiftFailures(out); !sameSet(got, want) {
		t.Errorf("6.3.2: swiftFailures = %q, want the printed %q", got, want)
	}
}

// Without a record the readers read the printed lines, as they did before
// the hook: a run whose hook could not set up, a block cut short, broken
// XML, a block of another format, and a testcase with no classname or with
// a name that is no swift-testing function (an XCTest record).
func TestSwiftFallsBackToPrintedLinesWithoutARecord(t *testing.T) {
	for _, v := range swiftHookToolchains {
		if out := readFixture(t, "swift-hook-fallback-"+v+".txt"); strings.Contains(out, recordBegin) || swiftFailures(out) != nil {
			t.Errorf("%s: the fallback run printed a block or names a failure", v)
		}
		hooked := readFixture(t, "swift-hook-fail-"+v+".txt")
		printed := swiftFailures(printedPart(t, "swift-hook-fail-"+v+".txt"))
		end := strings.LastIndex(hooked, recordEnd)
		for name, out := range map[string]string{
			"block cut short": hooked[:end],
			"broken xml":      strings.Replace(hooked, "</testsuites>", "</testsuite>", 1),
			"another format":  strings.Replace(hooked, recordBegin+" junit", recordBegin+" jest-json", 1),
			"no classname":    strings.Replace(hooked, `classname="CalcTests.Arithmetic"`, `classname=""`, 1),
			"an XCTest name":  strings.Replace(hooked, `name="fails()"`, `name="testFails"`, 1),
		} {
			if _, ok := swiftRecordedFailures(out); ok {
				t.Errorf("%s %s: a record was read", v, name)
			}
			if got := swiftFailures(out); !slices.Equal(got, printed) {
				t.Errorf("%s %s: %q, want the printed lines' %q", v, name, got, printed)
			}
		}
	}
}

// digits is a run of digits, with a decimal part.
var digits = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

// swiftPrintedShape is a `swift test` output's lines with the build's
// progress lines left out and every number written N, sorted: what the run
// printed, whatever order its parallel tests finished in and however long
// each took.
func swiftPrintedShape(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasPrefix(l, "[") || strings.HasPrefix(l, "Build") || strings.HasPrefix(l, "Compiling") || strings.HasPrefix(l, "Write ") || strings.HasPrefix(l, "# exit:") {
			continue
		}
		lines = append(lines, digits.ReplaceAllString(l, "N"))
	}
	slices.Sort(lines)
	return lines
}

// The hook changes nothing `swift test` prints, on either release: the
// hooked capture up to its record block is the unhooked run's lines, in
// another order (swift-testing runs its tests in parallel) and with other
// timings, and both exit 1 (the capture script checks that).
func TestSwiftHookLeavesThePrintedOutputAlone(t *testing.T) {
	for _, v := range swiftHookToolchains {
		hooked := swiftPrintedShape(printedPart(t, "swift-hook-fail-"+v+".txt"))
		plain := swiftPrintedShape(readFixture(t, "swift-hook-unhooked-"+v+".txt"))
		if !slices.Equal(hooked, plain) {
			t.Errorf("%s: printed output differs with the hook:\n--- hooked\n%s\n--- unhooked\n%s", v, strings.Join(hooked, "\n"), strings.Join(plain, "\n"))
		}
	}
}

// The capture script runs the row's graded command, taken through
// scripts/reportcmd and never copied, and each capture is that wrapper's
// output on the release its name says.
func TestSwiftHookCaptureIsTheRowsCommand(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, line := range []string{
		`mapfile -d '' GRADED < <(cd "$REPO" && go run ./scripts/reportcmd swift swift test)`,
		`hookrun "swift-hook-pass-$v.txt" "${GRADED[@]}"`,
		`hookrun "swift-hook-fallback-$v.txt" sh -c 'PATH=$0:$PATH exec "$@"' "$DIR/nomktemp" "${GRADED[@]}"`,
		`CORRAL_FAIL_TESTING=1 CORRAL_FAIL_XCTEST=1 hookrun "swift-hook-fail-$v.txt" "${GRADED[@]}"`,
		`CORRAL_FAIL_TESTING=1 CORRAL_FAIL_XCTEST=1 hookrun "swift-hook-unhooked-$v.txt" swift test`,
		`CORRAL_FAIL_XCTEST=1 hookrun "swift-hook-xctest-$v.txt" "${GRADED[@]}"`,
		`hookrun "swift-hook-edge-$v.txt" "${GRADED[@]}"`,
		`CORRAL_FAIL_TESTING=1 hookrun "swift-hook-crash-$v.txt" "${GRADED[@]}"`,
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, v := range swiftHookToolchains {
		for _, f := range []string{"fail", "xctest", "edge", "crash", "pass"} {
			out := readFixture(t, "swift-hook-"+f+"-"+v+".txt")
			if !strings.Contains(out, "\n"+recordBegin+" junit\n") || !strings.Contains(out, "\n"+recordEnd+"\n") {
				t.Errorf("swift-hook-%s-%s.txt: no record block", f, v)
			}
		}
		if out := readFixture(t, "swift-hook-pass-"+v+".txt"); !strings.Contains(out, "Testing Library Version: "+v+" ") {
			t.Errorf("swift-hook-pass-%s.txt: not a Swift %s run", v, v)
		}
	}
	// What the row's suffixes and declines rest on, as measured.
	for v, want := range map[string]string{
		"6.3.2": "plain exit 0: rec.xml \nparallel exit 0: rec.xml \ntwice exit 0: operator.xml \ndevnull exit 1\n",
		"6.4.2": "plain exit 0: rec-swift-testing.xml \nparallel exit 0: rec-swift-testing.xml rec.xml \ntwice exit 0: corral-swift-testing.xml \ndevnull exit 1\n",
	} {
		if got := readFixture(t, "swift-xunit-files-"+v+".txt"); got != want {
			t.Errorf("swift-xunit-files-%s.txt = %q, want %q", v, got, want)
		}
	}
}

// On the edge shapes every printed failure is a function name a recorded id
// ends in, so the readers name the record's four ids and nothing twice. A
// test whose arguments cannot be computed (badArguments) fails the run and
// is named by neither source: its printed line says it "recorded an issue"
// and never that it failed, and the record has no testcase for it.
func TestSwiftEdgeFailuresAreNamedOnce(t *testing.T) {
	want := []string{"CalcTests.throwsAnError()", "CalcTests.Outer/Inner/nested()", "CalcTests.Pretty/inPretty()", "CalcTests.ThrowingInit/neverRuns()"}
	for _, v := range swiftHookToolchains {
		out := readFixture(t, "swift-hook-edge-"+v+".txt")
		if got := swiftFailures(out); !sameSet(got, want) {
			t.Errorf("%s: swiftFailures = %q, want %q", v, got, want)
		}
		if !strings.Contains(out, "✘ Test badArguments(n:) recorded an issue") || strings.Contains(hookedRecord(t, "swift-hook-edge-"+v+".txt"), "badArguments") {
			t.Errorf("%s: the capture no longer shows badArguments printed and not recorded", v)
		}
	}
}
