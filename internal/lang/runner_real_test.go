// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// ranCount reads how many tests a runner says it ran and passed, from its
// own summary line (jest, vitest, mocha, node's TAP or spec reporter, rspec,
// PHPUnit), or -1.
var ranCount = regexp.MustCompile(`(?m)(\d+) passed|(\d+) passing|^[#ℹ] pass (\d+)|(\d+) examples?, 0 failures|OK \((\d+) tests?`)

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
		"Tests:       4 skipped, 1 passed, 5 total": 1,
		"      Tests  2 passed | 3 skipped (5)":     2,
		"  2 passing (3ms)":                         2,
		"# pass 1\n# fail 0":                        1,
		"ℹ pass 2":                                  2,
		"1 example, 0 failures":                     1,
		"OK (2 tests, 2 assertions)":                2,
		"no summary at all":                         -1,
	} {
		if got := ran(out); got != want {
			t.Errorf("ran(%q) = %d, want %d", out, got, want)
		}
	}
}
