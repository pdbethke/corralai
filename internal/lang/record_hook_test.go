// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/clip"
)

// fakeRecordRunner is a stand-in for a runner that writes its own record to
// a file named on its command line (PHPUnit's --log-junit, jest's
// --outputFile=): it prints every word it was given, one `arg:` line each,
// and one `ran` line, writes `<xml/>` to the path after --log or after
// --out=, and exits with the code its `exit=N` word names. `nl` makes the
// record end with a newline, `empty` leaves the file empty, `unreadable`
// takes every permission off it, and `env` prints the variables f, d, r, a,
// b and rc as the runner inherited them. --derived=P writes the record to P
// with "-st" inserted before its extension (as swift-testing 6.4.2 does with
// --xunit-output), and --both=P writes <derived/> there and <plain/> to P
// itself.
const fakeRecordRunner = `echo ran
log= code=0 nl= empty= unreadable= both=
while [ $# -gt 0 ]; do
  printf 'arg:%s\n' "$1"
  case $1 in
    --log) log=$2 ;;
    --out=*) log=${1#--out=} ;;
    --derived=*) log=${1#--derived=}; log=${log%.xml}-st.xml ;;
    --both=*) both=${1#--both=} ;;
    exit=*) code=${1#exit=} ;;
    nl) nl=1 ;;
    empty) empty=1 ;;
    unreadable) unreadable=1 ;;
    env) printf 'env:%s|%s|%s|%s|%s|%s\n' "$f" "$d" "$r" "$a" "$b" "$rc" ;;
  esac
  shift
done
if [ -n "$log" ]; then
  if [ -n "$empty" ]; then : > "$log"
  elif [ -n "$nl" ]; then printf '<xml/>\n' > "$log"
  else printf '<xml/>' > "$log"
  fi
  [ -n "$unreadable" ] && chmod 000 "$log"
fi
if [ -n "$both" ]; then
  printf '<derived/>' > "${both%.xml}-st.xml"
  printf '<plain/>' > "$both"
fi
exit "$code"
`

// withFakeRecordRow puts one python-language row named by the word
// `fakerunner` in the runner table, with a FileRecordHook built from args,
// and returns the directory holding the runner's script.
func withFakeRecordRow(t *testing.T, args ...string) string {
	t.Helper()
	return withFakeRecordRowAt(t, RecordFile{}, args...)
}

// withFakeRecordRowAt is withFakeRecordRow with the hook told where the
// runner writes its record (FileRecordHookAt).
func withFakeRecordRowAt(t *testing.T, f RecordFile, args ...string) string {
	t.Helper()
	withRunners(t, []Runner{{
		ID: "fakerunner", Name: "fakerunner", Languages: []string{"python"}, Words: []string{"fakerunner"},
		ReportHook: FileRecordHookAt(func() []string { return args }, "junit", "fakerunner writes a record", f),
	}})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fakerunner"), []byte(fakeRecordRunner), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// graded is operator as ReportCmd grades it under the fake row, which must
// be the hook's wrapper.
func graded(t *testing.T, operator []string) []string {
	t.Helper()
	p, _ := ByName("python")
	out, applied := ReportCmd(p, Cmd(operator), operator)
	if !applied || !cmdIsShellWrapped(out) {
		t.Fatalf("ReportCmd(%v) = %v applied=%v, want the record wrapper", operator, out, applied)
	}
	return out
}

// argLines is the words the fake runner printed it was given.
func argLines(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if w, ok := strings.CutPrefix(l, "arg:"); ok {
			got = append(got, w)
		}
	}
	return got
}

// The wrapper runs the command once, keeps its exit code, and prints the
// record the runner wrote, after the runner's output, as the last block.
// The record args go where the runner's options end (before its own `--`),
// the token becomes a path in a directory of the hook's own, and every other
// word reaches the runner byte for byte: spaces, quotes, `$`, a command
// substitution, an empty word, and a word that holds only part of the token.
func TestFileRecordHookWrapsAndPrintsTheRecord(t *testing.T) {
	dir := withFakeRecordRow(t, "--log", recordToken)
	words := []string{"a b", "", `it's "quoted"`, "$HOME and $(echo x) `id`", "__CORRAL_RECOR", "x__CORRAL_RECORD_", "*"}
	for _, code := range []string{"0", "1"} {
		t.Run("exit="+code, func(t *testing.T) {
			operator := append([]string{"sh", "fakerunner", "exit=" + code, "--"}, words...)
			out, got := runIn(t, dir, graded(t, operator))
			if want := map[string]int{"0": 0, "1": 1}[code]; got != want {
				t.Fatalf("exit %d, want the runner's %d:\n%s", got, want, out)
			}
			if n := strings.Count(out, "ran\n"); n != 1 {
				t.Errorf("the runner ran %d times, want once:\n%s", n, out)
			}
			args := argLines(out)
			if len(args) != 4+len(words) || args[0] != "exit="+code || args[1] != "--log" || args[3] != "--" {
				t.Fatalf("runner's words = %q, want exit=, --log <path>, --, then the operator's", args)
			}
			if p := args[2]; p == recordToken || strings.Contains(p, recordToken) || !filepath.IsAbs(p) || p == "/dev/null" {
				t.Errorf("record path = %q, want a path in the hook's own directory", p)
			}
			if !slices.Equal(args[4:], words) {
				t.Errorf("operator's words reached the runner as %q, want %q", args[4:], words)
			}
			format, body, ok := RecordBlock(out)
			if !ok || format != "junit" || body != "<xml/>" {
				t.Errorf("RecordBlock = %q, %q, %v, want junit, <xml/>, true, from:\n%s", format, body, ok, out)
			}
			if !strings.HasSuffix(out, "\n\ncorral-record-begin junit\n<xml/>\ncorral-record-end\n") {
				t.Errorf("output does not end with the block after a blank line:\n%s", out)
			}
			if _, err := os.Stat(args[2]); !os.IsNotExist(err) {
				t.Errorf("the record %s outlived the run: %v", args[2], err)
			}
		})
	}
}

// The token is substituted inside a word (`--out=__CORRAL_RECORD__`), and a
// record that already ends with a newline gains no blank line in the body.
// An empty record prints no block.
func TestFileRecordHookSubstitutesInsideAWord(t *testing.T) {
	dir := withFakeRecordRow(t, "--out="+recordToken)
	out, code := runIn(t, dir, graded(t, []string{"sh", "fakerunner", "nl"}))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	args := argLines(out)
	if len(args) != 2 || !strings.HasPrefix(args[1], "--out=/") || strings.Contains(args[1], recordToken) {
		t.Fatalf("runner's words = %q, want nl, --out=<path>", args)
	}
	if format, body, ok := RecordBlock(out); !ok || format != "junit" || body != "<xml/>" {
		t.Errorf("RecordBlock = %q, %q, %v, want junit, <xml/>, true, from:\n%s", format, body, ok, out)
	}

	out, code = runIn(t, dir, graded(t, []string{"sh", "fakerunner", "empty", "exit=3"}))
	if code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", code, out)
	}
	if strings.Contains(out, "corral-record-begin") {
		t.Errorf("an empty record printed a block:\n%s", out)
	}
}

// When the hook cannot make its temporary directory, the token becomes
// /dev/null: the command still runs once, with its own exit code, and no
// block is printed, so its failures are read from what it printed.
func TestFileRecordHookFallsBackWhenItCannotSetUp(t *testing.T) {
	dir := withFakeRecordRow(t, "--log", recordToken)
	for _, code := range []int{0, 1} {
		operator := []string{"sh", "fakerunner", "exit=" + map[int]string{0: "0", 1: "1"}[code]}
		out, got := runIn(t, dir, graded(t, operator), "TMPDIR="+filepath.Join(dir, "does-not-exist"))
		if got != code {
			t.Fatalf("exit %d, want the runner's %d:\n%s", got, code, out)
		}
		if n := strings.Count(out, "ran\n"); n != 1 {
			t.Errorf("the runner ran %d times, want once:\n%s", n, out)
		}
		if args := argLines(out); !slices.Equal(args, []string{operator[2], "--log", "/dev/null"}) {
			t.Errorf("runner's words = %q, want the record sent to /dev/null", args)
		}
		if strings.Contains(out, "corral-record-begin") {
			t.Errorf("a hook that could not be set up printed a block:\n%s", out)
		}
		if !strings.Contains(out, "could not make a temporary directory") {
			t.Errorf("the fallback is not said:\n%s", out)
		}
	}
}

// On Windows there is no POSIX shell to run the wrapper, so the row's record
// args are not added either: a token left in the command would reach the
// runner as a file name.
func TestFileRecordHookAddsNothingOnWindows(t *testing.T) {
	withFakeRecordRow(t, "--log", recordToken)
	was := reportHookGOOS
	reportHookGOOS = "windows"
	t.Cleanup(func() { reportHookGOOS = was })
	p, _ := ByName("python")
	operator := []string{"sh", "fakerunner", "tests"}
	if got, applied := ReportCmd(p, Cmd(operator), operator); applied || !slices.Equal(got, operator) {
		t.Errorf("ReportCmd on windows = %v applied=%v, want the command unchanged", got, applied)
	}
	if lines := RunDisclosures(p, t.TempDir(), Cmd(operator)); len(lines) != 0 {
		t.Errorf("disclosures on windows, where nothing changes: %v", lines)
	}
}

// Applying ReportCmd to a wrapped command changes nothing, and the hook is
// disclosed in the row's own words.
func TestFileRecordHookWrapsOnceAndSaysSo(t *testing.T) {
	withFakeRecordRow(t, "--log", recordToken)
	p, _ := ByName("python")
	once := graded(t, []string{"sh", "fakerunner"})
	if twice, applied := ReportCmd(p, Cmd(once), once); applied || !slices.Equal(twice, once) {
		t.Errorf("a second ReportCmd = %v applied=%v, want it unchanged", twice, applied)
	}
	lines := RunDisclosures(p, t.TempDir(), Cmd([]string{"sh", "fakerunner"}))
	if !slices.Equal(lines, []string{"fakerunner: fakerunner writes a record"}) {
		t.Errorf("disclosures = %v", lines)
	}
}

// RecordBlock takes the last complete block, as pytest's reader does: of two
// blocks the last; none when the last begin has no end, an earlier complete
// block included; none across an elision; a begin naming no format is not a
// begin.
func TestRecordBlockTakesTheLastCompleteBlock(t *testing.T) {
	el := clip.Elision(4096)
	cases := []struct {
		name, out, format, body string
		ok                      bool
	}{
		{"none", "ok\n", "", "", false},
		{"one", "x\n\ncorral-record-begin junit\n<a>\n  <b/>\n</a>\ncorral-record-end\n", "junit", "<a>\n  <b/>\n</a>", true},
		{"two blocks: the last", "corral-record-begin junit\n<forged/>\ncorral-record-end\nnoise\ncorral-record-begin jest-json\n{}\ncorral-record-end\n", "jest-json", "{}", true},
		{"trailing begin without end voids an earlier block", "corral-record-begin junit\n<forged/>\ncorral-record-end\ncorral-record-begin junit\n<real", "", "", false},
		{"end without begin", "<x/>\ncorral-record-end\n", "", "", false},
		{"a block split by an elision", "corral-record-begin junit\n<a>\n" + el + "\n</a>\ncorral-record-end\n", "", "", false},
		{"a block after an elision", "corral-record-begin junit\n<old/>\ncorral-record-end\n" + el + "\ncorral-record-begin junit\n<new/>\ncorral-record-end\n", "junit", "<new/>", true},
		{"a begin naming no format", "corral-record-begin\n<x/>\ncorral-record-end\n", "", "", false},
		{"CRLF", "corral-record-begin rspec-json\r\n{\"a\": 1}\r\ncorral-record-end\r\n", "rspec-json", "{\"a\": 1}", true},
		{"empty body", "corral-record-begin junit\ncorral-record-end\n", "junit", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			format, body, ok := RecordBlock(c.out)
			if format != c.format || body != c.body || ok != c.ok {
				t.Errorf("RecordBlock = %q, %q, %v, want %q, %q, %v", format, body, ok, c.format, c.body, c.ok)
			}
		})
	}
}

// pyReportBlockBefore is pyReportBlock as it was before it read its block
// through lastCompleteBlock, kept verbatim so the switch is shown to change
// no answer.
func pyReportBlockBefore(output string) (recs []pyReportRec, ok bool) {
	var cur []pyReportRec
	open := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case clip.IsElision(line):
			recs, ok, cur, open = nil, false, nil, false
		case line == pyReportBegin:
			cur, open = nil, true
		case line == pyReportEnd:
			if open {
				recs, ok, open = cur, true, false
			}
		case open:
			rest, isRec := strings.CutPrefix(line, pyReportRecord)
			if !isRec {
				continue
			}
			kind, id, found := strings.Cut(rest, " ")
			if !found || id == "" || (kind != pyReportFailed && kind != pyReportError) {
				continue
			}
			cur = append(cur, pyReportRec{kind: kind, id: id})
		}
	}
	if open {
		return nil, false
	}
	return recs, ok
}

// pytest's block reader, now on lastCompleteBlock, gives every answer it gave
// before, on a realistic run's output and on every shape the pytest reader
// tests pin (forged, split, elided, CRLF, malformed records, a begin with a
// suffix, which pytest's rule never counted as a begin).
func TestPytestBlockStillParses(t *testing.T) {
	el := clip.Elision(4096)
	run := "============================= test session starts ==============================\n" +
		"collected 4 items\n\ntests/test_x.py .FFE                                                     [100%]\n\n" +
		"FAILED tests/test_x.py::test_bad - assert 1 == 2\n= 2 failed, 1 passed, 1 error in 0.03s =\n\n" +
		"corral-report-begin\ncorral-report failed tests/test_x.py::test_bad\n" +
		"corral-report failed tests/test_x.py::test_spaced[a b]\ncorral-report error tests/test_x.py::test_setup\ncorral-report-end\n"
	outs := []string{
		run, "", "\n", "FAILED t.py::a - x\n",
		"corral-report-begin\ncorral-report failed t.py::a\n",
		"corral-report failed t.py::a\ncorral-report-end\n",
		"corral-report-begin\ncorral-report-end\n",
		"corral-report-begin\ncorral-report failed t.py::inner\ncorral-report-end\ncorral-report-begin\ncorral-report failed t.py::outer\ncorral-report-end\n",
		"corral-report-begin\r\ncorral-report passed t.py::a\r\ncorral-report failed t.py::b\r\ncorral-report-end\r\n",
		"corral-report-begin\ncorral-report failed\ncorral-report failed \ncorral-report  t.py::x\ncorral-report error t.py::e\ncorral-report-end\n",
		"corral-report-begin\ncorral-report failed t.py::a\ncorral-report-begin\ncorral-report failed t.py::b\ncorral-report-end\n",
		"corral-report-begin x\ncorral-report failed t.py::a\ncorral-report-end\n",
		"corral-report-begin\ncorral-report-begin x\ncorral-report failed t.py::a\ncorral-report-end\n",
		"noise\n" + el + "\nmore\ncorral-report-begin\ncorral-report failed t.py::real\ncorral-report-end\n",
		"corral-report-begin\ncorral-report failed t.py::a\n" + el + "\ncorral-report failed t.py::z\ncorral-report-end\n",
		"corral-report-begin\ncorral-report failed t.py::forged\ncorral-report-end\ncorral-report-begin\n" + el + "\ncorral-report failed t.py::z\ncorral-report-end\n",
		"corral-report-begin\ncorral-report failed t.py::f\ncorral-report-end\n" + el + "\n",
		"corral-report-begin\ncorral-report failed t.py::forged\ncorral-report-end\nFAILED t.py::printed - boom\ncorral-report-begin\ncorral-report failed t.py::real\n",
		"corral-report-begin\ncorral-report failed t.py::a\ncorral-report-end\ncorral-report-end\n",
	}
	for i, out := range outs {
		want, wantOK := pyReportBlockBefore(out)
		got, gotOK := pyReportBlock(out)
		if !slices.Equal(got, want) || gotOK != wantOK || (got == nil) != (want == nil) {
			t.Errorf("case %d: pyReportBlock = %v, %v; before the switch %v, %v, on:\n%s", i, got, gotOK, want, wantOK, out)
		}
	}
	recs, ok := pyReportBlock(run)
	if want := []pyReportRec{{pyReportFailed, "tests/test_x.py::test_bad"}, {pyReportFailed, "tests/test_x.py::test_spaced[a b]"}, {pyReportError, "tests/test_x.py::test_setup"}}; !ok || !slices.Equal(recs, want) {
		t.Errorf("pyReportBlock(run) = %v, %v, want %v", recs, ok, want)
	}
	if body, ok := lastCompleteBlock(run, pyReportBegin, pyReportEnd); !ok || !strings.HasPrefix(body, "corral-report failed tests/test_x.py::test_bad\n") {
		t.Errorf("lastCompleteBlock(run) = %q, %v", body, ok)
	}
}

// A record the wrapper cannot read whole (here: one it may not read at all)
// is not a complete block: the end marker is printed only when cat
// succeeded, so a failed or partial read never passes for the runner's whole
// account (an empty complete block would read as "no test failed").
func TestFileRecordHookPrintsNoCompleteBlockForAnUnreadableRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file with no permissions")
	}
	dir := withFakeRecordRow(t, "--log", recordToken)
	out, code := runIn(t, dir, graded(t, []string{"sh", "fakerunner", "unreadable", "exit=1"}))
	if code != 1 {
		t.Fatalf("exit %d, want the runner's 1:\n%s", code, out)
	}
	if strings.Contains(out, recordEnd) {
		t.Errorf("an unreadable record was closed with an end marker:\n%s", out)
	}
	if format, body, ok := RecordBlock(out); ok {
		t.Errorf("RecordBlock = %q, %q, true from an unreadable record, want no block:\n%s", format, body, out)
	}
}

// The wrapper's own variables are not ones a project exports: a variable the
// operator's environment exports reaches the runner with its own value.
func TestFileRecordHookLeavesTheOperatorsVariablesAlone(t *testing.T) {
	dir := withFakeRecordRow(t, "--log", recordToken)
	out, code := runIn(t, dir, graded(t, []string{"sh", "fakerunner", "env"}),
		"f=F", "d=D", "r=R", "a=A", "b=B", "rc=RC")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "env:F|D|R|A|B|RC\n") {
		t.Errorf("the runner inherited changed variables:\n%s", out)
	}
}

// A runner that writes its record to a file DERIVED from the path it is
// given (swift-testing 6.4.2 writes `--xunit-output x.xml` to
// x-swift-testing.xml) is read from the first of the hook's suffixed paths
// that holds a record: the derived file, the path itself when the runner
// wrote only that (swift-testing 6.3.2), and the first suffix when it wrote
// both.
func TestFileRecordHookReadsTheFirstSuffixedFile(t *testing.T) {
	f := RecordFile{Suffixes: []string{"-st.xml", ".xml"}}
	for _, c := range []struct{ name, arg, body string }{
		{"derived", "--derived=" + recordToken + ".xml", "<xml/>"},
		{"the path itself", "--out=" + recordToken + ".xml", "<xml/>"},
		{"both: the first suffix", "--both=" + recordToken + ".xml", "<derived/>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := withFakeRecordRowAt(t, f, c.arg)
			out, code := runIn(t, dir, graded(t, []string{"sh", "fakerunner", "exit=1"}))
			if code != 1 {
				t.Fatalf("exit %d, want the runner's 1:\n%s", code, out)
			}
			if format, body, ok := RecordBlock(out); !ok || format != "junit" || body != c.body {
				t.Errorf("RecordBlock = %q, %q, %v, want junit, %q, true, from:\n%s", format, body, ok, c.body, out)
			}
		})
	}
	// With no suffix the record is the path itself, as before: a derived
	// file is not read.
	dir := withFakeRecordRow(t, "--derived="+recordToken+".xml")
	if out, _ := runIn(t, dir, graded(t, []string{"sh", "fakerunner"})); strings.Contains(out, recordBegin) {
		t.Errorf("a hook with no suffix read a file it was not told of:\n%s", out)
	}
}

// A runner that derives its record's name from the path cannot be given
// /dev/null when the hook cannot set up: it would write beside it, where it
// may not (swift-testing 6.4.2 exits 1 on an otherwise passing run, "error:
// invalid access to /dev/null-swift-testing"). Such a hook leaves every word
// carrying the token out of the command instead, so the run is the
// operator's command exactly, with its own exit code, once, and says so.
func TestFileRecordHookDropsTheRecordWordWhenItCannotSetUp(t *testing.T) {
	dir := withFakeRecordRowAt(t, RecordFile{Suffixes: []string{"-st.xml"}, DropWhenUnset: true}, "--derived="+recordToken+".xml")
	for _, code := range []int{0, 1} {
		exit := map[int]string{0: "exit=0", 1: "exit=1"}[code]
		out, got := runIn(t, dir, graded(t, []string{"sh", "fakerunner", exit, "keep " + recordToken[:4]}), "TMPDIR="+filepath.Join(dir, "does-not-exist"))
		if got != code {
			t.Fatalf("exit %d, want the runner's %d:\n%s", got, code, out)
		}
		if n := strings.Count(out, "ran\n"); n != 1 {
			t.Errorf("the runner ran %d times, want once:\n%s", n, out)
		}
		if args := argLines(out); !slices.Equal(args, []string{exit, "keep " + recordToken[:4]}) {
			t.Errorf("runner's words = %q, want the operator's alone", args)
		}
		if strings.Contains(out, recordBegin) || !strings.Contains(out, "leaves its record option out") {
			t.Errorf("the fallback printed a block or did not say what it did:\n%s", out)
		}
	}
}

// A hook that drops its words on fallback drops every word it added, not
// only the one carrying the token: node --test pairs each --test-reporter
// with a --test-reporter-destination by position, so leaving out the
// destination alone would leave its reporter unpaired, and an option given
// its value as a separate word (`--log <token>`) would otherwise leave the
// option to take the operator's next word. The operator's words, a word
// equal to one of the hook's among them, all reach the runner.
func TestFileRecordHookDropsEveryHookWordWhenItCannotSetUp(t *testing.T) {
	dir := withFakeRecordRowAt(t, RecordFile{DropWhenUnset: true}, "--reporter=tap", "--log", recordToken, "nl")
	operator := []string{"sh", "fakerunner", "exit=1", "nl", "--"}
	cmd := graded(t, operator)
	if i := slices.Index(cmd, "drop:4:4"); i != 5 {
		t.Fatalf("fallback word = %q, want drop:4:4 (the hook's 4 words after the operator's first 4, before its --)", cmd[5])
	}
	out, got := runIn(t, dir, cmd, "TMPDIR="+filepath.Join(dir, "does-not-exist"))
	if got != 1 {
		t.Fatalf("exit %d, want the runner's 1:\n%s", got, out)
	}
	if args := argLines(out); !slices.Equal(args, []string{"exit=1", "nl", "--"}) {
		t.Errorf("runner's words = %q, want the operator's alone", args)
	}
	// With the directory made, every word is there and the record is read.
	out, _ = runIn(t, dir, cmd)
	if args := argLines(out); len(args) != 7 || args[2] != "--reporter=tap" || args[3] != "--log" || args[5] != "nl" {
		t.Errorf("runner's words = %q, want the hook's four before the --", args)
	}
	if _, body, ok := RecordBlock(out); !ok || body != "<xml/>" {
		t.Errorf("no record read with the directory made:\n%s", out)
	}
}

// What a RecordFile may say is checked when the hook is built: a suffix is
// a file name's tail (no "/", no space, no glob or quote character). A hook
// that drops its words on fallback may carry them in any shape, since it
// drops all of them (TestFileRecordHookDropsEveryHookWordWhenItCannotSetUp).
func TestFileRecordHookAtRefusesWhatItCannotKeep(t *testing.T) {
	for name, build := range map[string]func(){
		"a suffix with a slash": func() {
			FileRecordHookAt(func() []string { return nil }, "junit", "", RecordFile{Suffixes: []string{"a/b"}})
		},
		"a suffix with a space": func() {
			FileRecordHookAt(func() []string { return nil }, "junit", "", RecordFile{Suffixes: []string{"a b"}})
		},
		"a suffix with a glob": func() {
			FileRecordHookAt(func() []string { return nil }, "junit", "", RecordFile{Suffixes: []string{"*.xml"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("FileRecordHookAt accepted %s", name)
				}
			}()
			build()
		})
	}
}
