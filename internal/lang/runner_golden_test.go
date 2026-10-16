// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testdata/runner-golden.json was captured from the per-plugin methods the
// runner table replaced (commit 0eaba5b9, before any of them moved) and is
// never regenerated: it is the record of what corral did, and the table is
// held to it — every answer the plugins gave, exactly (goldenDiffs).

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

// captureNew asks the runner table the same questions captureOld asked the
// plugins. Output parsers are asked under EVERY corpus command and must give
// the golden's single answer under each: the old parsers never looked at the
// command, and the fallback to a language's Default row must keep it so.
func captureNew(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	outs := goldenOutputs(t)
	for _, n := range Names() {
		p, _ := ByName(n)
		for _, cmd := range goldenCommands() {
			key := strings.Join(cmd, " ")
			a, ok := FailFastArgsFor(p, Cmd(cmd))
			got[n+"|failfast|"+key] = show(a, ok)
			l, ok := ListTestsCmd(p, Cmd(cmd), "tests/test_x.py")
			got[n+"|list|"+key] = show(l, ok)
			s, ok := SingleTestCmd(p, Cmd(cmd), "tests/test_x.py", "tests::a")
			got[n+"|single|"+key] = show(s, ok)
			for _, dir := range []string{"testdata/rust/libtest", t.TempDir()} {
				base := filepath.Base(dir)
				if base != "libtest" {
					base = "empty-dir"
				}
				got[n+"|disclose|"+key+"|"+base] = show(RunDisclosures(p, dir, Cmd(cmd)))
			}
			for name, o := range outs {
				got[n+"|parselist|"+key+"|"+name] = show(ParseTestList(p, Cmd(cmd), o))
			}
		}
		for name, o := range outs {
			answers := map[string]map[string]bool{}
			note := func(c, v string) {
				if answers[c] == nil {
					answers[c] = map[string]bool{}
				}
				answers[c][v] = true
			}
			for _, cmd := range goldenCommands() {
				if fp := FailureParserFor(p, Cmd(cmd)); fp != nil {
					note("firstfailure", show(fp.FirstFailure([]byte(o))))
					if fr, ok := fp.(FlakyReporter); ok {
						note("flaky", show(fr.FlakyTests([]byte(o))))
					}
				}
			}
			if failed, deselect, ok := DeselectorFor(p, Cmd(p.TestCmd())); ok {
				f := failed(o)
				got[n+"|failedtests|"+name] = show(f)
				got[n+"|deselect|"+name] = show(deselect(f))
			}
			for c, vs := range answers {
				if len(vs) != 1 {
					t.Errorf("%s|%s|%s depends on the command: %v", n, c, name, vs)
				}
				for v := range vs {
					got[n+"|"+c+"|"+name] = v
				}
			}
		}
	}
	return got
}

func TestRunnerTableAnswersAsThePluginsDid(t *testing.T) {
	b, err := os.ReadFile("testdata/runner-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	for _, d := range goldenDiffs(want, captureNew(t)) {
		t.Error(d)
	}
}

// noAnswer is how the golden renders "the plugins had no answer here": a
// refused command, an empty list, an empty failure name.
var noAnswer = map[string]bool{"not-ok": true, "[]": true, `""`: true}

// writtenWhenHad are the questions captureNew asks only of a language whose
// runner HAS the capability (a FailureParser, a salvage pair): the plugins
// wrote no key at all where they had none, so a key missing from the golden
// is a no-answer, not a question the golden never asked.
var writtenWhenHad = map[string]bool{"firstfailure": true, "flaky": true, "failedtests": true, "deselect": true}

// goldenDiffs is every way got departs from the plugins' answers. The table
// may ADD an answer where the plugins had none — that is each later phase
// filling a capability, and it cannot change what any run did before — but
// never change an answer, and never answer a question the golden never asked.
func goldenDiffs(want, got map[string]string) []string {
	var diffs []string
	for k, w := range want {
		if g := got[k]; g != w && !noAnswer[w] {
			diffs = append(diffs, fmt.Sprintf("%s: table %s, golden %s", k, g, w))
		}
	}
	for k, g := range got {
		if _, ok := want[k]; !ok && !writtenWhenHad[goldenQuestion(k)] {
			diffs = append(diffs, fmt.Sprintf("%s: answered by the table (%s) but not by the plugins", k, g))
		}
	}
	slices.Sort(diffs)
	return diffs
}

// goldenQuestion is the question a golden key asks: its second field
// (language|question|...).
func goldenQuestion(k string) string {
	if f := strings.Split(k, "|"); len(f) > 1 {
		return f[1]
	}
	return ""
}

// The add-only rule must still catch a changed answer: the negative control
// for goldenDiffs, so relaxing it for new capabilities cannot silently relax
// it for old ones.
func TestGoldenAddOnlyRuleStillCatchesAChange(t *testing.T) {
	want := map[string]string{"a": `"x"`, "b": "not-ok", "c": "[]"}
	if d := goldenDiffs(want, map[string]string{"a": `"x"`, "b": `["new"]`, "c": `["new"]`}); len(d) != 0 {
		t.Errorf("an answer added where the plugins had none was refused: %v", d)
	}
	if d := goldenDiffs(want, map[string]string{"a": `"y"`, "b": "not-ok", "c": "[]"}); len(d) != 1 {
		t.Errorf("a changed answer was not caught: %v", d)
	}
	if d := goldenDiffs(want, map[string]string{"a": `"x"`, "b": "not-ok", "c": "[]", "d": `"z"`}); len(d) != 1 {
		t.Errorf("a key the plugins never answered was not caught: %v", d)
	}
	if d := goldenDiffs(want, map[string]string{"a": `"x"`, "b": "not-ok", "c": "[]", "ruby|firstfailure|out": `"t"`}); len(d) != 0 {
		t.Errorf("a failure parser the plugins lacked was refused: %v", d)
	}
}
