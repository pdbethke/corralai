// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// hookedRecord is the body of the JUnit block in a captured hooked run,
// failing the test when there is none.
func hookedRecord(t *testing.T, fixture string) string {
	t.Helper()
	format, body, ok := RecordBlock(readFixture(t, fixture))
	if !ok || format != recordJUnit {
		t.Fatalf("%s: no complete junit block (format %q ok=%v)", fixture, format, ok)
	}
	return body
}

// printedPart is a hooked capture up to its record block: what PHPUnit
// itself printed in that run.
func printedPart(t *testing.T, fixture string) string {
	t.Helper()
	out := readFixture(t, fixture)
	i := strings.Index(out, "\n"+recordBegin+" ")
	if i < 0 {
		t.Fatalf("%s: no record block", fixture)
	}
	return out[:i]
}

// Every failing and erroring testcase of PHPUnit's own record of a run
// (PHPUnit 11.5.57, capture-runner-fixtures.sh phpunit-hook-fail.txt), in
// document order, the data sets inside their method's nested <testsuite>
// included, with the class as PHPUnit writes it (backslashes in `class`,
// dots in `classname`).
func TestJUnitFailuresReadsFailuresAndErrorsInOrder(t *testing.T) {
	got, err := junitFailures(hookedRecord(t, "phpunit-hook-fail.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []junitCase{
		{Suite: "FTest", Class: "FTest", ClassName: "FTest", Name: "testAdds", Kind: "failure"},
		{Suite: "FTest::testData", Class: "FTest", ClassName: "FTest", Name: `testData with data set "two"`, Kind: "failure"},
		{Suite: "FTest", Class: "FTest", ClassName: "FTest", Name: "testBoom", Kind: "error"},
		{Suite: "DTest::testNamed", Class: "DTest", ClassName: "DTest", Name: `testNamed with data set "first"`, Kind: "failure"},
		{Suite: "DTest::testNamed", Class: "DTest", ClassName: "DTest", Name: `testNamed with data set "third"`, Kind: "failure"},
		{Suite: "DTest::testNamed", Class: "DTest", ClassName: "DTest", Name: `testNamed with data set "x (y)"`, Kind: "failure"},
		{Suite: "DTest::testUnnamed", Class: "DTest", ClassName: "DTest", Name: "testUnnamed with data set #0", Kind: "failure"},
		{Suite: "DTest::testUnnamed", Class: "DTest", ClassName: "DTest", Name: "testUnnamed with data set #2", Kind: "failure"},
		{Suite: `App\Tests\NTest`, Class: `App\Tests\NTest`, ClassName: "App.Tests.NTest", Name: "testNamespaced", Kind: "failure"},
	}
	var files []string
	for i := range got {
		files = append(files, got[i].File)
		if n := len(got[i].Suites); n == 0 || got[i].Suites[n-1] != got[i].Suite {
			t.Errorf("case %d: suites %q do not end in its suite %q", i, got[i].Suites, got[i].Suite)
		}
		got[i].File, got[i].Suites = "", nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("junitFailures:\n got %+v\nwant %+v", got, want)
	}
	for i, f := range files {
		if !strings.HasPrefix(f, "$DIR/fail/") || !strings.HasSuffix(f, ".php") {
			t.Errorf("case %d: file %q, want the test's file", i, f)
		}
	}
}

// A record that is not a whole JUnit document is refused, never read in
// part: a record cut short names only the failures before the cut.
func TestJUnitFailuresRefusesMalformedXML(t *testing.T) {
	whole := hookedRecord(t, "phpunit-hook-fail.txt")
	cut := whole[:strings.Index(whole, "testBoom")]
	for name, body := range map[string]string{
		"cut short":        cut,
		"cut after a case": whole[:strings.LastIndex(whole, "</testcase>")+len("</testcase>")],
		"mismatched tags":  `<testsuites><testsuite name="a"><testcase name="t" class="C"><failure>x</error></testcase></testsuite></testsuites>`,
		"not junit":        `<html><body>FTest::testAdds</body></html>`,
		"empty":            "",
		"text only":        "FTest::testAdds",
		"bad entity":       `<testsuites><testsuite name="a &bogus;"/></testsuites>`,
		"two roots, cut":   `<testsuites></testsuites><testsuites><testsuite name="a">`,
		"two whole roots":  `<testsuites><testsuite name="a"><testcase name="t" class="C"><failure/></testcase></testsuite></testsuites><testsuites/>`,
	} {
		if got, err := junitFailures(body); err == nil {
			t.Errorf("%s: read %+v, want an error", name, got)
		}
	}
	// A whole record that names no failure is an answer: none.
	if got, err := junitFailures(`<?xml version="1.0"?><testsuites><testsuite name="a"><testcase name="t" class="C"/></testsuite></testsuites>`); err != nil || len(got) != 0 {
		t.Errorf("a passing record: %+v, %v; want none and no error", got, err)
	}
}

// The ids PHPUnit's record gives are the ids its printed lines give for the
// SAME run, data sets included (the printed arguments dropped, phpDataSetID),
// errors first as PHPUnit prints them, so FirstFailure names the same test
// with the hook and without it. Scoped to this captured run: the printed
// reader also takes other numbered sections (risky tests, warnings), which
// a record does not list as failing.
func TestPHPUnitRecordIDsEqualPrintedIDs(t *testing.T) {
	out := readFixture(t, "phpunit-hook-fail.txt")
	recorded, ok := phpunitRecordedFailures(out)
	if !ok {
		t.Fatal("no record read from the hooked capture")
	}
	printed := phpFailures(printedPart(t, "phpunit-hook-fail.txt"))
	if len(printed) != 9 || !slices.Equal(recorded, printed) {
		t.Fatalf("record ids %q\nprinted ids %q", recorded, printed)
	}
	if got := phpFailures(out); !slices.Equal(got, recorded) {
		t.Errorf("phpFailures on the hooked run = %q, want the record's %q", got, recorded)
	}
	if a, b := phpFirstFailure([]byte(out)), phpFirstFailure([]byte(printedPart(t, "phpunit-hook-fail.txt"))); a != b || a != "FTest::testBoom" {
		t.Errorf("FirstFailure: hooked %q, printed %q, want FTest::testBoom for both", a, b)
	}
}

// Under a phpunit.xml with testdox="true" PHPUnit prints no `N)` line, so
// the printed reader names nothing; the record still names every failure
// (PHPUnit 11.5.57, report-phpunit-testdox-hooked.txt).
func TestPHPUnitRecordNamesFailuresTestdoxHides(t *testing.T) {
	const fixture = "report-phpunit-testdox-hooked.txt"
	if got := phpFailures(printedPart(t, fixture)); len(got) != 0 {
		t.Fatalf("printed lines under testdox name %q; the fixture no longer shows what the record is for", got)
	}
	want := []string{"FTest::testBoom", "FTest::testAdds", `FTest::testData with data set "two"`}
	if got := phpFailures(readFixture(t, fixture)); !slices.Equal(got, want) {
		t.Errorf("phpFailures = %q, want %q", got, want)
	}
}

// Without a complete, well-formed JUnit block the readers read PHPUnit's
// printed lines, as they did before the hook: no block, a block cut short,
// a block whose XML is broken, and a block of another format.
func TestPHPUnitFallsBackToPrintedLinesWithoutARecord(t *testing.T) {
	printedWant := []string{"FTest::testBoom", "FTest::testAdds", `FTest::testData with data set "two"`}
	if got := phpFailures(readFixture(t, "phpunit-fail.txt")); !slices.Equal(got, printedWant) {
		t.Errorf("no block: %q, want %q", got, printedWant)
	}
	hooked := readFixture(t, "phpunit-hook-fail.txt")
	printed := phpFailures(printedPart(t, "phpunit-hook-fail.txt"))
	end := strings.LastIndex(hooked, recordEnd)
	for name, out := range map[string]string{
		"block cut short": hooked[:end],
		"broken xml":      strings.Replace(hooked, "</testsuites>", "</testsuite>", 1),
		"another format":  strings.Replace(hooked, recordBegin+" junit", recordBegin+" jest-json", 1),
		"no class":        strings.ReplaceAll(hooked, ` class="FTest"`, ""),
	} {
		if _, ok := phpunitRecordedFailures(out); ok {
			t.Errorf("%s: a record was read", name)
		}
		if got := phpFailures(out); !slices.Equal(got, printed) {
			t.Errorf("%s: %q, want the printed lines' %q", name, got, printed)
		}
	}
}

// The PHPUnit row's graded command is the record wrapper with
// `--log-junit <token>` after PHPUnit's options; the capture script runs
// exactly that command, taken from the row through scripts/reportcmd and
// never copied, and its capture is that wrapper's output.
func TestPHPUnitHookCaptureIsTheRowsCommand(t *testing.T) {
	p, _ := ByName("php")
	operator := []string{"vendor/bin/phpunit", "fail/tests/FTest.php", "fail/datasets/DTest.php", "fail/ns/NTest.php"}
	got, ok := ReportCmd(p, Cmd(operator), operator)
	want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJUnit, "null", "", "", ""}, append(slices.Clone(operator), "--log-junit", recordToken)...)
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("ReportCmd(%q) = %q ok=%v, want %q", operator, got, ok, want)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, line := range []string{
		"phphook PHPHOOK " + strings.Join(operator, " "),
		"phphook PHPHOOKTESTDOX vendor/bin/phpunit -c report/phpunit/phpunit.xml fail/tests/FTest.php",
		`go run ./scripts/reportcmd php "$@"`,
		`fails phpunit-hook-fail.txt "${PHPHOOK[@]}"`,
		`fails report-phpunit-testdox-hooked.txt "${PHPHOOKTESTDOX[@]}"`,
		"fails phpunit-hook-unhooked.txt " + strings.Join(operator, " "),
		"phphook PHPHOOKEDGE vendor/bin/phpunit fail/tests/FTest.php fail/edge/SetupTest.php fail/edge/ProviderTest.php fail/edge/RiskyTest.php",
		`fails phpunit-hook-edge.txt "${PHPHOOKEDGE[@]}"`,
		`want phpunit-operator-junit-not-wrapped "1" "$(cd "$REPO" && go run ./scripts/reportcmd php vendor/bin/phpunit --log-junit operator-junit.xml fail/tests/FTest.php >/dev/null 2>&1; echo $?)"`,
		"phphook PHPHOOKPASS vendor/bin/phpunit tests/CalcTest.php",
		`want phpunit-hook-exit-pass "0 0" "$(vendor/bin/phpunit tests/CalcTest.php >/dev/null 2>&1; echo -n "$? "; "${PHPHOOKPASS[@]}" >/dev/null 2>&1; echo $?)"`,
		`want phpunit-hook-fallback-exit 0 "$(TMPDIR="$DIR/no-such-dir" "${PHPHOOKPASS[@]}" >/dev/null 2>&1; echo $?)"`,
		`want phpunit-config-junit-hooked absent "$(junit_written report/phpunit-junit/project-junit.xml "${PHPHOOKCFGLOG[@]}")"`,
		"phphook PHPHOOKCFGLOG vendor/bin/phpunit -c report/phpunit-junit/phpunit.xml fail/tests/FTest.php",
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, f := range []string{"phpunit-hook-fail.txt", "report-phpunit-testdox-hooked.txt", "phpunit-hook-edge.txt"} {
		if out := readFixture(t, f); !strings.Contains(out, "PHPUnit 11.5.57") || !strings.Contains(out, "\n"+recordEnd) {
			t.Errorf("%s: not a PHPUnit 11.5.57 run with a complete record block", f)
		}
	}
}

// The scan line says what corral adds to a PHPUnit run when it wraps one,
// and nothing for a command it does not.
func TestRunDisclosuresSayWhenThePHPUnitHookWraps(t *testing.T) {
	p, _ := ByName("php")
	const hook = "phpunit: corral runs every graded run through `sh -c` and adds `--log-junit <a temporary file>`"
	has := func(argv []string) bool {
		return slices.ContainsFunc(RunDisclosures(p, t.TempDir(), Cmd(argv)), func(l string) bool { return strings.HasPrefix(l, hook) })
	}
	for _, argv := range [][]string{
		{"vendor/bin/phpunit"},
		{"php", "vendor/bin/phpunit", "-c", "phpunit.xml", "tests"},
	} {
		if !has(argv) {
			t.Errorf("%q: no disclosure of the JUnit record corral adds", argv)
		}
	}
	// A command with a --log-junit of its own is not wrapped, as jest's
	// --outputFile, swift's --xunit-output and nextest's
	// --tool-config-file are not: PHPUnit writes one JUnit file, the last
	// named, so the hook's would replace the operator's (measured on
	// 11.5.57, phpunit-operator-junit-*).
	for _, argv := range [][]string{
		{"sh", "-c", "vendor/bin/phpunit"},
		{"vendor/bin/phpunit", "--log-junit", "theirs.xml"},
		{"vendor/bin/phpunit", "--log-junit=theirs.xml", "tests"},
	} {
		if got, wrapped := ReportCmd(p, Cmd(argv), argv); wrapped || !slices.Equal(got, argv) {
			t.Errorf("ReportCmd(%q) = %q wrapped=%v, want it unchanged", argv, got, wrapped)
		}
		if has(argv) {
			t.Errorf("%q: the hook is disclosed though corral does not wrap it", argv)
		}
	}
}

// What PHPUnit 11.5.57's JUnit record leaves out, captured under the hook
// (capture-runner-fixtures.sh phpunit-hook-edge.txt): a setUpBeforeClass
// that throws (printed `N) SetupTest`, in the errors) and a data provider
// that throws (printed `N) ProviderTest::testP`, a "PHPUnit error") are not
// in the record at all; a risky test (no assertion, PHPUnit 11's default
// strictness) is printed in its own numbered section and is a plain,
// passing testcase in the record. The readers take the record's failures
// first and then every name the printed lines give that the record does not,
// so the hook never names fewer tests than the printed lines did.
func TestPHPUnitReadersTakeWhatTheRecordOmits(t *testing.T) {
	const fixture = "phpunit-hook-edge.txt"
	out := readFixture(t, fixture)
	recorded, ok := phpunitRecordedFailures(out)
	if want := []string{"FTest::testBoom", "FTest::testAdds", `FTest::testData with data set "two"`}; !ok || !slices.Equal(recorded, want) {
		t.Fatalf("record: %q ok=%v, want %q", recorded, ok, want)
	}
	cases, err := junitFailures(hookedRecord(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Class == "RiskyTest" || c.Class == "SetupTest" || c.Class == "ProviderTest" {
			t.Errorf("the record lists %+v as failing; the fixture no longer shows what it omits", c)
		}
	}
	if body := hookedRecord(t, fixture); !strings.Contains(body, `name="testNothing"`) {
		t.Error("the record has no testcase for the risky test")
	}
	printed := phpFailures(printedPart(t, fixture))
	if want := []string{"ProviderTest::testP", "FTest::testBoom", "SetupTest", "FTest::testAdds", `FTest::testData with data set "two"`, "RiskyTest::testNothing"}; !slices.Equal(printed, want) {
		t.Fatalf("printed: %q, want %q", printed, want)
	}
	want := []string{"FTest::testBoom", "FTest::testAdds", `FTest::testData with data set "two"`, "ProviderTest::testP", "SetupTest", "RiskyTest::testNothing"}
	if got := phpFailures(out); !slices.Equal(got, want) {
		t.Errorf("phpFailures = %q, want %q", got, want)
	}
	if got := phpFirstFailure([]byte(out)); got != "FTest::testBoom" {
		t.Errorf("phpFirstFailure = %q, want the record's first, FTest::testBoom", got)
	}
}

// The hook changes nothing PHPUnit prints: the hooked capture, up to its
// record block, is the unhooked run of the same files (both PHPUnit
// 11.5.57, the Time line removed from both by the capture script).
func TestPHPUnitHookLeavesThePrintedOutputAlone(t *testing.T) {
	hooked := strings.TrimRight(printedPart(t, "phpunit-hook-fail.txt"), "\n")
	plain := strings.TrimRight(readFixture(t, "phpunit-hook-unhooked.txt"), "\n")
	if hooked != plain {
		t.Errorf("printed output differs with the hook:\n--- hooked\n%s\n--- unhooked\n%s", hooked, plain)
	}
}

// A line inside the record that looks like a printed failure (a failure
// message a test wrote, carried in the JUnit body) is not read as one.
func TestPHPUnitPrintedReaderSkipsTheRecordBlock(t *testing.T) {
	out := "There was 1 failure:\n\n1) FTest::testAdds\nboom\n\n" + recordBegin + " junit\n" +
		`<testsuites><testsuite name="FTest"><testcase name="testAdds" class="FTest"><failure>FTest::testAdds` + "\n2) Fake::fromTheMessage\n" + `</failure></testcase></testsuite></testsuites>` + "\n" + recordEnd + "\n"
	if got := phpFailures(out); !slices.Equal(got, []string{"FTest::testAdds"}) {
		t.Errorf("phpFailures = %q, want only FTest::testAdds", got)
	}
	if got := PrintedOutput(out); strings.Contains(got, recordBegin) || strings.Contains(got, "Fake") || !strings.Contains(got, "1) FTest::testAdds") {
		t.Errorf("PrintedOutput left %q", got)
	}
}
