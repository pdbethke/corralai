// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateRunnerGolden = flag.Bool("update-runner-golden", false, "rewrite testdata/runner-golden.json from the current code")

// goldenCommands is every command shape the runner table must answer for
// exactly as the per-plugin methods did: each plugin's stock command, the
// operator commands in docs and the sweep, wrappers no row recognises, and
// shell-wrapped forms. Every plugin is asked about every command, so a
// command claimed by the wrong language shows up too.
func goldenCommands() [][]string {
	cmds := [][]string{
		{"go", "test", "./..."}, {"go", "test", "-run", "TestX", "./pkg/..."}, {"gotestsum", "--", "./..."}, {"make", "test"},
		{"python3", "-m", "pytest", "-q"}, {".venv/bin/pytest", "-x"}, {"python3", "-m", "unittest", "discover"}, {"tox"},
		{"sh", "-c", "pytest -q"},
		{"node", "--test"}, {"node", "--experimental-strip-types", "--test"}, {"npx", "jest"}, {"npx", "mocha"}, {"npx", "vitest", "run"}, {"npm", "test"},
		{"rspec"}, {"bundle", "exec", "rspec"}, {"ruby", "-Itest", "test/t_test.rb"},
		{"vendor/bin/phpunit"}, {"./tools/phpunit/vendor/bin/phpunit"}, {"composer", "test"},
		{"cargo", "test"}, {"cargo", "test", "-p", "fuzzy_match"}, {"cargo", "test", "--", "--test-threads", "1"},
		{"cargo", "nextest", "run"}, {"cargo", "nextest", "run", "-p", "x", "--", "--nocapture"}, {"cargo", "nextest", "run", "-E", "test(fuzzy)"},
	}
	for _, n := range Names() {
		p, _ := ByName(n)
		cmds = append(cmds, p.TestCmd())
	}
	return cmds
}

// goldenOutputs are runner outputs the parsers must read identically: every
// captured file under testdata/rust, the outputs the per-language parser
// tests hold as package constants, and the literal list/summary outputs
// those tests pass inline.
func goldenOutputs(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{
		"empty":                     "",
		"prose":                     "FAILED because the venv was missing\n",
		"py-short-summary":          pytestShortSummary,
		"py-collection-error":       pytestCollectionError,
		"py-error-before-failed":    pytestErrorBeforeFailed,
		"py-no-summary":             pytestNoSummary,
		"py-collect-only":           "tests/test_recipes.py::TakeTests::test_take\ntests/test_recipes.py::TakeTests::test_negative_take\n\n2 tests collected in 0.01s\n",
		"py-passed":                 "13 passed in 0.06s",
		"py-collected-none":         "collected 0 items",
		"go-parent-and-subtest":     goTestParentAndSubtest,
		"go-subtest-first":          goTestSubtestFirst,
		"go-no-summary":             goTestNoSummary,
		"go-build-failure":          goBuildFailure,
		"go-list":                   "TestTake\nTestNegativeTake\nExampleTake\nok  \tgithub.com/x/recipes\t0.002s\n",
		"php-failure-summary":       phpunitFailureSummary,
		"php-fatal-error":           phpunitFatalError,
		"php-clean-pass":            phpunitCleanPass,
		"rust-libtest-list":         "   Compiling demo v0.1.0 (/tmp/demo)\n     Running unittests src/lib.rs (target/debug/deps/demo-1)\ninline::a: test\ntests::a: test\ntests::b: test\n\n3 tests, 0 benchmarks\n",
		"rust-nextest-list":         "   Compiling demo v0.1.0 (/tmp/demo)\ndemo inline::a\ndemo inline::a_longer\ndemo tests::a\ndemo tests::b\ndemo::integ a\n",
		"py-failed-with-error-tail": "=========================== short test summary info ============================\nFAILED tests/test_cli_corral.py::test_with_appcontext_ctx_invoke - AttributeError: ...\nFAILED tests/test_cli_corral.py::test_set_app_option - assert 2 == 0\n3 failed, 10 passed in 0.06s",
	}
	err := filepath.WalkDir("testdata/rust", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".txt" || filepath.Base(path) == "PROVENANCE.txt" {
			return err
		}
		b, err := os.ReadFile(path) // #nosec G304 -- test fixture under testdata
		if err != nil {
			return err
		}
		out[filepath.ToSlash(path)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func show(v any, ok ...bool) string {
	if len(ok) == 1 && !ok[0] {
		return "not-ok"
	}
	s := fmt.Sprintf("%q", v)
	if len(s) > 256 {
		// A long answer (a list parser reading a whole captured run) is kept
		// as its digest: still an exact comparison, without a megabyte golden.
		return fmt.Sprintf("sha256:%x len=%d", sha256.Sum256([]byte(s)), len(s))
	}
	return s
}

// captureOld runs TODAY's per-plugin methods over the corpus. After the
// plugin methods are deleted this function goes with them; the JSON it
// wrote stays as the reference the runner table is held to.
func captureOld(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	outs := goldenOutputs(t)
	for _, n := range Names() {
		p, _ := ByName(n)
		tt, targeter := p.(CommandTestTargeter)
		for _, cmd := range goldenCommands() {
			key := strings.Join(cmd, " ")
			a, ok := FailFastArgsFor(p, cmd)
			got[n+"|failfast|"+key] = show(a, ok)
			if targeter {
				l, ok := tt.ListTestsCmdFor(cmd)
				got[n+"|list|"+key] = show(l, ok)
				s, ok := tt.SingleTestCmdFor(cmd, "tests::a")
				got[n+"|single|"+key] = show(s, ok)
			} else {
				l, ok := p.ListTestsCmd("tests/test_x.py")
				got[n+"|list|"+key] = show(l, ok)
				s, ok := p.SingleTestCmd("tests/test_x.py", "tests::a")
				got[n+"|single|"+key] = show(s, ok)
			}
			for _, dir := range []string{"testdata/rust/libtest", t.TempDir()} {
				base := filepath.Base(dir)
				if base != "libtest" {
					base = "empty-dir"
				}
				got[n+"|disclose|"+key+"|"+base] = show(RunDisclosures(p, dir, cmd))
			}
			for name, o := range outs {
				if targeter {
					got[n+"|parselist|"+key+"|"+name] = show(tt.ParseTestListFor(cmd, o))
				} else {
					got[n+"|parselist|"+key+"|"+name] = show(p.ParseTestList(o))
				}
			}
		}
		for name, o := range outs {
			if fp, ok := p.(FailureParser); ok {
				got[n+"|firstfailure|"+name] = show(fp.FirstFailure([]byte(o)))
			}
			if fr, ok := p.(FlakyReporter); ok {
				got[n+"|flaky|"+name] = show(fr.FlakyTests([]byte(o)))
			}
			if fd, ok := p.(FailureDeselector); ok {
				f := fd.FailedTests(o)
				got[n+"|failedtests|"+name] = show(f)
				got[n+"|deselect|"+name] = show(fd.DeselectArgs(f))
			}
		}
	}
	return got
}

func TestRunnerGoldenIsCurrent(t *testing.T) {
	got := captureOld(t)
	path := "testdata/runner-golden.json"
	if *updateRunnerGolden {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d keys", len(keys))
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden: run go test ./internal/lang -run TestRunnerGoldenIsCurrent -update-runner-golden (%v)", err)
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %s, golden %s", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("key count: got %d, golden %d", len(got), len(want))
	}
}
