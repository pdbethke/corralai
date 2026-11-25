// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// nodeHookWords are the words the node --test row's hook adds after
// `--test`: tap on stdout, as node 22 prints off a TTY, and junit to the
// record, each reporter with its destination.
var nodeHookWords = []string{
	"--test-reporter=tap", "--test-reporter-destination=stdout",
	"--test-reporter=junit", "--test-reporter-destination=" + recordToken,
}

// The hook goes right after `--test`, before the first file (node hands a
// word after one to the test files), and its fallback leaves all four words
// out. A command the row derives (a deselection) is hooked the
// same way.
func TestNodeHookWrapsANodeTestRun(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "")
	p, _ := ByName("javascript")
	for _, operator := range [][]string{
		{"node", "--test", "fail/f.node.test.js"},
		{"node", "--test"},
		{"/usr/bin/node", "--import", "tsx", "--test", "--test-concurrency=1", "a.test.ts"},
		{"node", "--max-old-space-size=4096", "--test", "--test-name-pattern", "x", "a.test.js", "b.test.js"},
	} {
		at := slices.Index(operator, "--test") + 1
		got, ok := ReportCmd(p, Cmd(operator), operator)
		inner := append(append(slices.Clone(operator[:at]), nodeHookWords...), operator[at:]...)
		want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJUnit, "drop:" + strconv.Itoa(at) + ":4", ""}, inner...)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want the record wrapper around %q", operator, got[min(len(got), 5):], ok, inner)
		}
	}
	derived, ok := nodeDeselectCmd([]string{"node", "--test", "a.test.js"}, []string{"Calc::adds"})
	if !ok {
		t.Fatal("no deselect command")
	}
	if got, ok := ReportCmd(p, Cmd(derived), derived); !ok || !slices.Equal(got[7:13], append([]string{"node", "--test"}, nodeHookWords...)) {
		t.Errorf("deselect command graded as %q ok=%v, want it hooked after --test", got, ok)
	}
}

// What the hook leaves alone, and why. node pairs each --test-reporter with
// a --test-reporter-destination and refuses a run where they differ in
// number (capture-runner-fixtures.sh node-unpaired-reporter-fails-a-pass),
// so a command naming a reporter of its own, one in NODE_OPTIONS (which the
// workspace substrate's runs inherit), or a config file that may name one,
// would fail with the hook's two pairs added. A word before `--test` that is
// not an option node lists, or its value, may be the script node runs, which
// would receive the hook's words. None of these is disclosed.
func TestNodeHookDeclinesWhatItWouldChange(t *testing.T) {
	p, _ := ByName("javascript")
	const hook = "node --test: corral runs every graded run through `sh -c`"
	check := func(name string, argv []string) {
		t.Helper()
		if got, ok := ReportCmd(p, Cmd(argv), argv); ok || !slices.Equal(got, argv) {
			t.Errorf("%s: ReportCmd(%q) = %q ok=%v, want it unchanged", name, argv, got, ok)
		}
		if slices.ContainsFunc(RunDisclosures(p, t.TempDir(), Cmd(argv)), func(l string) bool { return strings.HasPrefix(l, hook) }) {
			t.Errorf("%s: the hook is disclosed though corral does not wrap it", name)
		}
	}
	t.Setenv("NODE_OPTIONS", "")
	for name, argv := range map[string][]string{
		"its own reporter":          {"node", "--test", "--test-reporter=spec", "a.test.js"},
		"its own reporter, spaced":  {"node", "--test", "--test-reporter", "dot", "a.test.js"},
		"its own destination":       {"node", "--test", "--test-reporter-destination=out.txt", "a.test.js"},
		"a config file":             {"node", "--experimental-config-file=node.config.json", "--test"},
		"the default config file":   {"node", "--experimental-default-config-file", "--test"},
		"a script before --test":    {"node", "run.js", "--test"},
		"an unknown option's value": {"node", "--no-such-option", "x", "--test"},
		"shell-wrapped":             {"sh", "-c", "node --test"},
		"no --test":                 {"node", "a.test.js"},
	} {
		check(name, argv)
	}
	for _, env := range []string{"--test-reporter=dot", "--max-old-space-size=4096 --test-reporter-destination=x", "--experimental-config-file=c.json"} {
		t.Setenv("NODE_OPTIONS", env)
		check("NODE_OPTIONS="+env, []string{"node", "--test", "a.test.js"})
	}
	t.Setenv("NODE_OPTIONS", "--max-old-space-size=4096")
	if _, ok := ReportCmd(p, Cmd([]string{"node", "--test"}), []string{"node", "--test"}); !ok {
		t.Error("a NODE_OPTIONS naming no reporter declined the hook")
	}
}

// The scan line says what corral adds to a node --test run when it wraps one.
func TestRunDisclosuresSayWhenTheNodeHookWraps(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "")
	p, _ := ByName("typescript")
	const hook = "node --test: corral runs every graded run through `sh -c` and adds `--test-reporter=tap --test-reporter-destination=stdout --test-reporter=junit --test-reporter-destination=<a temporary file>` after `--test`"
	got := RunDisclosures(p, t.TempDir(), Cmd([]string{"node", "--test", "a.test.ts"}))
	if !slices.ContainsFunc(got, func(l string) bool {
		return strings.HasPrefix(l, hook) && strings.Contains(l, "node 23 and later print TAP in place of their spec report")
	}) {
		t.Errorf("RunDisclosures = %q, want the hook's sentence", got)
	}
}

// The ids node's JUnit record gives are the ids the printed report gives,
// on the same run: the record's suites and the test's name, joined by "::"
// as nodeFailures joins the TAP report's titles, a name holding " > ",
// " :: " or "#" included. Captured on node 22.23.5 (node-hook-fail.txt,
// where node prints TAP whether or not the hook is there) and node 24.22.1
// (node24-hook-fail.txt, where the hook replaces the spec report with TAP),
// over a failing file, the edge shapes (fail/node-edge: a test whose subtest
// fails, a test whose body fails after its passing subtest, a suite whose
// `before` hook throws, a failing todo) and a file that fails to load.
//
// Neither names the failing todo: node does not fail a run for it, so it
// is never what killed a mutant. The record writes it as a testcase with
// both <skipped type="todo"> and <failure>, and TAP as `not ok … # TODO`;
// node 24's spec report leaves it out of its failures. Neither names the
// test whose body failed after its subtest passed either: the record has no
// <failure> for it, and the printed reader takes a test with subtests as
// their roll-up, as it always has.
func TestNodeRecordIDsEqualPrintedIDs(t *testing.T) {
	want := []string{
		"Calc::adds (a+b)", "Calc::neg::adds", "top level",
		"parent::child fails", "hooked::a", "a > b :: c #1",
	}
	for _, fixture := range []string{"node-hook-fail.txt", "node24-hook-fail.txt"} {
		out := readFixture(t, fixture)
		recorded, ok := nodeRecordedFailures(out)
		if !ok {
			t.Fatalf("%s: no record read", fixture)
		}
		printed := nodeFailures(withoutRecordBlock(out))
		if !slices.Equal(recorded, want) || !slices.Equal(printed, want) {
			t.Errorf("%s:\nrecord  %q\nprinted %q\nwant    %q", fixture, recorded, printed, want)
		}
		if got := nodeFailedTests(out); !slices.Equal(got, want) {
			t.Errorf("%s: nodeFailedTests = %q, want %q", fixture, got, want)
		}
		if got := firstOf(nodeFailedTests)([]byte(out)); got != "Calc::adds (a+b)" {
			t.Errorf("%s: first failure = %q", fixture, got)
		}
	}
	// What node 24 printed without the hook (its spec report) names the
	// same tests: the hook changes the printed form there, not the ids.
	if got := nodeFailures(readFixture(t, "node24-hook-unhooked.txt")); !slices.Equal(got, want) {
		t.Errorf("node 24's own spec report names %q, want %q", got, want)
	}
}

// A file that fails to load is a top-level testcase in node's record, named
// after the file, and is not a test: the printed reader names nothing for it
// (its TAP entry carries exitCode), and neither does the record's reader.
// node 22.23.5 writes the file's path as given with no `file` attribute, so
// only the printed report says it is a file; node 24.22.1 also writes its
// absolute path in `file`; an absolute name (node 18 and 20) is a file too.
// A test the record's part leaves out that the printed report names is
// named all the same, from the printed report.
func TestNodeRecordLeavesOutAFileThatDidNotLoad(t *testing.T) {
	for _, fixture := range []string{"node-hook-fail.txt", "node24-hook-fail.txt"} {
		cases, err := junitFailures(hookedRecord(t, fixture))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(cases, func(c junitCase) bool { return c.Name == "broken.node.test.js" && len(c.Suites) == 0 }) {
			t.Errorf("%s: the record has no top-level testcase for the file that did not load", fixture)
		}
		if got, _ := nodeRecordedFailures(readFixture(t, fixture)); slices.Contains(got, "broken.node.test.js") {
			t.Errorf("%s: the record's reader named the file that did not load: %q", fixture, got)
		}
	}
	block := func(tc string) string {
		return "TAP version 13\n# Subtest: real\nnot ok 1 - real\n  ---\n  type: 'test'\n  ...\n\n" +
			recordBegin + " junit\n<testsuites>" + tc + "</testsuites>\n" + recordEnd + "\n"
	}
	for name, tc := range map[string]string{
		"an absolute name":         `<testcase name="/w/broken.test.js" classname="test"><failure/></testcase>`,
		"the tail of its file":     `<testcase name="t/broken.test.js" classname="test" file="/w/t/broken.test.js"><failure/></testcase>`,
		"marked a file in the TAP": `<testcase name="real" classname="test"><failure/></testcase>`,
	} {
		out := block(tc)
		if name == "marked a file in the TAP" {
			out = strings.Replace(out, "type: 'test'\n", "type: 'test'\n  exitCode: 1\n", 1)
		}
		if got, ok := nodeRecordedFailures(out); !ok || len(got) != 0 {
			t.Errorf("%s: record read as %q ok=%v, want no test", name, got, ok)
		}
	}
	// A real test named like a path is left out of the record's part and
	// named by the printed report.
	out := strings.Replace(block(`<testcase name="/abs" classname="test"><failure/></testcase>`), "real", "/abs", 2)
	if got := nodeFailedTests(out); !slices.Equal(got, []string{"/abs"}) {
		t.Errorf("nodeFailedTests = %q, want the printed report's /abs", got)
	}
}

// With no record to trust, the printed report is the whole answer: no
// block, a cut block, a record that is not well-formed, another format, a
// testcase with no name. The fallback capture (the hook could not make its
// directory, so it left its four words out) prints no block and passes.
func TestNodeFallsBackToPrintedLinesWithoutARecord(t *testing.T) {
	out := readFixture(t, "node-hook-fail.txt")
	printed := nodeFailures(withoutRecordBlock(out))
	begin := strings.Index(out, recordBegin)
	for name, o := range map[string]string{
		"no block":    out[:begin],
		"cut short":   out[:begin+600],
		"broken XML":  out[:begin] + recordBegin + " junit\n<testsuites><testcase name=\"x\">\n" + recordEnd + "\n",
		"jest's JSON": out[:begin] + recordBegin + " jest-json\n{}\n" + recordEnd + "\n",
		"no name":     out[:begin] + recordBegin + " junit\n<testsuites><testcase classname=\"test\"><failure/></testcase></testsuites>\n" + recordEnd + "\n",
	} {
		if _, ok := nodeRecordedFailures(o); ok {
			t.Errorf("%s: a record was read", name)
		}
		if got := nodeFailedTests(o); !slices.Equal(got, printed) {
			t.Errorf("%s: nodeFailedTests = %q, want the printed %q", name, got, printed)
		}
	}
	fb := readFixture(t, "node-hook-fallback.txt")
	if strings.Contains(fb, recordBegin) || !strings.Contains(fb, "leaves its record option out") || !strings.Contains(fb, "\n# fail 0\n") {
		t.Errorf("node-hook-fallback.txt: want a passing run with no block and the fallback's line:\n%s", fb)
	}
}

// node 22 prints the same report with the hook as without it, on a failing
// run and a passing one: the hook names tap on stdout, which is what node 22
// prints off a TTY.
func TestNodeHookLeavesThePrintedOutputAlone(t *testing.T) {
	for hooked, plain := range map[string]string{
		"node-hook-fail.txt": "node-hook-unhooked.txt",
		"node-hook-pass.txt": "node-hook-unhooked-pass.txt",
	} {
		h := strings.TrimRight(printedPart(t, hooked), "\n")
		u := strings.TrimRight(readFixture(t, plain), "\n")
		if h != u {
			t.Errorf("%s: printed output differs with the hook:\n--- hooked\n%s\n--- unhooked\n%s", hooked, h, u)
		}
	}
}

// The hooked captures are made with the command the row builds, taken from
// scripts/reportcmd, and the script holds the checks the comments here cite.
func TestNodeHookCaptureIsTheRowsCommand(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "")
	p, _ := ByName("javascript")
	files := []string{"fail/f.node.test.js", "fail/node-edge/e.node.test.js", "broken.node.test.js"}
	operator := append([]string{"node", "--test"}, files...)
	got, ok := ReportCmd(p, Cmd(operator), operator)
	want := append([]string{"sh", "-c", recordScript, recordArgv0, recordJUnit, "drop:2:4", "", "node", "--test"}, append(slices.Clone(nodeHookWords), files...)...)
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("ReportCmd(%q) = %q ok=%v", operator, got, ok)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, line := range []string{
		"NODEFILES=(" + strings.Join(files, " ") + ")",
		`reporthook NODEHOOK javascript node --test "${NODEFILES[@]}"`,
		`go run ./scripts/reportcmd "$@"`,
		`nodefails node-hook-fail.txt "${NODEHOOK[@]}"`,
		`fails node-hook-unhooked.txt node --test "${NODEFILES[@]}"`,
		"reporthook NODEHOOKPASS javascript node --test calc.node.test.js",
		`nodefails node-hook-pass.txt "${NODEHOOKPASS[@]}"`,
		"fails node-hook-unhooked-pass.txt node --test calc.node.test.js",
		`want node-hook-exit-fail "1 1"`,
		`want node-hook-exit-pass "0 0"`,
		"want node-hook-fallback-exit 0",
		"want node-devnull-destination-fails-a-pass 1",
		"want node-unpaired-reporter-fails-a-pass 1",
		`nodefails node24-hook-fail.txt $NODE24 "${NODEHOOK[@]}"`,
		`nodefails node24-hook-unhooked.txt $NODE24 node --test "${NODEFILES[@]}"`,
		`want node24-hook-exit-fail "1 1"`,
		`want node24-hook-exit-pass "0 0"`,
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, f := range []string{"node-hook-fail.txt", "node24-hook-fail.txt", "node-hook-pass.txt"} {
		if out := readFixture(t, f); !strings.Contains(out, "\n"+recordEnd) {
			t.Errorf("%s: no complete record block", f)
		}
	}
	if v := strings.TrimSpace(readFixture(t, "toolchain-node24-hook-version.txt")); v != "v24.22.1" {
		t.Errorf("node24 captures made with %q, want v24.22.1", v)
	}
	for _, f := range []string{"node-hook-fail.txt", "node24-hook-fail.txt", "node-hook-pass.txt"} {
		if out := readFixture(t, f); strings.Contains(out, "hostname=") || strings.Contains(out, "timestamp=") {
			t.Errorf("%s: the record's host or clock was kept", f)
		}
	}
}

// vitest has no failure hook, for a measured reason its row's Note gives:
// corral must never write into a project's file, and no command line keeps
// vitest's JUnit report out of one. A string --outputFile keeps it out of a
// config's outputFile, string or object (--outputFile.junit does not, for a
// string), but a reporter the config names with options keeps them when the
// command line names the same reporter (vitest 5.1.0).
func TestVitestHasNoFailureHookBecauseNoFlagKeepsItsReportOutOfTheProject(t *testing.T) {
	js, _ := ByName("javascript")
	r, err := RunnerByID(js, "vitest")
	if err != nil {
		t.Fatal(err)
	}
	if r.ReportHook != nil {
		t.Fatal("vitest has a failure hook: TestVitest...'s measurements say none can keep out of a project file")
	}
	if !strings.Contains(r.Note, "no failure hook") || !strings.Contains(r.Note, "`reporters: [['junit', {outputFile}]]`") {
		t.Errorf("vitest's Note does not say why it has no failure hook: %s", r.Note)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`VOURS=(--reporter=default --reporter=junit --outputFile="$DIR/vitest-ours/rec")`,
		`want vitest-outputfile-none ours "$(vitest_wrote report/vitest-outputfile/none "${VOURS[@]}")"`,
		`want vitest-outputfile-string ours "$(vitest_wrote report/vitest-outputfile/string "${VOURS[@]}")"`,
		`want vitest-outputfile-object ours "$(vitest_wrote report/vitest-outputfile/object "${VOURS[@]}")"`,
		`want vitest-outputfile-reporter-options theirs "$(vitest_wrote report/vitest-outputfile/reporter "${VOURS[@]}")"`,
		`want vitest-outputfile-junit-key-string theirs`,
		`want vitest-outputfile-reporter-options-unflagged neither "$(vitest_wrote report/vitest-outputfile/reporter --reporter=default)"`,
	} {
		if !strings.Contains(string(b), line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
}

// A failing todo is not a failure in TAP either, and a "#" a test's name
// holds is escaped by node, so it is never read as the directive.
func TestNodeTAPReaderSkipsAFailingTodo(t *testing.T) {
	out := "TAP version 13\n# Subtest: t\nnot ok 1 - t # TODO\n  ---\n  type: 'test'\n  ...\n" +
		"# Subtest: x \\# TODO\nnot ok 2 - x \\# TODO\n  ---\n  type: 'test'\n  ...\n" +
		"# Subtest: y\nnot ok 3 - y # TODO not yet\n  ---\n  type: 'test'\n  ...\n"
	if got := nodeFailures(out); !slices.Equal(got, []string{"x # TODO"}) {
		t.Errorf("nodeFailures = %q, want only the test named \"x # TODO\"", got)
	}
}
