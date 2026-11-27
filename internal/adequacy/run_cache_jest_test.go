// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// THE SAFETY ARGUMENT FOR A SHARED JEST CACHE DIRECTORY, run against the
// real jail (go's is run_cache_replay_test.go, cargo's
// run_cache_cargo_test.go).
//
// jest keeps no test RESULT it could serve in place of a run. Its cache
// directory holds transformed code, under a key that hashes each file's
// bytes; module metadata, reused for a file whose mtime is unchanged (every
// run's workspace is written after the warming run ends, so no file is); and
// the last duration and outcome of each test file, which orders them and,
// under --onlyFailures alone, selects them (lang.jestRunCache). These tests
// run that argument with jest's own code:
//
//   - a frozen run of the unmutated code transforms nothing (the cache is
//     shared and used), and a mutant's run transforms the mutated file again
//     and fails (it is graded on its own code);
//   - with a transformer whose cache key ignores the file's bytes the mutant
//     passes: the one way the transform cache can grade a mutant as the
//     original, a project transformer's own defect, shown to be real;
//   - with --onlyFailures a mutant's frozen run reads the warming run's
//     passes and runs nothing, which is why lang refuses such a command a
//     shared cache.
//
// jest comes from CORRALAI_JEST_NODE_MODULES, a node_modules holding the
// runner-fixture pins (scripts/npm-pins/runner-fixtures, jest 30.5.2), bound
// into the jail as the project's own, as CI provisions it.

// jestCacheFiles is a project whose test expects add(1, 2) to be 3, with add
// computing a <op> b, run through transformer (a file of jestTransformers).
func jestCacheFiles(op, transformer string) map[string]string {
	return map[string]string{
		"package.json": `{"name":"p","private":true,"jest":{"transform":{"\\.js$":"./transform.js"}}}` + "\n",
		"transform.js": jestTransformers[transformer],
		"calc.js":      "function add(a, b) { return a " + op + " b; }\nmodule.exports = { add };\n",
		"calc.test.js": "const { add } = require('./calc');\ntest('adds', () => { expect(add(1, 2)).toBe(3); });\n",
	}
}

// jestTransformers print "corral-transformed <file>" each time jest asks them
// to transform a file, which it does only when its cache has no entry under
// the file's key. "content" keys on the file's bytes, as jest's own
// transformers do; "name" keys on its name alone.
var jestTransformers = map[string]string{
	"content": jestTransformer("src + '\\0' + path"),
	"name":    jestTransformer("path"),
}

func jestTransformer(key string) string {
	return "const crypto = require('crypto');\nconst base = require('path').basename;\nmodule.exports = {\n" +
		"  getCacheKey(src, path) { return crypto.createHash('sha1').update(" + key + ").digest('hex'); },\n" +
		"  process(src, path) { process.stderr.write('corral-transformed ' + base(path) + '\\n'); return { code: src }; },\n};\n"
}

// jestBase is the command every run starts from: jest's entry script, run in
// band so the transformer's lines reach this process.
var jestBase = []string{"node", "./node_modules/jest/bin/jest.js", "-i"}

// jestCacheJail is a jail sharing the jest row's cache, with the runner
// fixtures' node_modules bound read-only as the project's.
func jestCacheJail(t *testing.T) (RunCacheJail, lang.Plugin) {
	t.Helper()
	nm := os.Getenv("CORRALAI_JEST_NODE_MODULES")
	if nm == "" {
		t.Skip("SKIP: CORRALAI_JEST_NODE_MODULES is not set (scripts/ci-provision-test-toolchains.sh installs the runner-fixture pins and sets it)")
	}
	if _, err := os.Stat(nm + "/jest/bin/jest.js"); err != nil {
		t.Fatalf("CORRALAI_JEST_NODE_MODULES=%s holds no jest: %v", nm, err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("SKIP: node not on PATH")
	}
	backend := overlayJailOrSkip(t)
	t.Setenv("TMPDIR", t.TempDir())
	js, _ := lang.ByName("javascript")
	cache, _ := lang.RunCacheFor(js, lang.Invocation{Argv: jestBase}, lang.NoTree)
	ok := cache != nil
	if !ok || cache.Env != "" || cache.Args == nil {
		t.Fatalf("the jest row's run cache = %+v, %v; want one named by the command", cache, ok)
	}
	j := NewJail(backend, 2*time.Minute, WithReadOnlyBinds([]DepBind{{Host: nm, Rel: "node_modules"}}), WithRunCache(cache)).(RunCacheJail)
	t.Cleanup(func() { _ = j.Close() })
	if c := j.RunCache(); c.Target == "" {
		t.Skipf("SKIP: no shared cache on this host (%s)", c.Unshared)
	}
	return j, js
}

// jestCached is cmd as grading hands it to the jail: through lang.RunCacheCmd,
// which must read it and point it at the shared cache.
func jestCached(t *testing.T, js lang.Plugin, cmd []string) []string {
	t.Helper()
	out, state := lang.RunCacheCmd(js, lang.Invocation{Argv: cmd}, cmd)
	if state != lang.RunCacheRewritten || !strings.Contains(strings.Join(out, " "), "--cacheDirectory=/corral-cache") {
		t.Fatalf("RunCacheCmd(%v) = %v, %v; want it rewritten to the shared cache", cmd, out, state)
	}
	return out
}

func jestRun(t *testing.T, j RunCacheJail, files map[string]string, cmd []string) (bool, string) {
	t.Helper()
	ok, out, err := j.(bwrapJail).RunTestVerbose(context.Background(), files, cmd)
	if err != nil {
		t.Fatalf("run %v: %v\n%s", cmd, err, out)
	}
	return ok, out
}

func jestWarm(t *testing.T, j RunCacheJail, files map[string]string, cmd []string) {
	t.Helper()
	ok, out, err := j.WarmRun(context.Background(), files, cmd)
	if err != nil || !ok {
		t.Fatalf("warm run: ok=%v err=%v\n%s", ok, err, out)
	}
	if !strings.Contains(string(out), "corral-transformed calc.js") {
		t.Fatalf("the warming run transformed nothing, so the test cannot tell a shared cache from none:\n%s", out)
	}
}

func TestASharedJestCacheGradesEveryRunOnItsOwnCode(t *testing.T) {
	j, js := jestCacheJail(t)
	cmd := jestCached(t, js, jestBase)
	jestWarm(t, j, jestCacheFiles("+", "content"), cmd)
	for i, op := range []string{"-", "+", "*", "+"} {
		ok, out := jestRun(t, j, jestCacheFiles(op, "content"), cmd)
		if want := op == "+"; ok != want {
			t.Fatalf("frozen run %d (a %s b): passed=%v, want %v: the run was not graded on its own code\n%s", i, op, ok, want, out)
		}
		if transformed := strings.Contains(out, "corral-transformed calc.js"); transformed == (op == "+") {
			t.Errorf("frozen run %d (a %s b): transformed calc.js=%v: a changed file must be transformed again and an unchanged one read from the shared cache\n%s", i, op, transformed, out)
		}
		if strings.Contains(out, "corral-transformed calc.test.js") {
			t.Errorf("frozen run %d transformed the unchanged test file: the cache was not shared\n%s", i, out)
		}
	}
}

// The hazard the transform cache's key answers: a key that ignores the
// file's bytes hands the mutant the original's transformed code.
func TestAJestTransformerKeyedOnTheNameAloneGradesAMutantAsTheOriginal(t *testing.T) {
	j, js := jestCacheJail(t)
	cmd := jestCached(t, js, jestBase)
	jestWarm(t, j, jestCacheFiles("+", "name"), cmd)
	if ok, out := jestRun(t, j, jestCacheFiles("-", "name"), cmd); !ok {
		t.Fatalf("the mutant failed under a name-keyed transformer: the control no longer shows a stale transform, so the safety test may not be able to fail\n%s", out)
	}
}

// --onlyFailures selects by the perf cache: a mutant's frozen run reads the
// warming run's passes and runs no test, and jest 30.5.2 then exits 1 (0
// under --passWithNoTests), so every run after the warming one is graded
// without running a test. lang refuses such a command a shared cache; this
// is the run that shows why.
func TestJestOnlyFailuresOverASharedCacheRunsNoTest(t *testing.T) {
	j, js := jestCacheJail(t)
	only := append(append([]string{}, jestBase...), "--onlyFailures")
	if c, _ := lang.RunCacheFor(js, lang.Invocation{Argv: only}, lang.NoTree); c != nil {
		t.Fatalf("lang offers %v a shared cache", only)
	}
	cmd := jestCached(t, js, jestBase)
	jestWarm(t, j, jestCacheFiles("+", "content"), cmd)
	for _, extra := range [][]string{{"--onlyFailures"}, {"--onlyFailures", "--passWithNoTests"}} {
		run := append(append([]string{}, cmd...), extra...)
		ok, out := jestRun(t, j, jestCacheFiles("-", "content"), run)
		if !strings.Contains(out, "No failed test found") || strings.Contains(out, "Tests:") {
			t.Fatalf("%v over the warm cache ran a test (passed=%v): the control no longer shows the hazard\n%s", extra, ok, out)
		}
		if want := len(extra) == 2; ok != want {
			t.Errorf("%v: passed=%v, want %v\n%s", extra, ok, want, out)
		}
	}
}

// The unit evidence pass shares the jest row's cache among its units
// (lang.EvidenceCacheFor, its Args on each unit command), and JavaScript's
// evidence is V8 coverage of what jest compiles, mapped back to the source.
// Over one seed, the warming run and a frozen run of that command report the
// same coverage for every file but one, and the frozen run transforms
// nothing: its code comes from the shared cache and still maps to the file.
// The one file is the project's own transformer, whose process() runs in the
// warming run and not in a frozen one, so a frozen run reports fewer of its
// lines run (1 of 2 against 2 of 2, measured on jest 30.5.2); it is still a
// file the run executed, which is what pairing reads.
func TestJestCoverageOverASharedCacheReportsTheSameEvidence(t *testing.T) {
	nm := os.Getenv("CORRALAI_JEST_NODE_MODULES")
	if nm == "" {
		t.Skip("SKIP: CORRALAI_JEST_NODE_MODULES is not set (scripts/ci-provision-test-toolchains.sh installs the runner-fixture pins and sets it)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("SKIP: node not on PATH")
	}
	backend := overlayJailOrSkip(t)
	t.Setenv("TMPDIR", t.TempDir())
	js, _ := lang.ByName("javascript")
	cache := lang.EvidenceCacheFor(js, lang.Invocation{Argv: jestBase})
	if cache == nil || cache.Args == nil {
		t.Fatalf("the jest row's evidence cache is %+v, want one named by the command", cache)
	}
	cov, ok := js.(lang.CoverageReporter).CoverageCmd(append(append([]string{}, jestBase...), cache.Args(sandbox.CacheTarget)...))
	if !ok {
		t.Fatal("no coverage command for jest")
	}
	e := NewEnumerator(backend, 2*time.Minute, WithReadOnlyBinds([]DepBind{{Host: nm, Rel: "node_modules"}}))
	files := jestCacheFiles("+", "content")
	s, err := e.(bwrapJail).Seed(files, cache)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if c := s.Cache(); c.Target == "" {
		t.Skipf("SKIP: no shared cache on this host (%s)", c.Unshared)
	}
	warm, err := s.Warm(context.Background(), files, cov)
	if err != nil || warm.ExitCode != 0 {
		t.Fatalf("warm coverage run: %+v err=%v", warm, err)
	}
	frozen, err := s.EnumerateDetailed(context.Background(), files, cov)
	if err != nil || frozen.ExitCode != 0 {
		t.Fatalf("frozen coverage run: %+v err=%v", frozen, err)
	}
	// The report from its header on, one line per file, without the
	// transformer's count line, which is checked apart.
	report := func(out string) (rest, transformer string) {
		i := strings.Index(out, "corral-js-coverage: ")
		if i < 0 {
			return "", ""
		}
		var keep []string
		for _, l := range strings.Split(strings.TrimSpace(out[i:]), "\n") {
			if strings.HasSuffix(l, " transform.js") && !strings.HasPrefix(l, "#") {
				transformer = l
				continue
			}
			keep = append(keep, l)
		}
		return strings.Join(keep, "\n"), transformer
	}
	warmRest, warmT := report(warm.Output)
	frozenRest, frozenT := report(frozen.Output)
	if !strings.Contains(warmRest, " calc.js") || !strings.Contains(warmRest, " calc.test.js") {
		t.Fatalf("the warm run's coverage does not name calc.js and calc.test.js:\n%s", warm.Output)
	}
	if frozenRest != warmRest {
		t.Errorf("the frozen run's coverage differs from the warm run's:\nwarm   %q\nfrozen %q", warmRest, frozenRest)
	}
	if warmT != "2 2 transform.js" || frozenT != "1 2 transform.js" {
		t.Errorf("the transformer's coverage: warm %q, frozen %q; want 2 of 2 lines run warm and 1 of 2 frozen (process() runs only on a cache miss)", warmT, frozenT)
	}
	if !strings.Contains(warm.Output, "corral-transformed calc.js") || strings.Contains(frozen.Output, "corral-transformed") {
		t.Errorf("want the warming run to transform and the frozen run to transform nothing:\nwarm   %s\nfrozen %s", warm.Output, frozen.Output)
	}
}

// TypeScript through babel-jest, the TypeScript projects lang shares a
// cache with (one that uses ts-jest shares none, lang.jestUsesTSJest):
// Babel compiles each file on its own, so an import's value is read at run
// time, not compiled into the importer, and a mutant of the imported file
// fails over the shared cache though the importer's cached output is
// unchanged. The mutant is the one that ts-jest 26.5.6 graded as the
// original over a warm cache: a const enum member changed in the imported
// file. Needs @babel/preset-typescript and @babel/preset-env beside jest
// (the runner-fixture pins).
func TestASharedJestCacheGradesABabelTypeScriptImportOnItsOwnCode(t *testing.T) {
	j, js := jestCacheJail(t)
	nm := os.Getenv("CORRALAI_JEST_NODE_MODULES")
	for _, p := range []string{"@babel/preset-typescript", "@babel/preset-env"} {
		if _, err := os.Stat(nm + "/" + p + "/package.json"); err != nil {
			t.Fatalf("CORRALAI_JEST_NODE_MODULES=%s holds no %s (scripts/npm-pins/runner-fixtures pins it): %v", nm, p, err)
		}
	}
	files := func(member string) map[string]string {
		return map[string]string{
			"package.json":    `{"name":"p","private":true}` + "\n",
			"babel.config.js": "module.exports = { presets: [['@babel/preset-env', { targets: { node: 'current' } }], '@babel/preset-typescript'] };\n",
			"e.ts":            "export const enum E { A = " + member + " }\n",
			"f.ts":            "import { E } from './e';\nexport function f(): number { return E.A; }\n",
			"f.test.ts":       "import { f } from './f';\ntest('f', () => { expect(f()).toBe(1); });\n",
		}
	}
	cmd := jestCached(t, js, jestBase)
	if ok, out, err := j.WarmRun(context.Background(), files("1"), cmd); err != nil || !ok {
		t.Fatalf("warm run: ok=%v err=%v\n%s", ok, err, out)
	}
	for i, member := range []string{"2", "1", "3", "1"} {
		ok, out := jestRun(t, j, files(member), cmd)
		if want := member == "1"; ok != want {
			t.Fatalf("frozen run %d (A = %s): passed=%v, want %v: the run was not graded on its own code\n%s", i, member, ok, want, out)
		}
	}
}
