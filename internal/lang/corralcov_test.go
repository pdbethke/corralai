// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
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
		"php":        {{"pytest", "-q"}, {"go", "test", "./..."}, {"rspec"}, {"npm", "test"}, {}},
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
			lang: "php", dir: "php", tool: "php",
			testCmd:      []string{"php", "test/CalcTest.php"},
			wantExecuted: []string{"lib/Calc.php"},
			wantMeasured: []string{"lib/Dead.php"},
			wantAbsent:   []string{"lib/Unloaded.php"},
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
			// PHP is the one language whose coverage needs a runtime
			// EXTENSION, so the interpreter being present is not enough. A
			// machine with php and no pcov/Xdebug must SKIP rather than fail:
			// the missing driver is an environment fact, not a defect in the
			// reporter. CI installs php-pcov (see
			// scripts/ci-provision-test-toolchains.sh) so this does run there.
			if tc.lang == "php" {
				probe := exec.Command("php", "-r", `exit(extension_loaded("pcov") || extension_loaded("xdebug") ? 0 : 1);`)
				if err := probe.Run(); err != nil {
					t.Skip("php has no coverage driver (pcov/Xdebug) — install php-pcov to run this proof")
				}
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
			// STDERR IS CAPTURED, and separately from stdout. The instrumented
			// command deliberately pushes the suite's own output to stderr so
			// stdout carries only the report — which means every reason a run
			// could fail lives in the half a bare .Output() throws away. A
			// failure that reports "the report was empty" without the
			// interpreter's error costs a full CI round trip to learn nothing.
			var stderr bytes.Buffer
			c.Stderr = &stderr
			stdout, runErr := c.Output()

			got, err := r.ParseCoverage(string(stdout), root)
			if err != nil {
				t.Fatalf("ParseCoverage: %v\nrun error: %v\nstdout:\n%s\nSTDERR:\n%s", err, runErr, stdout, stderr.String())
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

// A SUITE THAT RUNS ITS TESTS IN A SUBPROCESS MUST NOT PRODUCE A FALSE
// FINDING. This is the regression for a shared report path.
//
// Ruby and PHP write their report at process shutdown. With one shared file
// opened 'w', every process in the tree truncates it and the PARENT exits
// LAST — so a Rakefile that shells out, or a runner that forks workers, had
// the child's real report overwritten by the parent's. The damage is not lost
// data: the parent LOADS the library without calling it, so its verdict for
// that file is `0`, and corral printed a file the suite genuinely covers under
// "measured and NEVER executed by the suite" — the only actionable list it
// produces.
//
// Negative control, run before this test was written: reverting the per-process
// filename to a single shared one makes lib/calc.rb come back present-FALSE.
func TestCoverageReporterMergesSubprocessReports(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not installed — this proof needs the real interpreter")
	}
	p, _ := ByName("ruby")
	r := p.(CoverageReporter)
	cmd, ok := r.CoverageCmd([]string{"ruby", "-Ilib", "run_tests.rb"})
	if !ok {
		t.Fatal("CoverageCmd refused the fixture's own command")
	}
	root, err := filepath.Abs(filepath.Join("testdata", "coverage", "ruby-subprocess"))
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir = root
	var stderr bytes.Buffer
	c.Stderr = &stderr
	stdout, runErr := c.Output()

	got, err := r.ParseCoverage(string(stdout), root)
	if err != nil {
		t.Fatalf("ParseCoverage: %v\nrun error: %v\nstdout:\n%s\nSTDERR:\n%s", err, runErr, stdout, stderr.String())
	}
	if !got["lib/calc.rb"] {
		t.Errorf("lib/calc.rb is executed BY THE CHILD process; got %v (present=%v).\n"+
			"A false here means one process's report clobbered another's — the parent, which merely "+
			"required the file, wins and the covered file is reported as never executed.\nfull map: %v",
			got["lib/calc.rb"], func() bool { _, ok := got["lib/calc.rb"]; return ok }(), got)
	}
	if v, ok := got["lib/dead.rb"]; !ok || v {
		t.Errorf("lib/dead.rb is loaded by the parent and never called: want present-FALSE, got present=%v value=%v", ok, v)
	}
	if _, ok := got["lib/unloaded.rb"]; ok {
		t.Error("lib/unloaded.rb is never loaded by either process: want ABSENT")
	}
}

// A REPORT WHOSE EVERY PATH IS OUTSIDE THE REPO IS A FAILURE TO MEASURE, NOT A
// MEASUREMENT OF NOTHING.
//
// The out-of-root filter runs after a line is counted, so a report consisting
// entirely of gems, stdlib or node_modules used to satisfy the has-entries
// check and return an EMPTY map with no error — which the caller reads as
// Ran=true and reports as a repo-wide "0 files executed". That is a claim about
// the repository manufactured from a run that measured nothing in it.
//
// This is the exact shape every non-Go language produced while the caller was
// passing an empty root, and the shape a wrongly-rooted run produces now.
func TestCorralCoverageReportRefusesAnAllOutsideRootReport(t *testing.T) {
	in := rubyCoverageHeader + "\n1 /usr/lib/ruby/3.3.0/set.rb\n0 /usr/lib/ruby/3.3.0/json.rb"
	got, err := corralCoverageReport(in, rubyCoverageHeader, "ruby", "/repo")
	if err == nil {
		t.Fatalf("want an error, got a map: %v", got)
	}
	if got != nil {
		t.Errorf("an error must return a NIL map, not an empty one a caller could iterate: %v", got)
	}
	if !strings.Contains(err.Error(), "NONE of them are under the repo root") {
		t.Errorf("the error must say what actually happened, got: %v", err)
	}
}

// A RUNNER NAME MUST BE IN COMMAND POSITION, not anywhere in the script.
//
// CoverageCmd's ok=false is how certify_repo.go decides which language an
// operator's `--` command belongs to, so a plugin that claims a command it
// cannot instrument makes the pre-flight either skip as "ambiguous" or
// instrument the wrong suite. Scanning the whole script matched a runner's name
// inside a PATH or a COMMENT: all four of the false cases below were accepted
// by the wrong language before this was a lexer.
func TestCoverageCmdMatchesOnlyCommandPosition(t *testing.T) {
	cases := []struct {
		lang string
		argv []string
		want bool
	}{
		{"javascript", []string{"sh", "-c", "pytest tests/node/"}, false},
		{"php", []string{"sh", "-c", "pytest --ignore=vendor/php"}, false},
		{"javascript", []string{"sh", "-c", "cargo test # node is not used here"}, false},
		{"ruby", []string{"sh", "-c", "pytest tests/ruby/"}, false},
		{"javascript", []string{"sh", "-c", "pytest --cov=node_modules"}, false},
		{"ruby", []string{"sh", "-c", "bundle install"}, false},
		{"javascript", []string{"sh", "-c", "npm test"}, true},
		{"javascript", []string{"sh", "-c", "cd web && npx vitest run"}, true},
		{"php", []string{"sh", "-c", "vendor/bin/phpunit --colors=never"}, true},
		{"ruby", []string{"sh", "-c", "BUNDLE_GEMFILE=x bundle exec rspec"}, true},
		// rubyPlugin.TestCmd()'s own shape: a dispatch script whose runner sits
		// after `then exec`. The plugin must be able to instrument its own
		// stock command — TestPluginStockCommandSatisfiesOwnCoverageCmd pins
		// that too, and this pins the reason it works.
		{"ruby", []string{"sh", "-c", `t="x"; if grep -q RSpec "$t"; then exec rspec "$t"; else exec ruby "$t"; fi`}, true},
	}
	for _, c := range cases {
		p, ok := ByName(c.lang)
		if !ok {
			t.Fatalf("no plugin %q", c.lang)
		}
		_, got := p.(CoverageReporter).CoverageCmd(c.argv)
		if got != c.want {
			t.Errorf("%s.CoverageCmd(%q) accepted=%v, want %v", c.lang, c.argv[2], got, c.want)
		}
	}
}

// THE JAIL RUNS THE SUITE SOMEWHERE ELSE. On the default substrate the suite
// executes in an ephemeral copy of the repo under /tmp, and the caller only
// knows the ORIGINAL repo's path. A reporter that emits absolute paths is
// therefore right on the workspace substrate and wrong on the jail: every path
// falls outside the root the caller aligns against, and the pre-flight fails
// with "NONE of them are under the repo root" on exactly the substrate
// operators get by default. Fixing the root for the workspace substrate this
// morning did not touch that.
//
// So the reducers emit paths RELATIVE TO THE WORKING DIRECTORY, which is
// repo-relative on both substrates — the same thing coverage.py does, and the
// reason Python never had this defect. This test reproduces the jail's shape:
// copy the fixture elsewhere, run there, and parse against the ORIGINAL root.
func TestCoverageReporterSurvivesRunningInACopyOfTheRepo(t *testing.T) {
	for _, tc := range []struct {
		lang, dir, tool string
		testCmd         []string
	}{
		{"ruby", "ruby", "ruby", []string{"ruby", "-Ilib", "test/calc_test.rb"}},
		{"javascript", "js", "node", []string{"node", "--test"}},
		{"php", "php", "php", []string{"php", "test/CalcTest.php"}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			if _, err := exec.LookPath(tc.tool); err != nil {
				t.Skip(tc.tool + " not installed")
			}
			if tc.lang == "php" {
				if exec.Command("php", "-r", `exit(extension_loaded("pcov") ? 0 : 1);`).Run() != nil {
					t.Skip("php has no coverage driver")
				}
			}
			original, _ := filepath.Abs(filepath.Join("testdata", "coverage", tc.dir))
			// The "jail": a copy somewhere the original's root does not cover.
			copyDir := filepath.Join(t.TempDir(), "corral-adequacy-jail")
			if out, err := exec.Command("cp", "-r", original, copyDir).CombinedOutput(); err != nil {
				t.Fatalf("copy fixture: %v\n%s", err, out)
			}
			p, _ := ByName(tc.lang)
			r := p.(CoverageReporter)
			cmd, _ := r.CoverageCmd(tc.testCmd)
			c := exec.Command(cmd[0], cmd[1:]...)
			c.Dir = copyDir
			var stderr bytes.Buffer
			c.Stderr = &stderr
			stdout, _ := c.Output()

			// Parsed against the ORIGINAL — the only root the caller has.
			got, err := r.ParseCoverage(string(stdout), original)
			if err != nil {
				t.Fatalf("ParseCoverage against the original root failed — the jail substrate would report could-not-run: %v\nstderr:\n%s", err, stderr.String())
			}
			executed := 0
			for k, v := range got {
				if filepath.IsAbs(k) {
					t.Errorf("absolute path in the map: %q — the reducer must emit cwd-relative paths", k)
				}
				if v {
					executed++
				}
			}
			if executed == 0 {
				t.Fatalf("no file reported executed when run from a copy: %v", got)
			}
		})
	}
}

// THE JAIL MUST BIND THE TOOLCHAIN THE WRAPPED COMMAND ACTUALLY RUNS. Every
// coverage command here is `sh -c …`, so resolving the toolchain from argv[0]
// bound nothing, and a Go under ~/sdk or a venv python vanished inside the
// jail for the pre-flight while working for the scoring runs beside it.
func TestInterpretersInSeesThroughTheWrapper(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"go", "test", "./..."}, []string{"go"}},
		{[]string{"sh", "-c", `d=$(mktemp -d); /home/u/.venv/bin/python -m pytest -q >&2; cat "$d/report"`}, []string{"mktemp", "/home/u/.venv/bin/python", "cat"}},
		{[]string{"sh", "-c", `NODE_V8_COVERAGE="$d" node --test >&2; node "$d/reduce.js"`}, []string{"node", "node"}},
		{[]string{"bash", "-c", `if grep -q RSpec "$t"; then exec rspec "$t"; else exec ruby "$t"; fi`}, []string{"grep", "rspec", "ruby"}},
	} {
		got := InterpretersIn(tc.argv)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("InterpretersIn(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}
