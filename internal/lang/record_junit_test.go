// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
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
		got[i].File = ""
	}
	if !slices.Equal(got, want) {
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
	want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJUnit}, append(slices.Clone(operator), "--log-junit", recordToken)...)
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
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, f := range []string{"phpunit-hook-fail.txt", "report-phpunit-testdox-hooked.txt"} {
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
		{"vendor/bin/phpunit", "--log-junit", "theirs.xml"},
	} {
		if !has(argv) {
			t.Errorf("%q: no disclosure of the JUnit record corral adds", argv)
		}
	}
	for _, argv := range [][]string{
		{"sh", "-c", "vendor/bin/phpunit"},
	} {
		if has(argv) {
			t.Errorf("%q: the hook is disclosed though corral does not wrap it", argv)
		}
	}
}
