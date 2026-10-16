// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repoWith writes a repository holding the given files.
func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func resolve(t *testing.T, lang, dir string, argv []string, override string) (Plugin, Invocation) {
	t.Helper()
	p, _ := ByName(lang)
	inv, err := Resolve(p, dir, argv, override)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", argv, err)
	}
	return p, inv
}

const pkgJSON = `{"scripts": {
 "test": "jest",
 "test:env": "NODE_ENV=test jest",
 "test:compound": "jest --ci && eslint .",
 "test:vitest": "vitest run",
 "unit": "mocha"
}}`

// npm test runs package.json's "test" script, so it is jest's command: jest's
// fail-fast, list and one-test arguments reach jest through npm's "--".
// Measured on npm 10.9 (testdata/runners/PROVENANCE.txt).
func TestNpmTestIsTheRunnerInItsScript(t *testing.T) {
	dir := repoWith(t, map[string]string{"package.json": pkgJSON})
	for _, argv := range [][]string{{"npm", "test"}, {"npm", "t"}, {"npm", "run", "test"}, {"npm", "run-script", "test"}} {
		p, inv := resolve(t, "javascript", dir, argv, "")
		if r, _ := RunnerFor(p, inv); r.ID != "jest" {
			t.Errorf("%q ran as %q", argv, r.ID)
			continue
		}
		ff, ok := FailFastArgsFor(p, inv)
		if !ok || !slices.Equal(ff, []string{"--", "--bail"}) {
			t.Errorf("%q fail-fast = %q, %v", argv, ff, ok)
		}
		one, ok := SingleTestCmd(p, inv, "a.test.js", "top level")
		if want := append(slices.Clone(argv), "--", "--testNamePattern", "^top level$", "a.test.js"); !ok || !slices.Equal(one, want) {
			t.Errorf("%q single = %q, %v; want %q", argv, one, ok, want)
		}
		list, ok := ListTestsCmd(p, inv, "a.test.js")
		if want := append(slices.Clone(argv), "--", "--json", "--testNamePattern", `^\b$`, "a.test.js"); !ok || !slices.Equal(list, want) {
			t.Errorf("%q list = %q, %v; want %q", argv, list, ok, want)
		}
	}
}

// A separator the operator already wrote is not written twice: the extra
// arguments follow theirs.
func TestNpmTestKeepsTheOperatorsSeparator(t *testing.T) {
	dir := repoWith(t, map[string]string{"package.json": pkgJSON})
	p, inv := resolve(t, "javascript", dir, []string{"npm", "test", "--", "--ci"}, "")
	ff, ok := FailFastArgsFor(p, inv)
	if !ok || !slices.Equal(ff, []string{"--bail"}) {
		t.Errorf("fail-fast = %q, %v", ff, ok)
	}
	one, _ := SingleTestCmd(p, inv, "a.test.js", "x")
	if want := []string{"npm", "test", "--", "--ci", "--testNamePattern", "^x$", "a.test.js"}; !slices.Equal(one, want) {
		t.Errorf("single = %q, want %q", one, want)
	}
}

func TestScriptShapes(t *testing.T) {
	dir := repoWith(t, map[string]string{"package.json": pkgJSON})

	// An env assignment before the runner: still jest, and appended
	// arguments still reach it (measured: npm run test:env runs 1 test).
	p, inv := resolve(t, "javascript", dir, []string{"npm", "run", "test:env"}, "")
	if one, ok := SingleTestCmd(p, inv, "a.test.js", "x"); !ok || one[len(one)-1] != "a.test.js" {
		t.Errorf("env-prefixed script: single = %q, %v", one, ok)
	}

	// A compound script is detected, for the scan line and coverage, but
	// nothing is appended: --bail would reach eslint.
	p, inv = resolve(t, "javascript", dir, []string{"npm", "run", "test:compound"}, "")
	if r, _ := RunnerFor(p, inv); r.ID != "jest" {
		t.Errorf("compound script ran as %q", r.ID)
	}
	if _, ok := FailFastArgsFor(p, inv); ok {
		t.Error("a compound script got fail-fast")
	}
	if _, ok := SingleTestCmd(p, inv, "a.test.js", "x"); ok {
		t.Error("a compound script got a one-test command")
	}
	if _, ok := ListTestsCmd(p, inv, "a.test.js"); ok {
		t.Error("a compound script got a list command")
	}
	if d := inv.Describe(p); !strings.Contains(d, "not one command") {
		t.Errorf("Describe = %q", d)
	}

	// vitest's list REWRITES the subcommand, which cannot reach a script
	// through appended arguments; its one-test run only appends.
	p, inv = resolve(t, "typescript", dir, []string{"npm", "run", "test:vitest"}, "")
	if _, ok := ListTestsCmd(p, inv, "a.test.ts"); ok {
		t.Error("vitest list through a script was not refused")
	}
	if one, ok := SingleTestCmd(p, inv, "a.test.ts", "x"); !ok || !slices.Equal(one, []string{"npm", "run", "test:vitest", "--", "-t", "^x$", "a.test.ts"}) {
		t.Errorf("vitest single through a script = %q, %v", one, ok)
	}

	// Another script name reads that script.
	p, inv = resolve(t, "javascript", dir, []string{"npm", "run", "unit"}, "")
	if r, _ := RunnerFor(p, inv); r.ID != "mocha" {
		t.Errorf("npm run unit ran as %q", r.ID)
	}
}

func TestLaunchersAndTheirSeparators(t *testing.T) {
	dir := repoWith(t, map[string]string{"package.json": pkgJSON, "composer.json": `{"scripts": {"test": "phpunit", "arr": ["phpunit"], "two": ["phpunit", "phpstan"], "at": "@php vendor/bin/phpunit"}}`})
	for _, c := range []struct {
		lang string
		argv []string
		want []string // fail-fast args, nil = refused
	}{
		{"javascript", []string{"yarn", "test"}, []string{"--bail"}},
		{"javascript", []string{"yarn", "unit"}, []string{"--bail"}},
		{"javascript", []string{"pnpm", "test"}, []string{"--", "--bail"}},
		{"javascript", []string{"pnpm", "unit"}, []string{"--", "--bail"}},
		{"php", []string{"composer", "test"}, []string{"--", "--stop-on-failure"}},
		{"php", []string{"composer", "run-script", "arr"}, []string{"--", "--stop-on-failure"}},
		{"php", []string{"composer", "run", "two"}, nil},
		{"php", []string{"composer", "at"}, nil},
		{"javascript", []string{"yarn", "install"}, nil},
	} {
		p, inv := resolve(t, c.lang, dir, c.argv, "")
		got, ok := FailFastArgsFor(p, inv)
		if c.want == nil && ok || c.want != nil && !slices.Equal(got, c.want) {
			t.Errorf("%q: fail-fast = %q, %v; want %q", c.argv, got, ok, c.want)
		}
	}
}

// Nothing to read is not an error, but it is not argv alone either: `npm
// test` still runs a package script, one corral did not read, so nothing is
// appended to it and the scan line says so.
func TestAnUnreadableScriptGetsNoArguments(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no manifest":  {},
		"no script":    {"package.json": `{"scripts": {"build": "tsc"}}`},
		"bad json":     {"package.json": `{"scripts": `},
		"bad quotes":   {"package.json": `{"scripts": {"test": "jest 'oops"}}`},
		"empty script": {"package.json": `{"scripts": {"test": ""}}`},
	} {
		p, inv := resolve(t, "javascript", repoWith(t, files), []string{"npm", "test"}, "")
		if inv.Script == nil || len(inv.Script) != 0 || inv.From != "" {
			t.Errorf("%s: resolved %+v, want an unread script", name, inv)
		}
		if d := inv.Describe(p); !strings.Contains(d, "did not read") {
			t.Errorf("%s: Describe = %q", name, d)
		}
		if _, ok := FailFastArgsFor(p, inv); ok {
			t.Errorf("%s: npm test got fail-fast", name)
		}
	}
}

// --runner chooses the row. Its arguments are added only where the command
// words name that runner, since nothing else says they would reach it.
func TestRunnerOverride(t *testing.T) {
	p, inv := resolve(t, "javascript", t.TempDir(), []string{"make", "test"}, "jest")
	if r, _ := RunnerFor(p, inv); r.ID != "jest" {
		t.Errorf("--runner jest ran as %q", r.ID)
	}
	if _, ok := FailFastArgsFor(p, inv); ok {
		t.Error("make test got jest's --bail")
	}
	if _, ok := SingleTestCmd(p, inv, "a.test.js", "x"); ok {
		t.Error("make test got jest's one-test command")
	}
	if d := inv.Describe(p); d != "make test → jest (--runner)" {
		t.Errorf("Describe = %q", d)
	}

	// A command that names the runner itself keeps its capabilities.
	p, inv = resolve(t, "javascript", t.TempDir(), []string{"npx", "jest"}, "jest")
	if _, ok := FailFastArgsFor(p, inv); !ok {
		t.Error("npx jest under --runner jest lost fail-fast")
	}
	// ...but not when --runner names a different row than the command does.
	p, inv = resolve(t, "javascript", t.TempDir(), []string{"npx", "jest"}, "mocha")
	if _, ok := FailFastArgsFor(p, inv); ok {
		t.Error("npx jest under --runner mocha got a fail-fast flag")
	}

	py, _ := ByName("python")
	if _, err := Resolve(py, t.TempDir(), []string{"pytest"}, "jest"); err == nil || !strings.Contains(err.Error(), "pytest, unittest") {
		t.Errorf("--runner jest on python: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	dir := repoWith(t, map[string]string{"package.json": `{"scripts": {"test": "jest", "other": "node scripts/run.js"}}`})
	p, inv := resolve(t, "javascript", dir, []string{"npm", "test"}, "")
	if d := inv.Describe(p); d != "npm test → jest (package.json scripts.test)" {
		t.Errorf("Describe = %q", d)
	}
	if got := RunDisclosures(p, dir, inv); len(got) != 1 || got[0] != inv.Describe(p) {
		t.Errorf("RunDisclosures = %q; the scan line must say how the runner was found", got)
	}
	p, inv = resolve(t, "javascript", dir, []string{"npm", "run", "other"}, "")
	if d := inv.Describe(p); !strings.Contains(d, "no runner corral recognises") {
		t.Errorf("Describe = %q", d)
	}
	if d := Cmd([]string{"npx", "jest"}).Describe(p); d != "" {
		t.Errorf("an unresolved command described itself: %q", d)
	}
}

func TestSplitScript(t *testing.T) {
	for in, want := range map[string][]string{
		`jest --ci`:                      {"jest", "--ci"},
		`NODE_ENV=test  jest`:            {"NODE_ENV=test", "jest"},
		`jest -t "a b" 'c d'`:            {"jest", "-t", "a b", "c d"},
		`cross-env CI=1 jest --config=x`: {"cross-env", "CI=1", "jest", "--config=x"},
	} {
		if got, ok := splitScript(in); !ok || !slices.Equal(got, want) {
			t.Errorf("splitScript(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := splitScript(`jest "unclosed`); ok {
		t.Error("unbalanced quotes split")
	}
}

// The fixture manifests are real ones — a name, private, a require block —
// not a bare {"scripts": ...}: a reader that only handled the bare shape
// passed every other test here and read no real repository at all.
func TestResolveReadsRealManifests(t *testing.T) {
	src := "testdata/runners/src"
	for _, c := range []struct {
		lang string
		argv []string
		row  string
	}{
		{"javascript", []string{"npm", "test"}, "jest"},
		{"javascript", []string{"npm", "run", "test:env"}, "jest"},
		{"php", []string{"composer", "test"}, "phpunit"},
	} {
		p, inv := resolve(t, c.lang, src, c.argv, "")
		if inv.Script == nil || inv.From == "" {
			t.Errorf("%q: nothing resolved from %s: %+v", c.argv, src, inv)
			continue
		}
		if r, _ := RunnerFor(p, inv); r.ID != c.row {
			t.Errorf("%q ran as %q", c.argv, r.ID)
		}
	}
	dir := repoWith(t, map[string]string{"composer.json": `{"name": "a/b", "minimum-stability": "dev", "require": {"php": ">=8.1"}, "scripts": {"test": "phpunit"}}`})
	if _, inv := resolve(t, "php", dir, []string{"composer", "test"}, ""); inv.Script == nil {
		t.Error("a composer.json with string and object siblings of scripts was not read")
	}
}

// Once argv runs a package script, nothing is appended to it unless the
// script was read and names a runner corral knows: not for a script naming
// some other tool (pest, artisan), not for one corral could not read, and not
// when a launcher flag points it at another package's manifest.
func TestNothingIsAppendedToAScriptCorralDidNotRead(t *testing.T) {
	for name, c := range map[string]struct {
		lang  string
		files map[string]string
		argv  []string
	}{
		"unrecognised tool":    {"php", map[string]string{"composer.json": `{"scripts": {"test": "pest"}}`}, []string{"composer", "test"}},
		"artisan":              {"php", map[string]string{"composer.json": `{"scripts": {"test": "php artisan test"}}`}, []string{"composer", "test"}},
		"two-command array":    {"php", map[string]string{"composer.json": `{"scripts": {"test": ["phpunit", "phpstan"]}}`}, []string{"composer", "test"}},
		"no manifest":          {"php", map[string]string{}, []string{"composer", "test"}},
		"composer working-dir": {"php", map[string]string{"composer.json": `{"scripts": {"test": "phpunit"}}`}, []string{"composer", "--working-dir=pkg", "test"}},
		"npm workspace":        {"javascript", map[string]string{"package.json": pkgJSON}, []string{"npm", "test", "--workspace=web"}},
		"pnpm filter":          {"javascript", map[string]string{"package.json": pkgJSON}, []string{"pnpm", "--filter", "web", "test"}},
	} {
		p, inv := resolve(t, c.lang, repoWith(t, c.files), c.argv, "")
		if cmd, ok := ListTestsCmd(p, inv, "tests/ATest.php"); ok {
			t.Errorf("%s: list = %q", name, cmd)
		}
		if cmd, ok := SingleTestCmd(p, inv, "tests/ATest.php", "ATest::testA"); ok {
			t.Errorf("%s: single = %q", name, cmd)
		}
		if args, ok := FailFastArgsFor(p, inv); ok {
			t.Errorf("%s: fail-fast = %q", name, args)
		}
	}
}
