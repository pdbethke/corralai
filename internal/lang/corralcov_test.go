// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// THE TRI-STATE IS THE WHOLE CONTRACT, so it is tested directly rather than
// only through a live run: present with Executed>0 is executed, present with
// Executed==0 is measured-and-never-executed, and ABSENT means never
// measured. Collapsing the third state into the second turns "we did not
// look" into "we looked and found nothing", which is a repo-wide accusation
// manufactured from a failure to measure.
func TestCorralCoverageCountsTriState(t *testing.T) {
	root := "/repo"
	report := strings.Join([]string{
		rubyCoverageHeaderV2,
		"2 3 /repo/lib/calc.rb",
		"0 2 /repo/lib/dead.rb",
		"1 1 /usr/lib/ruby/3.3.0/set.rb", // outside the root: dropped entirely
	}, "\n")

	got, err := corralCoverageCounts(report, rubyCoverageHeaderV2, "ruby", root)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c, ok := got["lib/calc.rb"]; !ok || c != (LineCounts{Executed: 2, Executable: 3}) {
		t.Errorf("lib/calc.rb: want {2 3}, got present=%v %+v", ok, c)
	}
	if c, ok := got["lib/dead.rb"]; !ok || c.Executed != 0 || c.Executable == 0 {
		t.Errorf("lib/dead.rb: want measured and never executed, got present=%v %+v", ok, c)
	}
	if _, ok := got["lib/unloaded.rb"]; ok {
		t.Error("a file the report never mentioned must be ABSENT, never inserted as {0 0}")
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
		"rust":       {{"pytest", "-q"}, {"go", "test", "./..."}, {"rspec"}, {"npm", "test"}, {"sh", "-c", "cargo test"}, {}},
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

	// Derived: every CoverageReporter must also refuse every OTHER
	// registered plugin's stock test command. The one exception is
	// javascript<->typescript: typescript.go delegates CoverageCmd (and
	// ParseCoverage) to jsPlugin, so each accepts the other's stock command.
	// That is harmless: cmd/corral's selectPreflightLanguage compares what
	// each matching language would actually run and treats identical
	// instrumentation as one run, so the overlap never wrongly instruments
	// the wrong suite or makes a mixed JS/TS repository ambiguous.
	sharedCoverage := map[[2]string]bool{
		{"javascript", "typescript"}: true,
		{"typescript", "javascript"}: true,
	}
	// An exemption that is no longer needed hides the next real overlap
	// behind it, so each one must still describe what the code does.
	for pair := range sharedCoverage {
		r, ok := registry[pair[0]].(CoverageReporter)
		if !ok {
			t.Errorf("sharedCoverage exempts %s, which is not a registered CoverageReporter — the exemption is stale and must be deleted", pair[0])
			continue
		}
		if _, ok := r.CoverageCmd(registry[pair[1]].TestCmd()); !ok {
			t.Errorf("sharedCoverage exempts %s accepting %s's stock command, but it refuses it — the exemption is stale and must be deleted", pair[0], pair[1])
		}
	}
	checked := 0
	for _, name := range Names() {
		r, ok := registry[name].(CoverageReporter)
		if !ok {
			continue
		}
		checked++
		for _, other := range Names() {
			if other == name || sharedCoverage[[2]string{name, other}] {
				continue
			}
			if _, ok := r.CoverageCmd(registry[other].TestCmd()); ok {
				t.Errorf("%s's CoverageCmd accepts %s's stock test command %q — it would instrument the wrong suite", name, other, registry[other].TestCmd())
			}
		}
	}
	if checked < 2 {
		t.Errorf("checked %d CoverageReporters, want at least 2 — the derived walk covered nothing worth the name", checked)
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
			wantAbsent:   []string{"lib/unloaded.rb", "lib/empty.rb"},
		},
		{
			lang: "javascript", dir: "js", tool: "node",
			testCmd:      []string{"node", "--test"},
			wantExecuted: []string{"lib/calc.js"},
			wantMeasured: []string{"lib/dead.js"},
			wantAbsent:   []string{"lib/unloaded.js", "lib/empty.js"},
		},
		{
			lang: "php", dir: "php", tool: "php",
			testCmd:      []string{"php", "test/CalcTest.php"},
			wantExecuted: []string{"lib/Calc.php"},
			wantMeasured: []string{"lib/Dead.php"},
			wantAbsent:   []string{"lib/Unloaded.php", "lib/Shape.php"},
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
			stdout, root, stderr, runErr := runCoverageFixture(t, tc.lang, tc.dir, tc.testCmd)
			got, err := r.ParseCoverage(stdout, root)
			if err != nil {
				t.Fatalf("ParseCoverage: %v\nrun error: %v\nstdout:\n%s\nSTDERR:\n%s", err, runErr, stdout, stderr)
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
					t.Errorf("%s is never loaded by the suite, or has no executable line: want ABSENT (not measured), but it is in the map", f)
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
func TestCorralCoverageCountsRefusesAnAllOutsideRootReport(t *testing.T) {
	in := rubyCoverageHeaderV2 + "\n1 4 /usr/lib/ruby/3.3.0/set.rb\n0 2 /usr/lib/ruby/3.3.0/json.rb"
	got, err := corralCoverageCounts(in, rubyCoverageHeaderV2, "ruby", "/repo")
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
		{"rust", "rust", "cargo", []string{"cargo", "test"}},
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
			if tc.lang == "rust" {
				if exec.Command(rustLLVMToolsProbe[0], rustLLVMToolsProbe[1:]...).Run() != nil { // #nosec G204 -- a fixed probe
					t.Skip("the rust toolchain has no llvm-tools")
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

// Both corralcov detectors ask "is this argv a shell -c wrapper?" and must
// give the same answer as preflight does, for every shell spelling: a
// `/usr/bin/bash -e -c` or `dash -c` wrapper is still seen through, and the
// script's own runner (not the shell) is what gets matched.
func TestShellWrapperSpellingsSeenThroughByCorralcov(t *testing.T) {
	runners := []string{"pytest"}
	for _, argv := range [][]string{
		{"sh", "-c", "pytest -q"},
		{"/bin/sh", "-c", "pytest -q"},
		{"/usr/bin/bash", "-e", "-c", "pytest -q"},
		{"dash", "-c", "pytest -q"},
	} {
		if !coverageRunnerNamed(argv, runners) {
			t.Errorf("coverageRunnerNamed(%q) = false, want true: the wrapped runner is pytest", argv)
		}
		if got := InterpretersIn(argv); strings.Join(got, ",") != "pytest" {
			t.Errorf("InterpretersIn(%q) = %v, want [pytest]", argv, got)
		}
	}
}

// The words each language's coverage wrapper claims, before they were
// derived from the runner rows. Deriving them must not change one: a word
// lost stops instrumenting that runner's suites, and a word gained claims a
// command for a language it is not.
func TestCoverageClaimsMatchTheListsTheyReplaced(t *testing.T) {
	for lang, old := range map[string][]string{
		"javascript": {"node", "npm", "npx", "yarn", "pnpm", "jest", "vitest", "mocha", "tap", "ava", "jasmine"},
		"php":        {"php", "phpunit", "phpdbg", "composer", "pest", "paratest", "codecept"},
		"ruby":       {"ruby", "rspec", "rake", "testrb"},
	} {
		got := slices.Sorted(slices.Values(coverageClaims(lang)))
		want := slices.Sorted(slices.Values(old))
		if !slices.Equal(got, want) {
			t.Errorf("%s claims %q, want %q", lang, got, want)
		}
	}
}

// THE V2 GRAMMAR CARRIES LINE COUNTS: `<executed> <executable> <path>`. A
// path outside the root is dropped exactly as v1 drops it, and the tri-state
// survives: a present entry with Executed==0 is measured-and-never-run.
func TestCorralCoverageCountsParsesV2(t *testing.T) {
	got, err := corralCoverageCounts("corral-js-coverage: v2\n3 10 /r/a.js\n0 4 /r/b.js\n", jsCoverageHeaderV2, "javascript", "/r")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]LineCounts{"a.js": {Executed: 3, Executable: 10}, "b.js": {Executed: 0, Executable: 4}}
	if !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A STALE REDUCER IS AN ERROR, NOT A HALF-READ. A v1 body has no counts; read
// as v2 it would either fail line by line or, worse, be guessed at. The error
// names the header it wanted so the mismatch is legible. Nothing emits v1 any
// more, which is exactly when a stale copy of a reducer would be the one
// still printing it, so the v1 headers are spelled out here.
func TestCorralCoverageCountsRefusesV1(t *testing.T) {
	for _, tc := range []struct{ name, in, header string }{
		{"js v1", "corral-js-coverage: v1\n1 /r/a.js\n0 /r/b.js\n", jsCoverageHeaderV2},
		{"ruby v1", "corral-ruby-coverage: v1\n1 /r/a.rb\n", rubyCoverageHeaderV2},
		{"php v1", "corral-php-coverage: v1\n1 /r/a.php\n", phpCoverageHeaderV2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := corralCoverageCounts(tc.in, tc.header, "x", "/r")
			if err == nil {
				t.Fatalf("want an error, got %v", got)
			}
			if got != nil {
				t.Errorf("an error must return a NIL map: %v", got)
			}
			if !strings.Contains(err.Error(), tc.header) {
				t.Errorf("the error must name the header it wanted (%q), got: %v", tc.header, err)
			}
		})
	}
}

// Every malformed v2 shape is an error with a nil map, never a partial one.
func TestCorralCoverageCountsRefusesGarbage(t *testing.T) {
	h := jsCoverageHeaderV2
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"whitespace", "   \n\n  "},
		{"no header", "1 2 /r/a.js"},
		{"stack trace only", "Traceback:\n  something exploded\n"},
		{"header but no entries", h},
		{"only disclosures", h + "\n# uncounted lib/a.js: the source could not be read"},
		{"v1 line under a v2 header", h + "\n1 /r/a.js"},
		{"non-numeric executed", h + "\nx 2 /r/a.js"},
		{"non-numeric executable", h + "\n1 y /r/a.js"},
		{"negative count", h + "\n-1 2 /r/a.js"},
		{"executed exceeds executable", h + "\n5 2 /r/a.js"},
		{"missing path", h + "\n1 2"},
		{"all outside root", h + "\n1 2 /usr/lib/node/x.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := corralCoverageCounts(tc.in, h, "javascript", "/r")
			if err == nil {
				t.Fatalf("want an error, got %v", got)
			}
			if got != nil {
				t.Errorf("an error must return a NIL map: %v", got)
			}
		})
	}
}

// Several processes report one file: the larger count of each kind wins, so a
// run that executed it is never downgraded by one that only loaded it, and no
// count is invented by adding lines that may be the same lines.
func TestCorralCoverageCountsNeverDowngradesAHit(t *testing.T) {
	for _, order := range []string{"2 5 /r/a.js\n0 5 /r/a.js", "0 5 /r/a.js\n2 5 /r/a.js"} {
		got, err := corralCoverageCounts(jsCoverageHeaderV2+"\n"+order, jsCoverageHeaderV2, "javascript", "/r")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got["a.js"] != (LineCounts{Executed: 2, Executable: 5}) {
			t.Errorf("order %q: got %v, want {2 5}", strings.ReplaceAll(order, "\n", " | "), got["a.js"])
		}
	}
}

// capturedCoverageReport names, per language, the report a REAL run of the
// fixture suite produced, and the command that produced it. The file is
// written only by TestCapturedCoverageReportsMatchALiveRun with
// CORRALAI_RECAPTURE_COVERAGE=1 — never by hand. Every CoverageReporter has
// one, which is what TestParseCoverageAgreesWithCoverageCounts requires.
//
// probe, when set, must exit 0 for the live half to run: PHP needs a coverage
// EXTENSION and Python needs coverage.py, and an interpreter without them is
// an environment fact, not drift. payload is what of a live run's stdout is
// the report, and compared is what of a report must match byte for byte —
// both named per language, since what is stable differs (see coverageAsIs,
// goCoveragePayload, pyCoveragePayload, pyCoverageCompared).
var capturedCoverageReport = map[string]struct {
	dir, report, tool string
	probe             []string
	testCmd           []string
	payload, compared func(string) string
	// scratchEnv names an environment variable the live run points at a
	// fresh temp directory: a build cache the run would otherwise leave
	// inside testdata (cargo's target directory).
	scratchEnv string
}{
	"javascript": {dir: "js", report: "js-report.txt", tool: "node", testCmd: []string{"node", "--test"}, payload: coverageAsIs, compared: coverageAsIs},
	"go":         {dir: "go", report: "go-report.txt", tool: "go", testCmd: []string{"go", "test", "-count=1", "./..."}, payload: goCoveragePayload, compared: coverageAsIs},
	"typescript": {dir: "ts", report: "ts-report.txt", tool: "node", testCmd: []string{"node", "--test"}, payload: coverageAsIs, compared: coverageAsIs},
	"ruby":       {dir: "ruby", report: "ruby-report.txt", tool: "ruby", testCmd: []string{"ruby", "-Ilib", "test/calc_test.rb"}, payload: coverageAsIs, compared: coverageAsIs},
	"php": {dir: "php", report: "php-report.txt", tool: "php", testCmd: []string{"php", "test/CalcTest.php"}, payload: coverageAsIs, compared: coverageAsIs,
		probe: []string{"php", "-r", `exit(extension_loaded("pcov") ? 0 : 1);`}},
	"python": {dir: "python", report: "python-report.txt", tool: "python3", testCmd: []string{"python3", "-m", "pytest", "-q"}, payload: pyCoveragePayload, compared: pyCoverageCompared,
		probe: []string{"python3", "-c", "import coverage, pytest"}},
	"rust": {dir: "rust", report: "rust-report.txt", tool: "cargo", testCmd: []string{"cargo", "test"}, payload: coverageAsIs, compared: coverageAsIs,
		probe: rustLLVMToolsProbe, scratchEnv: "CARGO_TARGET_DIR"},
}

// rustLLVMToolsProbe exits 0 when the active toolchain carries llvm-tools,
// or llvm-cov is on PATH: what rustPlugin.CoverageCmd needs, looked up the
// same way.
var rustLLVMToolsProbe = []string{"sh", "-c", `b="$(rustc --print sysroot)/lib/rustlib/$(rustc -vV | sed -n 's/^host: //p')/bin"; test -x "$b/llvm-cov" || command -v llvm-cov`}

// coverageAsIs is a report that is stable as printed.
func coverageAsIs(s string) string { return s }

// goCoveragePayload drops the timing line `go test` prints ahead of the
// profile, which changes every run; the report is what follows the profile's
// mode header.
func goCoveragePayload(s string) string {
	if i := strings.Index(s, "mode: "); i > 0 {
		return s[i:]
	}
	return s
}

// pyCoveragePayload is the JSON report: the last non-blank line, which is
// where pyPlugin.ParseCoverage finds it. Ahead of it is pytest's own
// progress output, timing included.
func pyCoveragePayload(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1] + "\n"
}

// pyCoverageCompared is what of coverage.py's JSON must match a live run:
// every file's summary counts and the totals, which is all CoverageCounts
// reads. The rest is not stable across runs or machines — a timestamp, the
// coverage.py version, and per-version extras (the functions and classes
// breakdowns appeared in 7.5) — so comparing it byte for byte would fail on
// a CI runner whose coverage.py is older without anything corral reads
// having changed. An unparseable report compares as itself, so it still
// fails.
func pyCoverageCompared(s string) string {
	var r pyCoverageReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &r); err != nil {
		return s
	}
	var b strings.Builder
	for _, p := range slices.Sorted(maps.Keys(r.Files)) {
		f := r.Files[p].Summary
		fmt.Fprintf(&b, "%d %d %s\n", f.CoveredLines, f.NumStatements, p)
	}
	fmt.Fprintf(&b, "totals %d\n", r.Totals.CoveredLines)
	return b.String()
}

// runCoverageFixture runs lang's CoverageCmd over the fixture suite in
// testdata/coverage/<dir> and returns its stdout (the report) and the
// fixture's absolute root. STDERR is captured separately and shown on failure:
// the instrumented command pushes the suite's own output there, so every
// reason a run could fail lives in the half a bare .Output() throws away.
func runCoverageFixture(t *testing.T, langName, dir string, testCmd []string) (stdout, root string, stderr string, runErr error) {
	t.Helper()
	p, _ := ByName(langName)
	r := p.(CoverageReporter)
	cmd, ok := r.CoverageCmd(testCmd)
	if !ok {
		t.Fatalf("CoverageCmd refused %v", testCmd)
	}
	root, err := filepath.Abs(filepath.Join("testdata", "coverage", dir))
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir = root
	var errBuf bytes.Buffer
	c.Stderr = &errBuf
	out, runErr := c.Output()
	return string(out), root, errBuf.String(), runErr
}

// THE CAPTURED REPORTS ARE WHAT A LIVE RUN PRINTS, byte for byte. This is
// what keeps them from being hand-edited or going stale when the reducer
// changes: with the runtime installed, any drift fails here. Set
// CORRALAI_RECAPTURE_COVERAGE=1 to rewrite them from a real run.
func TestCapturedCoverageReportsMatchALiveRun(t *testing.T) {
	for _, langName := range slices.Sorted(maps.Keys(capturedCoverageReport)) {
		fx := capturedCoverageReport[langName]
		t.Run(langName, func(t *testing.T) {
			if _, err := exec.LookPath(fx.tool); err != nil {
				t.Skipf("%s not installed — the live half of this check needs the real runtime", fx.tool)
			}
			if fx.probe != nil {
				if err := exec.Command(fx.probe[0], fx.probe[1:]...).Run(); err != nil { // #nosec G204 -- a fixed probe from the table above
					t.Skipf("%v failed (%v) — this machine lacks the coverage instrument the live half needs", fx.probe, err)
				}
			}
			if fx.scratchEnv != "" {
				t.Setenv(fx.scratchEnv, t.TempDir())
			}
			stdout, _, stderr, runErr := runCoverageFixture(t, langName, fx.dir, fx.testCmd)
			stdout = fx.payload(stdout)
			path := filepath.Join("testdata", "coverage", fx.report)
			if os.Getenv("CORRALAI_RECAPTURE_COVERAGE") == "1" {
				if err := os.WriteFile(path, []byte(stdout), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Logf("recaptured %s (run error: %v)", path, runErr)
				return
			}
			want, err := os.ReadFile(path) // #nosec G304 -- a fixed testdata path
			if err != nil {
				t.Fatal(err)
			}
			if fx.compared(stdout) != fx.compared(string(want)) {
				t.Errorf("a live run no longer prints the captured %s.\nlive:\n%s\ncaptured:\n%s\nrun error: %v\nSTDERR:\n%s\n"+
					"If the reducer changed on purpose, recapture with CORRALAI_RECAPTURE_COVERAGE=1.", path, stdout, want, runErr, stderr)
			}
		})
	}
}

// PARSECOVERAGE IS DERIVED FROM COVERAGECOUNTS, and this holds it there: over
// each language's real captured report, a file is executed exactly when it
// has an executed line, and both maps name the same files. Every reporter
// must have a captured report, so a new one cannot skip this by omission.
func TestParseCoverageAgreesWithCoverageCounts(t *testing.T) {
	for name, p := range registry {
		r, ok := p.(CoverageReporter)
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			fx, ok := capturedCoverageReport[name]
			if !ok {
				t.Fatalf("%s reports line counts but has no captured report in capturedCoverageReport", name)
			}
			raw, err := os.ReadFile(filepath.Join("testdata", "coverage", fx.report)) // #nosec G304 -- a fixed testdata path
			if err != nil {
				t.Fatal(err)
			}
			root, err := filepath.Abs(filepath.Join("testdata", "coverage", fx.dir))
			if err != nil {
				t.Fatal(err)
			}
			executed, err := r.ParseCoverage(string(raw), root)
			if err != nil {
				t.Fatalf("ParseCoverage: %v", err)
			}
			counts, err := r.CoverageCounts(string(raw), root)
			if err != nil {
				t.Fatalf("CoverageCounts: %v", err)
			}
			if !slices.Equal(slices.Sorted(maps.Keys(executed)), slices.Sorted(maps.Keys(counts))) {
				t.Fatalf("key sets differ: ParseCoverage %v, CoverageCounts %v", executed, counts)
			}
			// The ONE allowed divergence (CoverageReporter.CoverageCounts): Go
			// may read executed with Executed==0, never the other way round.
			diverged := 0
			for f, c := range counts {
				if executed[f] == (c.Executed > 0) {
					continue
				}
				if name == "go" && executed[f] && c.Executed == 0 {
					diverged++
					continue
				}
				t.Errorf("%s: ParseCoverage says %v, counts say %+v", f, executed[f], c)
			}
			if wantDiverged := name == "go"; wantDiverged && diverged == 0 {
				t.Errorf("go's captured report exercises no empty-function file, so the allowed divergence is untested")
			} else if !wantDiverged && diverged != 0 {
				t.Errorf("%s diverged %d times; only go may", name, diverged)
			}
		})
	}
}

// A DISCLOSURE SURVIVES A REPORT THAT ALSO HAS COUNTS. CoverageCounts leaves
// the uncounted file absent; UncountedCoverage is where its reason is read,
// with the path normalised exactly as CoverageCounts normalises one.
func TestUncountedCoverageKeepsDisclosuresBesideCounts(t *testing.T) {
	report := jsCoverageHeaderV2 + "\n" +
		"1 2 lib/a.js\n" +
		"# uncounted ./lib/b.js: V8 ran 300 characters under its URL and the file on disk has 120\n" +
		"0 3 lib/c.js\n"
	counts, err := corralCoverageCounts(report, jsCoverageHeaderV2, "javascript", "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := map[string]LineCounts{"lib/a.js": {1, 2}, "lib/c.js": {0, 3}}; !maps.Equal(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	got := UncountedCoverage(report)
	want := []string{"lib/b.js: V8 ran 300 characters under its URL and the file on disk has 120"}
	if !slices.Equal(got, want) {
		t.Errorf("UncountedCoverage = %q, want %q", got, want)
	}
	if extra := UncountedCoverage(jsCoverageHeaderV2 + "\n# uncounted ../outside.js: x\n1 1 lib/a.js\n"); len(extra) != 0 {
		t.Errorf("a path escaping the repository must be dropped as CoverageCounts drops it, got %q", extra)
	}
}

// capturedCounts is CoverageCounts over langName's captured real-run report
// (the one TestCapturedCoverageReportsMatchALiveRun holds to a live run),
// parsed against its fixture root.
func capturedCounts(t *testing.T, langName string) map[string]LineCounts {
	t.Helper()
	fx, ok := capturedCoverageReport[langName]
	if !ok {
		t.Fatalf("%s has no captured coverage report", langName)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "coverage", fx.report)) // #nosec G304 -- a fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("testdata", "coverage", fx.dir))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := ByName(langName)
	got, err := p.(CoverageReporter).CoverageCounts(string(raw), root)
	if err != nil {
		t.Fatalf("%s CoverageCounts: %v", langName, err)
	}
	return got
}

// assertFixtureCounts holds the shape every reduce-in-process fixture has: a
// called file executed in part, a required-and-never-called file measured
// with nothing run, a never-loaded file absent, and a loaded file with no
// executable line absent too — nothing in it could have run, so it is not
// measurable, and "measured, never executed" would accuse it of a gap it
// cannot have.
func assertFixtureCounts(t *testing.T, got map[string]LineCounts, called, dead, unloaded, empty string) {
	t.Helper()
	if c, ok := got[called]; !ok || c.Executed == 0 || c.Executed >= c.Executable {
		t.Errorf("%s: one method is called and one is not, want 0 < Executed < Executable, got present=%v %+v (full: %v)", called, ok, c, got)
	}
	if dead != "" {
		if c, ok := got[dead]; !ok || c.Executed != 0 || c.Executable == 0 {
			t.Errorf("%s is loaded and never called: want Executed==0 && Executable>0, got present=%v %+v", dead, ok, c)
		}
	}
	if _, ok := got[unloaded]; ok && unloaded != "" {
		t.Errorf("%s is never loaded: want ABSENT, got %+v", unloaded, got[unloaded])
	}
	if c, ok := got[empty]; ok {
		t.Errorf("%s has no executable line: want ABSENT (not measurable), got %+v", empty, c)
	}
}

// A FILE A RUN LOADED AND NEVER CALLED IS NAMED LOADED, in every reporter
// that can see it (ruling R24, carried past JavaScript by the final review's
// I1). Ruby runs a required file's class body, constants and def lines;
// pcov reports a required PHP file's top-level statements and its include
// marker. Both reports used to drop that, so unit evidence recorded such a
// file `uncovered` — the loudest claim the record carries — when a unit had
// in fact executed it. The reports are captured from real runs and held to a
// live one (TestCapturedCoverageReportsMatchALiveRun). A file never loaded,
// or one with nothing executable, is never named.
func TestReportsNameALoadedFileWhoseMethodsNeverRan(t *testing.T) {
	cases := []struct {
		lang, report                string
		called, dead, unloaded, nop string
	}{
		{"ruby", "ruby-report.txt", "lib/calc.rb", "lib/dead.rb", "lib/unloaded.rb", "lib/empty.rb"},
		{"php", "php-report.txt", "lib/Calc.php", "lib/Dead.php", "lib/Unloaded.php", "lib/Shape.php"},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "coverage", c.report)) // #nosec G304 -- a fixed testdata path
			if err != nil {
				t.Fatal(err)
			}
			loaded := LoadedCoverage(string(raw))
			for _, want := range []string{c.called, c.dead} {
				if !slices.Contains(loaded, want) {
					t.Errorf("LoadedCoverage = %q, want it to name %s: the run loaded it", loaded, want)
				}
			}
			for _, never := range []string{c.unloaded, c.nop} {
				if slices.Contains(loaded, never) {
					t.Errorf("LoadedCoverage = %q names %s, which the run never loaded or which has nothing executable", loaded, never)
				}
			}
			counts := capturedCounts(t, c.lang)
			if lc := counts[c.dead]; lc.Executed != 0 || lc.Executable == 0 {
				t.Errorf("%s counts = %+v: loading it must not make its methods read as executed", c.dead, lc)
			}
		})
	}
}
