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

// jestHookWords are the words the jest row's hook adds after its report
// flag: jest's JSON record of the run, written to the record file.
var jestHookWords = []string{"--json", "--outputFile=" + recordToken}

// jestHookCaptureFiles are the files the hooked jest captures run: a failing
// file, the edge shapes (fail/jest-edge) and a file that fails to load.
var jestHookCaptureFiles = []string{"fail/f.jest.test.js", "fail/jest-edge/e.jest.test.js", "fail/broken.jest.test.js"}

// The hook's two words go after jest's options, after the row's
// --reporters=default, and its fallback points the record at /dev/null
// (jest writes its JSON there and passes, capture-runner-fixtures.sh
// jest-hook-fallback-exit). A command the row derives (a deselection, a
// killer command) is hooked the same way.
func TestJestHookWrapsAJestRun(t *testing.T) {
	p, _ := ByName("javascript")
	for _, operator := range [][]string{
		{"npx", "jest", "fail/f.jest.test.js"},
		{"node", "./node_modules/jest/bin/jest.js", "--ci", "src"},
		{"jest"},
	} {
		got, ok := ReportCmd(p, Cmd(operator), operator)
		inner := append(append(slices.Clone(operator), "--reporters=default"), jestHookWords...)
		want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJestJSON, "null", "", "", ""}, inner...)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want the record wrapper around %q", operator, got[min(len(got), 9):], ok, inner)
		}
	}
	// The operator's own --reporters=default last: the flag is not added
	// twice, and the hook still follows it.
	own := []string{"npx", "jest", "a.test.js", "--reporters=default"}
	if got, ok := ReportCmd(p, Cmd(own), own); !ok || !slices.Equal(got[9:], append(slices.Clone(own), jestHookWords...)) {
		t.Errorf("ReportCmd(%q) = %q ok=%v", own, got, ok)
	}
	for name, derived := range map[string]func() ([]string, bool){
		"deselect": func() ([]string, bool) {
			return jestDeselectCmd([]string{"npx", "jest", "a.test.js"}, []string{"Calc::adds"})
		},
		"killer": func() ([]string, bool) { return jestKillerCmd([]string{"npx", "jest", "a.test.js"}, "Calc::adds") },
	} {
		cmd, ok := derived()
		if !ok {
			t.Fatalf("%s: no command", name)
		}
		got, ok := ReportCmd(p, Cmd(cmd), cmd)
		if !ok || !slices.Equal(got[len(got)-2:], jestHookWords) || got[4] != recordJestJSON {
			t.Errorf("%s command graded as %q ok=%v, want it hooked", name, got, ok)
		}
	}
}

// A command that names jest's JSON output itself is left alone: a later
// --outputFile would move the operator's own JSON (to stdout, or to their
// file) into corral's record. Not disclosed, since nothing is added.
func TestJestHookDeclinesACommandNamingItsOwnJSON(t *testing.T) {
	p, _ := ByName("javascript")
	const hook = "jest: corral runs every graded run through `sh -c`"
	for name, argv := range map[string][]string{
		"--json":         {"npx", "jest", "--json", "a.test.js"},
		"--json=true":    {"npx", "jest", "--json=true", "a.test.js"},
		"--no-json":      {"npx", "jest", "--no-json", "a.test.js"},
		"--outputFile=x": {"npx", "jest", "--outputFile=x.json", "a.test.js"},
		"--outputFile x": {"npx", "jest", "--outputFile", "x.json", "a.test.js"},
		"--output-file":  {"npx", "jest", "--output-file=x.json", "a.test.js"},
		"--json last":    {"npx", "jest", "a.test.js", "--json"},
	} {
		got, ok := ReportCmd(p, Cmd(argv), argv)
		if ok && slices.Contains(got, recordScript) {
			t.Errorf("%s: ReportCmd(%q) wrapped it: %q", name, argv, got)
		}
		if slices.ContainsFunc(RunDisclosures(p, t.TempDir(), Cmd(argv)), func(l string) bool { return strings.HasPrefix(l, hook) }) {
			t.Errorf("%s: the hook is disclosed though corral does not wrap it", name)
		}
	}
}

// The scan line says what corral adds to a jest run when it wraps one,
// and the one line it adds to what jest prints.
func TestRunDisclosuresSayWhenTheJestHookWraps(t *testing.T) {
	p, _ := ByName("typescript")
	const hook = "jest: corral runs every graded run through `sh -c` and adds `--json --outputFile=<a temporary file>`"
	got := RunDisclosures(p, t.TempDir(), Cmd([]string{"npx", "jest", "a.test.ts"}))
	if !slices.ContainsFunc(got, func(l string) bool {
		return strings.HasPrefix(l, hook) && strings.Contains(l, "`Test results written to: <that file>`")
	}) {
		t.Errorf("RunDisclosures = %q, want the hook's sentence", got)
	}
}

// The ids jest's JSON record gives are the ids the printed `●` headings
// give, on the same run (jest 30.5.2, jest-hook-fail.txt): the ancestor
// titles and the title joined by "::". The one difference is a title that
// holds jest's printed separator " › " (fail/jest-edge `Sep` › `a › b`),
// which the printed reader splits wrongly (`Sep::a::b`) and the record
// keeps whole (`Sep::a › b`); jestFailedTests names it once, as the record
// does, and adds no printed name for it.
//
// Neither names a test whose afterAll hook failed (jest prints it as "Test
// suite failed to run" and records the test as passed), a file that failed
// to load, a todo, or a test.failing that failed as expected. A
// beforeAll that throws fails its tests in both, and a test.failing that
// passed is a failure in both.
func TestJestRecordIDsEqualPrintedIDs(t *testing.T) {
	out := readFixture(t, "jest-hook-fail.txt")
	recorded, ok := jestRecordedFailures(out)
	if !ok {
		t.Fatal("no record read")
	}
	want := []string{"Calc::adds (a+b)", "Calc::neg::adds", "top level", "Sep::a › b", "BeforeAll::one", "passes unexpectedly", "each 2"}
	sorted := func(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }
	if !slices.Equal(sorted(recorded), sorted(want)) {
		t.Errorf("record = %q, want %q in some order", recorded, want)
	}
	printed := jestFailures(PrintedOutput(out))
	asPrinted := make([]string, len(recorded))
	for i, id := range recorded {
		asPrinted[i] = strings.ReplaceAll(id, " › ", "::")
	}
	if !slices.Equal(printed, asPrinted) {
		t.Errorf("printed %q, want the record's ids as the printed headings split them, in its order: %q", printed, asPrinted)
	}
	if got := jestFailedTests(out); !slices.Equal(got, recorded) {
		t.Errorf("jestFailedTests = %q, want the record's %q and no printed name added", got, recorded)
	}
	if got := firstOf(jestFailedTests)([]byte(out)); got != recorded[0] {
		t.Errorf("first failure = %q, want %q", got, recorded[0])
	}
	// The id the record gives a title holding " › " is one the row's
	// killer command takes: fullName joins the titles with a space, which is
	// jest's own full name for the test, and that pattern runs the one test
	// (capture-runner-fixtures.sh jest-killer-separator-title).
	cmd, ok := jestKillerCmd([]string{"npx", "jest", "e.jest.test.js"}, "Sep::a › b")
	if !ok || cmd[len(cmd)-1] != `^Sep a › b$` {
		t.Errorf("killer command for the record's id = %q", cmd)
	}
}

// A printed name the record does not hold is added after the record's (the
// union every record-file row reads by), and with no record to trust the
// printed headings are the whole answer: no block, a cut block, a block of
// another format, JSON that is not well-formed or not jest's, a failing
// test with no title. The fallback (the hook could not make its directory,
// so jest wrote its JSON to /dev/null) prints no block and passes.
func TestJestFallsBackToPrintedLinesWithoutARecord(t *testing.T) {
	out := readFixture(t, "jest-hook-fail.txt")
	printed := jestFailures(PrintedOutput(out))
	begin := strings.Index(out, recordBegin)
	for name, o := range map[string]string{
		"no block":    out[:begin],
		"cut short":   out[:begin+200],
		"junit":       out[:begin] + recordBegin + " junit\n<testsuites/>\n" + recordEnd + "\n",
		"broken JSON": out[:begin] + recordBegin + " jest-json\n{\"testResults\":[\n" + recordEnd + "\n",
		"not jest's":  out[:begin] + recordBegin + " jest-json\n{\"examples\":[]}\n" + recordEnd + "\n",
		"two values":  out[:begin] + recordBegin + " jest-json\n{\"testResults\":[]}{}\n" + recordEnd + "\n",
		"no title":    out[:begin] + recordBegin + " jest-json\n{\"testResults\":[{\"assertionResults\":[{\"ancestorTitles\":[],\"status\":\"failed\"}]}]}\n" + recordEnd + "\n",
	} {
		if _, ok := jestRecordedFailures(o); ok {
			t.Errorf("%s: a record was read", name)
		}
		if got := jestFailedTests(o); !slices.Equal(got, printed) {
			t.Errorf("%s: jestFailedTests = %q, want the printed %q", name, got, printed)
		}
	}
	union := out[:begin] + recordBegin + " jest-json\n" +
		`{"testResults":[{"assertionResults":[{"ancestorTitles":["Sep"],"title":"a › b","status":"failed"},{"ancestorTitles":[],"title":"ok","status":"passed"}]}]}` +
		"\n" + recordEnd + "\n"
	got := jestFailedTests(union)
	if got[0] != "Sep::a › b" || slices.Contains(got, "Sep::a::b") || !slices.Contains(got, "top level") {
		t.Errorf("jestFailedTests = %q, want the record's id first, then the printed names it lacks", got)
	}
}

// jestTestDuration is the time jest prints after a test that took a
// millisecond or more, which differs from run to run.
var jestTestDuration = regexp.MustCompile(` \(\d+ ms\)`)

// jest prints what it printed without the hook, plus one line naming the
// record file (capture-runner-fixtures.sh writes it as <record>, since the
// file's name is a fresh temporary one), on a failing run and a passing one;
// each test's printed duration aside.
func TestJestHookLeavesThePrintedOutputAlone(t *testing.T) {
	for hooked, plain := range map[string]string{
		"jest-hook-fail.txt": "jest-hook-unhooked.txt",
		"jest-hook-pass.txt": "jest-hook-unhooked-pass.txt",
	} {
		h := printedPart(t, hooked)
		if !strings.Contains(h, "\nTest results written to: <record>\n") {
			t.Errorf("%s: no `Test results written to` line", hooked)
		}
		h = jestTestDuration.ReplaceAllString(strings.TrimRight(strings.Replace(h, "Test results written to: <record>\n", "", 1), "\n"), "")
		u := jestTestDuration.ReplaceAllString(strings.TrimRight(readFixture(t, plain), "\n"), "")
		if h != u {
			t.Errorf("%s: printed output differs with the hook:\n--- hooked\n%s\n--- unhooked\n%s", hooked, h, u)
		}
	}
}

// The hooked captures are made with the command the row builds, taken from
// scripts/reportcmd, and the script holds the checks this file's comments
// cite, the never-write-a-project-file ones among them: under each jest
// configuration that asks for a report file of its own (jest.config.js
// `json`/`outputFile`, a configured reporter with an `outputFile` option, a
// package.json `jest` block, a testResultsProcessor), the hooked run writes
// no project file the run without the hook does not write.
func TestJestHookCaptureIsTheRowsCommand(t *testing.T) {
	p, _ := ByName("javascript")
	operator := append([]string{"npx", "jest", "-i"}, jestHookCaptureFiles...)
	got, ok := ReportCmd(p, Cmd(operator), operator)
	want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJestJSON, "null", "", "", ""}, append(append(slices.Clone(operator), "--reporters=default"), jestHookWords...)...)
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("ReportCmd(%q) = %q ok=%v", operator, got, ok)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, line := range []string{
		"JESTFILES=(" + strings.Join(jestHookCaptureFiles, " ") + ")",
		`reporthook JESTHOOK javascript npx jest -i "${JESTFILES[@]}"`,
		`jestfails jest-hook-fail.txt "${JESTHOOK[@]}"`,
		`fails jest-hook-unhooked.txt npx jest -i "${JESTFILES[@]}" --reporters=default`,
		"reporthook JESTHOOKPASS javascript npx jest calc.jest.test.js",
		`jestfails jest-hook-pass.txt "${JESTHOOKPASS[@]}"`,
		"fails jest-hook-unhooked-pass.txt npx jest calc.jest.test.js --reporters=default",
		`want jest-hook-exit-fail "1 1"`,
		`want jest-hook-exit-pass "0 0"`,
		"want jest-hook-fallback-exit 0",
		"want jest-wrote-config-flagged neither",
		"want jest-wrote-config-hooked ours",
		"want jest-wrote-reporter-flagged neither",
		"want jest-wrote-reporter-hooked ours",
		"want jest-wrote-package-flagged neither",
		"want jest-wrote-package-hooked ours",
		"want jest-wrote-processor-flagged theirs",
		"want jest-wrote-processor-hooked ours+theirs",
		`want jest-killer-separator-title "1 failed, 7 skipped, 8 total"`,
		`want jest-killer-separator-title-printed-id "8 skipped, 8 total"`,
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, f := range []string{"jest-hook-fail.txt", "jest-hook-pass.txt"} {
		if out := readFixture(t, f); !strings.Contains(out, "\n"+recordEnd) || strings.Contains(out, `"startTime":1`) {
			t.Errorf("%s: no complete record block, or its clock was kept", f)
		}
	}
}

// jestFailedUnits reads the printed `FAIL <file>` lines only, like every
// other reader of printed lines: a line inside the record block (a record
// is the runner's or a test's text) is never read as one jest printed.
func TestJestFailedUnitsSkipsTheRecordBlock(t *testing.T) {
	out := "FAIL tests/a.test.js\n  ● adds\n\ncorral-record-begin jest-json\nFAIL tests/b.test.js\ncorral-record-end\n"
	if got := jestFailedUnits(out); !slices.Equal(got, []string{"tests/a.test.js"}) {
		t.Errorf("jestFailedUnits = %q, want the printed file alone", got)
	}
}
