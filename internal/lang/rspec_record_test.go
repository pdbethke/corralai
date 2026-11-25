// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rspecHookWords are the words the rspec row's hook adds after its report
// flag: a JSON formatter whose output goes to the record file. `--out`
// binds to the formatter named BEFORE it (rspec-core 3.13.6: `--out f
// --format json --format progress` wrote the progress text into f), so the
// order is load-bearing.
var rspecHookWords = []string{"--format", "json", "--out", recordToken}

// rspecHookCaptureFiles are the files the hooked rspec captures run: a
// failing file and the edge shapes (fail/rspec-edge).
var rspecHookCaptureFiles = []string{"fail/spec/f_spec.rb", "fail/rspec-edge/e_spec.rb"}

// The hook's four words go after the row's --format progress, so progress
// stays on stdout and json goes to the record; its fallback points the
// record at /dev/null (rspec writes its JSON there and passes,
// capture-runner-fixtures.sh rspec-hook-fallback-exit). A command the row
// derives is hooked the same way.
func TestRSpecHookWrapsAnRSpecRun(t *testing.T) {
	p, _ := ByName("ruby")
	for _, operator := range [][]string{
		{"rspec", "fail/spec/f_spec.rb"},
		{"bundle", "exec", "rspec", "--fail-fast", "spec"},
		{"rspec", "--format", "json", "--out", "theirs.json", "spec"},
	} {
		got, ok := ReportCmd(p, Cmd(operator), operator)
		inner := append(append(slices.Clone(operator), "--format", "progress"), rspecHookWords...)
		want := append([]string{"sh", "-c", recordScript, recordArgv0, recordRSpecJSON, "null", ""}, inner...)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want the record wrapper around %q", operator, got[min(len(got), 7):], ok, inner)
		}
	}
	own := []string{"rspec", "spec", "--format", "progress"}
	if got, ok := ReportCmd(p, Cmd(own), own); !ok || !slices.Equal(got[7:], append(slices.Clone(own), rspecHookWords...)) {
		t.Errorf("ReportCmd(%q) = %q ok=%v", own, got, ok)
	}
	for name, derived := range map[string]func() ([]string, bool){
		"deselect": func() ([]string, bool) { return rspecDeselectCmd([]string{"rspec", "spec"}, []string{"Calc adds"}) },
		"killer":   func() ([]string, bool) { return rspecKillerCmd([]string{"rspec", "spec"}, "Calc adds") },
	} {
		cmd, ok := derived()
		if !ok {
			t.Fatalf("%s: no command", name)
		}
		got, ok := ReportCmd(p, Cmd(cmd), cmd)
		if !ok || !slices.Equal(got[len(got)-4:], rspecHookWords) || got[4] != recordRSpecJSON {
			t.Errorf("%s command graded as %q ok=%v, want it hooked", name, got, ok)
		}
	}
}

// The scan line says what corral adds to an rspec run when it wraps one.
func TestRunDisclosuresSayWhenTheRSpecHookWraps(t *testing.T) {
	p, _ := ByName("ruby")
	const hook = "rspec: corral runs every graded run through `sh -c` and adds `--format json --out <a temporary file>` after `--format progress`"
	got := RunDisclosures(p, t.TempDir(), Cmd([]string{"rspec", "spec"}))
	if !slices.ContainsFunc(got, func(l string) bool {
		return strings.HasPrefix(l, hook) && strings.Contains(l, "SPEC_OPTS")
	}) {
		t.Errorf("RunDisclosures = %q, want the hook's sentence", got)
	}
}

// The ids rspec's JSON record gives are the full descriptions its printed
// `Failed examples:` list gives, in the same order, on the same run
// (rspec-core 3.13.6, rspec-hook-fail.txt), over a failing file and the edge
// shapes (fail/rspec-edge): a before(:context) that raises fails its
// examples in both; an after(:context) that raises is an error outside any
// example in both, named by neither; a pending example that passes is a
// failure in both and one that fails is pending in both; aggregate_failures
// and a shared example are one failure each.
func TestRSpecRecordIDsEqualPrintedIDs(t *testing.T) {
	out := readFixture(t, "rspec-hook-fail.txt")
	want := []string{
		"Calc adds (a+b)", "Calc neg adds", "MyApp::Calc top level",
		"BeforeAll one", "Pending fixed", "Agg many", "Shared behaves like shared in shared",
	}
	recorded, ok := rspecRecordedFailures(out)
	if !ok {
		t.Fatal("no record read")
	}
	printed := rspecFailures(withoutRecordBlock(out))
	if !slices.Equal(recorded, want) || !slices.Equal(printed, want) {
		t.Errorf("\nrecord  %q\nprinted %q\nwant    %q", recorded, printed, want)
	}
	if got := rspecFailedTests(out); !slices.Equal(got, want) {
		t.Errorf("rspecFailedTests = %q, want %q", got, want)
	}
	if got := firstOf(rspecFailedTests)([]byte(out)); got != want[0] {
		t.Errorf("first failure = %q", got)
	}
}

// A printed name the record does not hold is added after the record's, and
// with no record to trust the printed list is the whole answer: no block, a
// cut block, another format, JSON that is not well-formed or not rspec's, a
// failing example with no full description. The fallback capture prints no
// block and passes.
func TestRSpecFallsBackToPrintedLinesWithoutARecord(t *testing.T) {
	out := readFixture(t, "rspec-hook-fail.txt")
	printed := rspecFailures(withoutRecordBlock(out))
	begin := strings.Index(out, recordBegin)
	for name, o := range map[string]string{
		"no block":    out[:begin],
		"cut short":   out[:begin+200],
		"jest's":      out[:begin] + recordBegin + " jest-json\n{\"examples\":[]}\n" + recordEnd + "\n",
		"broken JSON": out[:begin] + recordBegin + " rspec-json\n{\"examples\":[\n" + recordEnd + "\n",
		"not rspec's": out[:begin] + recordBegin + " rspec-json\n{\"testResults\":[]}\n" + recordEnd + "\n",
		"no name":     out[:begin] + recordBegin + " rspec-json\n{\"examples\":[{\"status\":\"failed\"}]}\n" + recordEnd + "\n",
	} {
		if _, ok := rspecRecordedFailures(o); ok {
			t.Errorf("%s: a record was read", name)
		}
		if got := rspecFailedTests(o); !slices.Equal(got, printed) {
			t.Errorf("%s: rspecFailedTests = %q, want the printed %q", name, got, printed)
		}
	}
	union := out[:begin] + recordBegin + " rspec-json\n" +
		`{"examples":[{"full_description":"Agg many","status":"failed"},{"full_description":"Calc ok","status":"passed"}]}` +
		"\n" + recordEnd + "\n"
	if got := rspecFailedTests(union); len(got) != len(printed) || got[0] != "Agg many" || !slices.Contains(got, "Calc adds (a+b)") {
		t.Errorf("rspecFailedTests = %q, want the record's id first, then every printed name it lacks", got)
	}
}

// rspec prints what it printed without the hook, on a failing run and a
// passing one: the JSON formatter writes only to the record file.
func TestRSpecHookLeavesThePrintedOutputAlone(t *testing.T) {
	for hooked, plain := range map[string]string{
		"rspec-hook-fail.txt": "rspec-hook-unhooked.txt",
		"rspec-hook-pass.txt": "rspec-hook-unhooked-pass.txt",
	} {
		h := strings.TrimRight(printedPart(t, hooked), "\n")
		u := strings.TrimRight(readFixture(t, plain), "\n")
		if h != u {
			t.Errorf("%s: printed output differs with the hook:\n--- hooked\n%s\n--- unhooked\n%s", hooked, h, u)
		}
	}
}

// The hooked captures are made with the command the row builds, taken from
// scripts/reportcmd, and the script holds the never-write-a-project-file
// checks: rspec merges its option sources by replacing the formatter list
// whole (global ~/.rspec, then .rspec, then .rspec-local, then the command
// line, then SPEC_OPTS), so a configuration file's `--format`/`--out` is
// replaced by the command line's formatters, which the row's --format
// progress already did before the hook, and a SPEC_OPTS that names one
// replaces corral's, hook included, and writes its file with or without
// the hook. A formatter a spec_helper adds, or an output_stream it sets, is
// the project's own code and writes with or without the hook, with the same
// content (compared by hand on rspec-core 3.13.6, timing lines aside).
func TestRSpecHookCaptureIsTheRowsCommand(t *testing.T) {
	p, _ := ByName("ruby")
	operator := append([]string{"rspec"}, rspecHookCaptureFiles...)
	got, ok := ReportCmd(p, Cmd(operator), operator)
	want := append([]string{"sh", "-c", recordScript, recordArgv0, recordRSpecJSON, "null", ""}, append(append(slices.Clone(operator), "--format", "progress"), rspecHookWords...)...)
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("ReportCmd(%q) = %q ok=%v", operator, got, ok)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, line := range []string{
		"RSPECFILES=(" + strings.Join(rspecHookCaptureFiles, " ") + ")",
		`reporthook RSPECHOOK ruby $RSPEC "${RSPECFILES[@]}"`,
		`rspecfails rspec-hook-fail.txt "${RSPECHOOK[@]}"`,
		`fails rspec-hook-unhooked.txt $RSPEC "${RSPECFILES[@]}" --format progress`,
		"reporthook RSPECHOOKPASS ruby $RSPEC spec/calc_spec.rb",
		`rspecfails rspec-hook-pass.txt "${RSPECHOOKPASS[@]}"`,
		"fails rspec-hook-unhooked-pass.txt $RSPEC spec/calc_spec.rb --format progress",
		`want rspec-hook-exit-fail "1 1"`,
		`want rspec-hook-exit-pass "0 0"`,
		"want rspec-hook-fallback-exit 0",
		"want rspec-wrote-dotrspec-flagged neither",
		"want rspec-wrote-dotrspec-hooked ours",
		"want rspec-wrote-dotrspec-out-flagged neither",
		"want rspec-wrote-dotrspec-out-hooked ours",
		"want rspec-wrote-dotrspec-local-flagged neither",
		"want rspec-wrote-dotrspec-local-hooked ours",
		"want rspec-wrote-home-flagged neither",
		"want rspec-wrote-home-hooked ours",
		"want rspec-wrote-helper-flagged theirs",
		"want rspec-wrote-helper-hooked ours+theirs",
		"want rspec-wrote-helper-stream-flagged theirs",
		"want rspec-wrote-helper-stream-hooked ours+theirs",
		"want rspec-wrote-specopts-flagged theirs",
		"want rspec-wrote-specopts-hooked theirs",
		"want rspec-wrote-specopts-out-flagged theirs",
		"want rspec-wrote-specopts-out-hooked theirs",
		"want rspec-wrote-operator-out-flagged theirs",
		"want rspec-wrote-operator-out-hooked ours+theirs",
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, f := range []string{"rspec-hook-fail.txt", "rspec-hook-pass.txt"} {
		if out := readFixture(t, f); !strings.Contains(out, "\n"+recordEnd) {
			t.Errorf("%s: no complete record block", f)
		}
	}
}
