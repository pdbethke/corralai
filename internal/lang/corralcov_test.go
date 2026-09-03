// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE TRI-STATE IS THE WHOLE CONTRACT, so it is tested directly rather than
// only through a live run: present-true is executed, present-false is
// measured-and-never-executed, and ABSENT means never measured. Collapsing the
// third state into the second turns "we did not look" into "we looked and
// found nothing", which is a repo-wide accusation manufactured from a failure
// to measure.
func TestCorralCoverageReportTriState(t *testing.T) {
	root := "/repo"
	report := strings.Join([]string{
		rubyCoverageHeader,
		"1 /repo/lib/calc.rb",
		"0 /repo/lib/dead.rb",
		"1 /usr/lib/ruby/3.3.0/set.rb", // outside the root: dropped entirely
	}, "\n")

	got, err := corralCoverageReport(report, rubyCoverageHeader, "ruby", root)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, ok := got["lib/calc.rb"]; !ok || !v {
		t.Errorf("lib/calc.rb: want present-true, got present=%v value=%v", ok, v)
	}
	if v, ok := got["lib/dead.rb"]; !ok || v {
		t.Errorf("lib/dead.rb: want present-FALSE (measured, never executed), got present=%v value=%v", ok, v)
	}
	if _, ok := got["lib/unloaded.rb"]; ok {
		t.Error("a file the report never mentioned must be ABSENT, never inserted as false")
	}
	for k := range got {
		if strings.HasPrefix(k, "..") || filepath.IsAbs(k) {
			t.Errorf("path outside the repo root leaked into the map: %q", k)
		}
	}
	if len(got) != 2 {
		t.Errorf("want exactly the 2 in-root entries, got %d: %v", len(got), got)
	}
}

// A REPORT THAT DID NOT PARSE MUST BE AN ERROR, NEVER AN EMPTY MAP. Every one
// of these inputs would otherwise read as "the suite covered nothing" — which
// is why the header exists at all.
func TestCorralCoverageReportRefusesGarbage(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"whitespace", "   \n\n  "},
		{"no header", "1 /repo/lib/a.rb\n0 /repo/lib/b.rb"},
		{"stack trace only", "Traceback:\n  something exploded\n"},
		{"header but no entries", rubyCoverageHeader},
		{"malformed hit column", rubyCoverageHeader + "\nyes /repo/lib/a.rb"},
		{"missing path", rubyCoverageHeader + "\n1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := corralCoverageReport(tc.in, rubyCoverageHeader, "ruby", "/repo")
			if err == nil {
				t.Fatalf("want an error, got a map: %v", got)
			}
			if got != nil {
				t.Errorf("an error must return a NIL map, not a partial one: %v", got)
			}
		})
	}
}

// A file reported by several test processes (workers, sharded runs) is
// executed if ANY of them executed it. A later 0 must never downgrade an
// earlier 1 — with jest or `node --test` spawning one process per file, the
// same module is routinely reported both ways in one run.
func TestCorralCoverageReportNeverDowngradesAHit(t *testing.T) {
	for _, order := range []string{"1 /repo/a.js\n0 /repo/a.js", "0 /repo/a.js\n1 /repo/a.js"} {
		got, err := corralCoverageReport(jsCoverageHeader+"\n"+order, jsCoverageHeader, "javascript", "/repo")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !got["a.js"] {
			t.Errorf("order %q: a hit from any process must win, got false", strings.ReplaceAll(order, "\n", " | "))
		}
	}
}

// EVERY CoverageReporter MUST REFUSE A COMMAND IT CANNOT INSTRUMENT, because
// certify_repo.go reads ok=false to decide which language an operator's `--`
// command belongs to. A plugin that accepts everything would claim another
// language's test command and silently instrument the wrong suite.
func TestCoverageCmdRefusesForeignCommands(t *testing.T) {
	foreign := map[string][][]string{
		"ruby":       {{"pytest", "-q"}, {"go", "test", "./..."}, {"npm", "test"}, {"bundle", "install"}, {}},
		"javascript": {{"pytest", "-q"}, {"go", "test", "./..."}, {"rspec"}, {}},
		"typescript": {{"pytest", "-q"}, {"go", "test", "./..."}, {"rspec"}, {}},
	}
	for name, cmds := range foreign {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("no plugin %q", name)
		}
		r, ok := p.(CoverageReporter)
		if !ok {
			t.Fatalf("%s does not implement CoverageReporter", name)
		}
		for _, c := range cmds {
			if _, ok := r.CoverageCmd(c); ok {
				t.Errorf("%s.CoverageCmd accepted a foreign command %v — it would instrument the wrong suite", name, c)
			}
		}
	}
}

// THE LIVE PROOF. A parser test cannot tell you the instrumentation works;
// only running a real suite under it can. Each of these runs the EXACT command
// CoverageCmd builds against a real fixture project and asserts the tri-state
// comes back from an actual interpreter.
//
// The fixtures are deliberately shaped around the one distinction that is easy
// to get wrong: dead.{rb,js,ts} is REQUIRED by the suite and never called.
// Line-coverage (Ruby) and module-wrapper-coverage (V8) both report it as
// executed, and both would be wrong — that file is the negative control for
// the whole feature.
func TestCoverageReporterAgainstRealSuites(t *testing.T) {
	cases := []struct {
		lang, dir, tool string
		testCmd         []string
		wantExecuted    []string
		wantMeasured    []string
		wantAbsent      []string
	}{
		{
			lang: "ruby", dir: "ruby", tool: "ruby",
			testCmd:      []string{"ruby", "-Ilib", "test/calc_test.rb"},
			wantExecuted: []string{"lib/calc.rb"},
			wantMeasured: []string{"lib/dead.rb"},
			wantAbsent:   []string{"lib/unloaded.rb"},
		},
		{
			lang: "javascript", dir: "js", tool: "node",
			testCmd:      []string{"node", "--test"},
			wantExecuted: []string{"lib/calc.js"},
			wantMeasured: []string{"lib/dead.js"},
			wantAbsent:   []string{"lib/unloaded.js"},
		},
		{
			lang: "typescript", dir: "ts", tool: "node",
			testCmd:      []string{"node", "--test"},
			wantExecuted: []string{"lib/calc.ts"},
			wantMeasured: []string{"lib/dead.ts"},
			wantAbsent:   []string{"lib/unloaded.ts"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			if _, err := exec.LookPath(tc.tool); err != nil {
				t.Skipf("%s not installed — this proof needs the real interpreter", tc.tool)
			}
			p, _ := ByName(tc.lang)
			r := p.(CoverageReporter)
			cmd, ok := r.CoverageCmd(tc.testCmd)
			if !ok {
				t.Fatalf("CoverageCmd refused %v", tc.testCmd)
			}
			root, err := filepath.Abs(filepath.Join("testdata", "coverage", tc.dir))
			if err != nil {
				t.Fatal(err)
			}
			c := exec.Command(cmd[0], cmd[1:]...)
			c.Dir = root
			stdout, runErr := c.Output()

			got, err := r.ParseCoverage(string(stdout), root)
			if err != nil {
				t.Fatalf("ParseCoverage: %v\nrun error: %v\nstdout:\n%s", err, runErr, stdout)
			}
			for _, f := range tc.wantExecuted {
				if v, ok := got[f]; !ok || !v {
					t.Errorf("%s: want EXECUTED, got present=%v value=%v (full map: %v)", f, ok, v, got)
				}
			}
			for _, f := range tc.wantMeasured {
				if v, ok := got[f]; !ok || v {
					t.Errorf("%s is required by the suite and never called: want present-FALSE, got present=%v value=%v.\n"+
						"A true here means the reporter is counting mere LOADING as execution — the exact defect "+
						"method/named-function coverage exists to avoid.", f, ok, v)
				}
			}
			for _, f := range tc.wantAbsent {
				if _, ok := got[f]; ok {
					t.Errorf("%s is never loaded by the suite: want ABSENT (not measured), but it is in the map", f)
				}
			}
		})
	}
}
