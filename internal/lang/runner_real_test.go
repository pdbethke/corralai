// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ranCount reads how many tests a runner says it ran and passed, from its
// own summary line (jest, vitest, mocha, node's TAP or spec reporter, rspec,
// PHPUnit, minitest), or -1.
var ranCount = regexp.MustCompile(`(?m)(\d+) passed|(\d+) passing|^[#ℹ] pass (\d+)|(\d+) examples?, 0 failures|OK \((\d+) tests?|(\d+) runs, \d+ assertions, 0 failures, 0 errors`)

func ran(out string) int {
	m := ranCount.FindStringSubmatch(out)
	for _, g := range m[min(1, len(m)):] {
		if g != "" {
			n, _ := strconv.Atoi(g)
			return n
		}
	}
	return -1
}

// TestRealRunnersListAndRunOne drives each runner row end to end against the
// real runner over the fixture suites: list, resolve a critic's name, run
// that one test, and require the runner to report exactly one test run.
// node runs wherever the suite does; rspec where CI provisions it; jest,
// vitest, mocha and PHPUnit when CORRALAI_RUNNER_ROOT names a directory
// prepared by `scripts/capture-runner-fixtures.sh --keep DIR`. A runner that
// is absent is SKIPPED, by name, so the CI census shows what was not driven.
func TestRealRunnersListAndRunOne(t *testing.T) {
	root := os.Getenv("CORRALAI_RUNNER_ROOT")
	rspec := []string{"rspec"}
	if exec.Command("rspec", "--version").Run() != nil {
		rspec = []string{"ruby", "-S", "rspec"}
	}
	for _, c := range []struct {
		runner, lang string
		argv         []string
		testPath     string
		name, want   string
		needsRoot    bool
		// throughScript: argv runs a package script, read from the
		// fixture's package.json / composer.json (lang.Resolve).
		throughScript bool
	}{
		{"node --test", "javascript", []string{"node", "--test"}, "calc.node.test.js", "top level", "top level", false, false},
		{"rspec", "ruby", rspec, "spec/calc_spec.rb", "level", "top::level", false, false},
		{"jest", "javascript", []string{"npx", "--no-install", "jest"}, "calc.jest.test.js", "top level", "top level", true, false},
		{"vitest", "typescript", []string{"npx", "--no-install", "vitest", "run"}, "calc.vitest.test.mjs", "top level", "top level", true, false},
		{"mocha", "javascript", []string{"npx", "--no-install", "mocha"}, "calc.mocha.js", "top level", "top level", true, false},
		{"phpunit", "php", []string{"vendor/bin/phpunit"}, "tests/CalcTest.php", "testAdds", "CalcTest::testAdds", true, false},
		{"jest", "javascript", []string{"npm", "test"}, "calc.jest.test.js", "top level", "top level", true, true},
		{"jest", "javascript", []string{"yarn", "test"}, "calc.jest.test.js", "top level", "top level", true, true},
		{"jest", "javascript", []string{"pnpm", "test"}, "calc.jest.test.js", "top level", "top level", true, true},
		{"phpunit", "php", []string{"composer", "test"}, "tests/CalcTest.php", "testAdds", "CalcTest::testAdds", true, true},
	} {
		t.Run(c.runner+"/"+c.argv[0], func(t *testing.T) {
			dir := root
			if !c.needsRoot {
				dir = t.TempDir()
				if err := os.CopyFS(dir, os.DirFS("testdata/runners/src")); err != nil {
					t.Fatal(err)
				}
			} else if dir == "" {
				t.Skipf("SKIP %s: CORRALAI_RUNNER_ROOT is not set (scripts/capture-runner-fixtures.sh --keep DIR)", c.runner)
			}
			if _, err := exec.LookPath(c.argv[0]); err != nil && !filepath.IsAbs(c.argv[0]) && filepath.Base(c.argv[0]) == c.argv[0] {
				t.Skipf("SKIP %s: %s is not installed", c.runner, c.argv[0])
			}
			if c.runner == "rspec" && exec.Command(rspec[0], append(rspec[1:], "--version")...).Run() != nil {
				t.Skip("SKIP rspec: rspec does not run here")
			}
			p, _ := ByName(c.lang)
			inv := Cmd(c.argv)
			if c.throughScript {
				var err error
				if inv, err = Resolve(p, dir, c.argv, ""); err != nil || inv.Script == nil {
					t.Fatalf("Resolve(%q) = %+v, %v", c.argv, inv, err)
				}
			}
			if r, _ := RunnerFor(p, inv); r.Name != c.runner {
				t.Fatalf("%q dispatched to row %q", c.argv, r.Name)
			}
			run := func(cmd []string) (string, string) {
				var stdout, all bytes.Buffer
				x := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the row's own command over a test fixture
				x.Dir = dir
				x.Stdout = &stdout
				x.Stderr = &all
				_ = x.Run()
				return stdout.String(), stdout.String() + all.String()
			}
			list, ok := ListTestsCmd(p, inv, c.testPath)
			if !ok {
				t.Fatal("no list command")
			}
			out, _ := run(list)
			ids := ParseTestList(p, inv, out)
			id, ok := ResolveForRun(p, inv, ids, c.name)
			if !ok || id != c.want {
				t.Fatalf("%q resolved to %q, %v over the listed %q", c.name, id, ok, ids)
			}
			one, ok := SingleTestCmd(p, inv, c.testPath, id)
			if !ok {
				t.Fatal("no single-test command")
			}
			if _, all := run(one); ran(all) != 1 {
				t.Errorf("%q ran %d tests, want 1:\n%s", one, ran(all), all)
			}
		})
	}
}

// ran reads each runner's own summary line, so the test above can tell one
// test run from two.
func TestRanReadsEachRunnersSummary(t *testing.T) {
	for out, want := range map[string]int{
		"Tests:       4 skipped, 1 passed, 5 total":           1,
		"      Tests  2 passed | 3 skipped (5)":               2,
		"  2 passing (3ms)":                                   2,
		"# pass 1\n# fail 0":                                  1,
		"ℹ pass 2":                                            2,
		"1 example, 0 failures":                               1,
		"OK (2 tests, 2 assertions)":                          2,
		"1 runs, 0 assertions, 0 failures, 0 errors, 0 skips": 1,
		"no summary at all":                                   -1,
	} {
		if got := ran(out); got != want {
			t.Errorf("ran(%q) = %d, want %d", out, got, want)
		}
	}
}

// TestRealRunnersSalvage drives salvage end to end against the real runner:
// run the operator's command over the failing fixture (testdata/runners/
// src/fail: three failing tests, one passing), read its failures through the
// row, rerun with the row's skip command, and require exactly the passing
// test to run, and pass. Skipped by name where the runner is absent, as
// TestRealRunnersListAndRunOne is.
func TestRealRunnersSalvage(t *testing.T) {
	root := os.Getenv("CORRALAI_RUNNER_ROOT")
	rspec := []string{"rspec"}
	if exec.Command("rspec", "--version").Run() != nil {
		rspec = []string{"ruby", "-S", "rspec"}
	}
	for _, c := range []struct {
		runner, lang  string
		argv          []string
		needsRoot     bool
		throughScript bool
	}{
		{"node --test", "javascript", []string{"node", "--test", "fail/f.node.test.js"}, false, false},
		{"node --test", "javascript", []string{"node", "--test", "--test-reporter=spec", "fail/f.node.test.js"}, false, false},
		{"rspec", "ruby", append(slices.Clone(rspec), "fail/spec/f_spec.rb"), false, false},
		{"minitest", "ruby", []string{"ruby", "fail/f_test.rb"}, false, false},
		{"go test", "go", []string{"go", "test", "-v", "./..."}, false, false},
		{"jest", "javascript", []string{"npx", "--no-install", "jest", "fail/f.jest.test.js"}, true, false},
		{"vitest", "typescript", []string{"npx", "--no-install", "vitest", "run", "fail/f.vitest.test.mjs"}, true, false},
		{"mocha", "javascript", []string{"npx", "--no-install", "mocha", "fail/f.mocha.js"}, true, false},
		{"phpunit", "php", []string{"vendor/bin/phpunit", "fail/tests/FTest.php"}, true, false},
		{"jest", "javascript", []string{"npm", "test", "--", "fail/f.jest.test.js"}, true, true},
		{"phpunit", "php", []string{"composer", "test", "--", "fail/tests/FTest.php"}, true, true},
	} {
		t.Run(c.runner+"/"+c.argv[0], func(t *testing.T) {
			dir := root
			switch {
			case c.runner == "go test":
				dir = t.TempDir()
				writeGoFailFixture(t, dir)
			case !c.needsRoot:
				dir = t.TempDir()
				if err := os.CopyFS(dir, os.DirFS("testdata/runners/src")); err != nil {
					t.Fatal(err)
				}
			case dir == "":
				t.Skipf("SKIP %s: CORRALAI_RUNNER_ROOT is not set (scripts/capture-runner-fixtures.sh --keep DIR)", c.runner)
			}
			if _, err := exec.LookPath(c.argv[0]); err != nil && filepath.Base(c.argv[0]) == c.argv[0] {
				t.Skipf("SKIP %s: %s is not installed", c.runner, c.argv[0])
			}
			if c.runner == "rspec" && exec.Command(rspec[0], append(rspec[1:], "--version")...).Run() != nil {
				t.Skip("SKIP rspec: rspec does not run here")
			}
			if c.runner == "minitest" && exec.Command("ruby", "-e", `require "minitest"`).Run() != nil {
				t.Skip("SKIP minitest: minitest is not installed")
			}
			p, _ := ByName(c.lang)
			inv := Cmd(c.argv)
			if c.throughScript {
				var err error
				if inv, err = Resolve(p, dir, c.argv, ""); err != nil || inv.Script == nil {
					t.Fatalf("Resolve(%q) = %+v, %v", c.argv, inv, err)
				}
			}
			if r, _ := RunnerFor(p, inv); r.Name != c.runner {
				t.Fatalf("%q dispatched to row %q", c.argv, r.Name)
			}
			run := func(cmd []string) (string, error) {
				var out bytes.Buffer
				x := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the row's own command over a test fixture
				x.Dir = dir
				x.Stdout = &out
				x.Stderr = &out
				err := x.Run()
				return out.String(), err
			}
			out, err := run(c.argv)
			if err == nil {
				t.Fatalf("the failing fixture passed:\n%s", out)
			}
			failed, ok := DeselectorFor(p, inv)
			if !ok {
				t.Fatal("no salvage for this command")
			}
			ids := failed(out)
			if len(ids) != 3 {
				t.Fatalf("read %d failures %q, want 3, from:\n%s", len(ids), ids, out)
			}
			skip, ok := DeselectCmd(p, inv, ids)
			if !ok {
				t.Fatal("no skip command")
			}
			out, err = run(skip)
			if err != nil {
				t.Fatalf("%q failed: %v\n%s", skip, err, out)
			}
			if c.runner == "go test" {
				if !strings.Contains(out, "--- PASS: TestOK") || strings.Contains(out, "--- FAIL") {
					t.Errorf("%q:\n%s", skip, out)
				}
				return
			}
			if ran(out) != 1 {
				t.Errorf("%q ran %d tests, want 1:\n%s", skip, ran(out), out)
			}
		})
	}
}

// writeGoFailFixture is the go-test counterpart of src/fail: TestA fails in
// a subtest, TestB and TestC fail, TestOK passes.
func writeGoFailFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"go.mod": "module failfix\n\ngo 1.20\n",
		"f_test.go": `package failfix

import "testing"

func TestA(t *testing.T)  { t.Run("sub", func(t *testing.T) { t.Fatal("a") }) }
func TestB(t *testing.T)  { t.Fatal("b") }
func TestC(t *testing.T)  { t.Fatal("c") }
func TestOK(t *testing.T) {}
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRealRunnersListAndRunOneUnit drives each unit form end to end against
// the real runner over src/units: list the units the operator's command
// collects, then run the first alone and require only that file's tests to
// run (a.* holds two tests, b.* one, so a run of both reads three). Gated as
// TestRealRunnersListAndRunOne is.
func TestRealRunnersListAndRunOneUnit(t *testing.T) {
	root := os.Getenv("CORRALAI_RUNNER_ROOT")
	passed := regexp.MustCompile(`(?m)^\s*Tests:?\s+(\d+) passed|^# pass (\d+)`)
	for _, c := range []struct {
		runner, lang  string
		argv          []string
		want          []string // the listing, or (throughScript) units it must hold
		needsRoot     bool
		throughScript bool
	}{
		{"node --test", "javascript", []string{"node", "--test", "--test-concurrency", "1", "units/node/*.test.js"},
			[]string{"units/node/a.node.test.js", "units/node/b.node.test.js"}, false, false},
		{"jest", "javascript", []string{"npx", "--no-install", "jest", "--ci", "units/jest"},
			[]string{"units/jest/a.jest.test.js", "units/jest/b.jest.test.js"}, true, false},
		{"vitest", "typescript", []string{"npx", "--no-install", "vitest", "run", "--reporter=verbose", "units/vitest"},
			[]string{"units/vitest/a.vitest.test.mjs", "units/vitest/b.vitest.test.mjs"}, true, false},
		{"jest", "javascript", []string{"npm", "test"},
			[]string{"units/jest/a.jest.test.js", "units/jest/b.jest.test.js"}, true, true},
	} {
		t.Run(c.runner+"/"+c.argv[0], func(t *testing.T) {
			dir := root
			if !c.needsRoot {
				dir = t.TempDir()
				if err := os.CopyFS(dir, os.DirFS("testdata/runners/src")); err != nil {
					t.Fatal(err)
				}
			} else if dir == "" {
				t.Skipf("SKIP %s: CORRALAI_RUNNER_ROOT is not set (scripts/capture-runner-fixtures.sh --keep DIR)", c.runner)
			}
			if _, err := exec.LookPath(c.argv[0]); err != nil {
				t.Skipf("SKIP %s: %s is not installed", c.runner, c.argv[0])
			}
			p, _ := ByName(c.lang)
			inv := Cmd(c.argv)
			if c.throughScript {
				var err error
				if inv, err = Resolve(p, dir, c.argv, ""); err != nil || inv.Script == nil {
					t.Fatalf("Resolve(%q) = %+v, %v", c.argv, inv, err)
				}
			}
			run := func(cmd []string) (string, string) {
				var stdout, all bytes.Buffer
				x := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the row's own command over a test fixture
				x.Dir = dir
				x.Stdout = &stdout
				x.Stderr = &all
				_ = x.Run()
				return stdout.String(), stdout.String() + all.String()
			}
			list, ok := ListUnitsCmd(p, inv)
			if !ok {
				t.Fatal("no unit listing command")
			}
			out, all := run(list)
			units := ParseUnitList(p, inv, out)
			if c.throughScript {
				for _, w := range c.want {
					if !slices.Contains(units, w) {
						t.Fatalf("%q listed %q, missing %q:\n%s", list, units, w, all)
					}
				}
			} else if !slices.Equal(units, c.want) {
				t.Fatalf("%q listed %q, want %q:\n%s", list, units, c.want, all)
			}
			one, ok := UnitRunCmd(p, inv, c.want[0])
			if !ok {
				t.Fatal("no one-unit command")
			}
			_, all = run(one)
			m := passed.FindStringSubmatch(all)
			if m == nil || m[1]+m[2] != "2" || strings.Contains(all, "b."+strings.SplitN(filepath.Base(c.want[0]), ".", 2)[1]) {
				t.Errorf("%q did not run exactly %s's two tests:\n%s", one, c.want[0], all)
			}
		})
	}
}
