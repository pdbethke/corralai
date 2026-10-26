// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

func TestJavaScriptPlugin(t *testing.T) {
	p, ok := ByName("javascript")
	if !ok {
		t.Fatal("javascript plugin not registered")
	}
	for _, ok1 := range []string{"a.js", "a.mjs", "a.cjs"} {
		if !p.Detect(ok1) {
			t.Fatalf("Detect(%q) should be true", ok1)
		}
	}
	if p.Detect("a.ts") {
		t.Fatal("must not detect .ts")
	}
	if got := pairing.Candidates(p.TestRules(), "pkg/foo.js")[0]; got.Path != "pkg/foo.test.js" || got.Rank != 0 {
		t.Fatalf("Candidates()[0] = %+v", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"node", "--test"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	// A two-command SEQUENCE, not a single `&&`-joined argv element: `node
	// --check` only checks one file per invocation, and a bare `&&` argv
	// element only means anything to a shell — the workspace substrate execs
	// argv directly with no shell to interpret it.
	// …plus a LOAD gate as the third command (syntax alone let a mutant that
	// fails at import read as killed).
	cc := p.CompileCheck("foo.js", "foo.test.js")
	if len(cc) < 3 || !reflect.DeepEqual(cc[:2], [][]string{{"node", "--check", "foo.js"}, {"node", "--check", "foo.test.js"}}) ||
		cc[2][0] != "node" || !strings.Contains(strings.Join(cc[2], " "), "import(") || cc[2][len(cc[2])-1] != "foo.js" {
		t.Fatalf("CompileCheck = %v", cc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty")
	}
	if !strings.Contains(p.TestWriterSystem(), "node:test") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("js prompts must be language-appropriate")
	}
	if p.PromptLang() != "JavaScript" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestJavaScriptCandidatesOrder pins the ordered-candidate-list contract:
// sibling .test/.spec, then a same-dir __tests__/ folder, then a
// leading-segment-stripped parallel test/ or tests/ tree.
func TestJavaScriptCandidatesOrder(t *testing.T) {
	p, _ := ByName("javascript")
	cases := []struct {
		name string
		in   string
		want []pairing.Candidate
	}{
		{
			name: "top-level file",
			in:   "foo.js",
			want: []pairing.Candidate{
				{Path: "foo.test.js", Rank: 0}, {Path: "foo.spec.js", Rank: 0}, {Path: "__tests__/foo.test.js", Rank: 1},
				{Path: "test/foo.test.js", Rank: 2}, {Path: "tests/foo.test.js", Rank: 2},
			},
		},
		{
			name: "single-segment dir",
			in:   "pkg/foo.js",
			want: []pairing.Candidate{
				{Path: "pkg/foo.test.js", Rank: 0}, {Path: "pkg/foo.spec.js", Rank: 0}, {Path: "pkg/__tests__/foo.test.js", Rank: 1},
				{Path: "test/foo.test.js", Rank: 2}, {Path: "tests/foo.test.js", Rank: 2},
			},
		},
		{
			name: "src/ layout",
			in:   "src/pkg/foo.js",
			want: []pairing.Candidate{
				{Path: "src/pkg/foo.test.js", Rank: 0}, {Path: "src/pkg/foo.spec.js", Rank: 0}, {Path: "src/pkg/__tests__/foo.test.js", Rank: 1},
				{Path: "test/pkg/foo.test.js", Rank: 2}, {Path: "tests/pkg/foo.test.js", Rank: 2},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pairing.Candidates(p.TestRules(), c.in)
			if len(got) != len(c.want) {
				t.Fatalf("Candidates(%q) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i].Path != c.want[i].Path || got[i].Rank != c.want[i].Rank {
					t.Errorf("Candidates(%q)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// THE COUNTS COME FROM A REAL V8 RUN, not a hand-written report: the captured
// report is what TestCapturedCoverageReportsMatchALiveRun checks against a
// live run. The fixture's negative control is dead.{js,ts}: required by the
// suite and never called, so its module wrapper ran and none of its named
// functions did. It must read as measured (Executable>0) and never executed
// (Executed==0) — counting the wrapper's lines would call mere loading
// execution, the defect named-function coverage exists to avoid.
func TestJSCoverageCountsFromRealV8Output(t *testing.T) {
	for _, tc := range []struct{ lang, ext string }{{"javascript", "js"}, {"typescript", "ts"}} {
		t.Run(tc.lang, func(t *testing.T) {
			fx := capturedCoverageReport[tc.lang]
			raw, err := os.ReadFile(filepath.Join("testdata", "coverage", fx.report)) // #nosec G304 -- a fixed testdata path
			if err != nil {
				t.Fatal(err)
			}
			root, err := filepath.Abs(filepath.Join("testdata", "coverage", fx.dir))
			if err != nil {
				t.Fatal(err)
			}
			p, _ := ByName(tc.lang)
			got, err := p.(CoverageReporter).CoverageCounts(string(raw), root)
			if err != nil {
				t.Fatalf("CoverageCounts: %v", err)
			}
			calc, dead := "lib/calc."+tc.ext, "lib/dead."+tc.ext
			if c, ok := got[calc]; !ok || c.Executed == 0 || c.Executed > c.Executable {
				t.Errorf("%s is called by the suite: want 0 < Executed <= Executable, got present=%v %+v", calc, ok, c)
			}
			// add ran and sub did not, so not every executable line executed.
			if c := got[calc]; c.Executed >= c.Executable {
				t.Errorf("%s: sub is never called, so Executed must be below Executable, got %+v", calc, c)
			}
			if c, ok := got[dead]; !ok || c.Executed != 0 || c.Executable == 0 {
				t.Errorf("%s is required and never called: want Executed==0 && Executable>0, got present=%v %+v", dead, ok, c)
			}
			if _, ok := got["lib/unloaded."+tc.ext]; ok {
				t.Errorf("lib/unloaded.%s is never loaded: want ABSENT", tc.ext)
			}
			// An EMPTY file the suite requires has no executable line: there
			// is nothing to have run or skipped, so it is not measurable and
			// must be absent, never {0 0} — which ParseCoverage would read as
			// "measured, never executed", an accusation it cannot deserve.
			if c, ok := got["lib/empty."+tc.ext]; ok {
				t.Errorf("lib/empty.%s has no executable line: want ABSENT, got %+v", tc.ext, c)
			}
		})
	}
}

// runJSReducer runs jsCoverageReduce over hand-built V8 coverage files, in a
// temp project whose sources are given, and returns its report. The INPUT is
// built by hand here so the two edge cases below can be reached at all; the
// reducer that reads it is the shipped one, run by the real node.
func runJSReducer(t *testing.T, sources map[string]string, v8 []string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed — the reducer runs in node")
	}
	root := t.TempDir()
	for rel, body := range sources {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cov := filepath.Join(root, "cov")
	if err := os.Mkdir(cov, 0o750); err != nil {
		t.Fatal(err)
	}
	for i, body := range v8 {
		body = strings.ReplaceAll(body, "ROOT", root)
		if err := os.WriteFile(filepath.Join(cov, fmt.Sprintf("coverage-%d.json", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(t.TempDir(), "reduce.js")
	if err := os.WriteFile(script, []byte(jsCoverageReduce), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("node", script, cov) // #nosec G204 -- node and a temp script this test wrote
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		t.Fatalf("reducer: %v", err)
	}
	return string(out)
}

// A FILE WHOSE RANGES DO NOT FIT ITS SOURCE IS DISCLOSED, NEVER COUNTED. A
// runner that executes transformed code under the original file's URL hands
// V8 offsets into code that is not on disk; mapping them onto the file would
// produce a count of nothing in particular.
func TestJSReducerDisclosesRangesThatDoNotFitTheSource(t *testing.T) {
	out := runJSReducer(t,
		map[string]string{"lib/a.js": "function a() { return 1; }\n", "lib/b.js": "function b() { return 2; }\n"},
		[]string{`{"result":[
		 {"url":"file://ROOT/lib/a.js","functions":[{"functionName":"","ranges":[{"startOffset":0,"endOffset":27,"count":1}]},{"functionName":"a","ranges":[{"startOffset":500,"endOffset":540,"count":1}]}]},
		 {"url":"file://ROOT/lib/b.js","functions":[{"functionName":"","ranges":[{"startOffset":0,"endOffset":27,"count":1}]},{"functionName":"b","ranges":[{"startOffset":0,"endOffset":26,"count":1}]}]}]}`})
	if !strings.Contains(out, "# uncounted lib/a.js:") {
		t.Errorf("want lib/a.js disclosed as uncounted, got:\n%s", out)
	}
	got, err := corralCoverageCounts(out, jsCoverageHeaderV2, "javascript", "")
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if _, ok := got["lib/a.js"]; ok {
		t.Errorf("an uncounted file must be ABSENT, not given a count: %v", got)
	}
	if got["lib/b.js"] != (LineCounts{Executed: 1, Executable: 1}) {
		t.Errorf("lib/b.js: want {1 1}, got %+v", got["lib/b.js"])
	}
}

// Two processes that ran DIFFERENT functions of one file: the reducer sees
// lines, so it unions them — both lines executed — rather than keeping the
// larger of two counts the way the line-blind parser has to.
func TestJSReducerUnionsLinesAcrossProcesses(t *testing.T) {
	src := "function a() { return 1; }\nfunction b() { return 2; }\n"
	proc := func(aCount, bCount int) string {
		return fmt.Sprintf(`{"result":[{"url":"file://ROOT/lib/x.js","functions":[
		 {"functionName":"","ranges":[{"startOffset":0,"endOffset":54,"count":1}]},
		 {"functionName":"a","ranges":[{"startOffset":0,"endOffset":26,"count":%d}]},
		 {"functionName":"b","ranges":[{"startOffset":27,"endOffset":53,"count":%d}]}]}]}`, aCount, bCount)
	}
	out := runJSReducer(t, map[string]string{"lib/x.js": src}, []string{proc(1, 0), proc(0, 1)})
	got, err := corralCoverageCounts(out, jsCoverageHeaderV2, "javascript", "")
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if got["lib/x.js"] != (LineCounts{Executed: 2, Executable: 2}) {
		t.Errorf("want {2 2} (each process ran one of the two lines), got %+v\n%s", got["lib/x.js"], out)
	}
}

// A SCRIPT RANGE THAT IS NOT THE FILE ON DISK MEANS V8 RAN OTHER TEXT under
// this URL — vitest runs transformed code, whose shifted offsets usually still
// START inside the file, so a start-past-the-end check alone passes them and
// counts lines of nothing in particular. Shorter or longer, the file is
// disclosed and absent: from the counts, and from ParseCoverage (the boolean
// Preflight reads), which is now "never measured" rather than a value read off
// offsets into some other text.
func TestJSReducerDisclosesAScriptRangeThatIsNotTheFile(t *testing.T) {
	src := "function a() { return 1; }\n" // 27 characters
	doc := func(scriptEnd int) string {
		return fmt.Sprintf(`{"result":[
		 {"url":"file://ROOT/lib/a.js","functions":[{"functionName":"","ranges":[{"startOffset":0,"endOffset":%d,"count":1}]},{"functionName":"a","ranges":[{"startOffset":0,"endOffset":20,"count":1}]}]},
		 {"url":"file://ROOT/lib/ok.js","functions":[{"functionName":"","ranges":[{"startOffset":0,"endOffset":27,"count":1}]},{"functionName":"a","ranges":[{"startOffset":0,"endOffset":26,"count":1}]}]}]}`, scriptEnd)
	}
	for _, tc := range []struct {
		name string
		end  int
	}{{"shorter", 20}, {"longer", 40}} {
		t.Run(tc.name, func(t *testing.T) {
			out := runJSReducer(t, map[string]string{"lib/a.js": src, "lib/ok.js": src}, []string{doc(tc.end)})
			counts, err := corralCoverageCounts(out, jsCoverageHeaderV2, "javascript", "")
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}
			if _, ok := counts["lib/a.js"]; ok {
				t.Errorf("lib/a.js must be uncounted, got %+v\n%s", counts["lib/a.js"], out)
			}
			if counts["lib/ok.js"] != (LineCounts{Executed: 1, Executable: 1}) {
				t.Errorf("lib/ok.js (script range == file) must still count: %+v", counts["lib/ok.js"])
			}
			if u := UncountedCoverage(out); len(u) != 1 || !strings.HasPrefix(u[0], "lib/a.js: V8 ran ") {
				t.Errorf("UncountedCoverage = %q, want one lib/a.js disclosure", u)
			}
			executed, err := jsPlugin{}.ParseCoverage(out, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := executed["lib/a.js"]; ok {
				t.Errorf("Preflight's boolean for lib/a.js must be ABSENT, got %v", executed["lib/a.js"])
			}
		})
	}
}

// A FILE ANY PROCESS COULD NOT COUNT IS UNCOUNTED, in either order (ruling
// R30, final review T1). One worker counts lib/x.js with nothing run; another
// ran text under its URL that is not the file on disk. The file is not a
// measured zero — the second process may be exactly where it ran — so it is
// disclosed and absent, whichever process's report the reducer reads first.
// Before, a count read first won and the disclosure was dropped, and a
// disclosure read first lost to the count.
func TestJSReducerUncountsAFileAnyProcessCouldNotCount(t *testing.T) {
	src := "function a() { return 1; }\n" // 27 characters
	counted := `{"result":[{"url":"file://ROOT/lib/x.js","functions":[
	 {"functionName":"","ranges":[{"startOffset":0,"endOffset":27,"count":1}]},
	 {"functionName":"a","ranges":[{"startOffset":0,"endOffset":26,"count":0}]}]}]}`
	foreign := `{"result":[{"url":"file://ROOT/lib/x.js","functions":[
	 {"functionName":"","ranges":[{"startOffset":0,"endOffset":90,"count":1}]},
	 {"functionName":"a","ranges":[{"startOffset":10,"endOffset":60,"count":1}]}]}]}`
	for name, order := range map[string][]string{"counted first": {counted, foreign}, "uncountable first": {foreign, counted}} {
		out := runJSReducer(t, map[string]string{"lib/x.js": src}, order)
		if !strings.Contains(out, "# uncounted lib/x.js:") {
			t.Errorf("%s: want lib/x.js disclosed as uncounted, got:\n%s", name, out)
		}
		if strings.Contains(out, " lib/x.js\n") && !strings.Contains(out, "# uncounted lib/x.js") || strings.Contains(out, "0 1 lib/x.js") {
			t.Errorf("%s: lib/x.js must not be a measured zero:\n%s", name, out)
		}
	}
}
