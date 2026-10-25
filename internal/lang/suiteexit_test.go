// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// suiteExitProject is a smallest project per reporter whose one test passes
// or fails on the substitution of OUTCOME, and the exit status its runner
// gives a failing suite (cargo test exits 101, the others 1).
var suiteExitProject = map[string]struct {
	tool    string
	probe   []string
	testCmd []string
	files   map[string]string
	failRC  int
}{
	"go": {tool: "go", testCmd: []string{"go", "test", "-count=1", "./..."}, failRC: 1, files: map[string]string{
		"go.mod":    "module sx\n\ngo 1.22\n",
		"a.go":      "package sx\n\nfunc One() int { return 1 }\n",
		"a_test.go": "package sx\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif !(OUTCOME) || One() != 1 {\n\t\tt.Fatal(\"failed\")\n\t}\n}\n",
	}},
	"python": {tool: "python3", probe: []string{"python3", "-c", "import coverage, pytest"}, testCmd: []string{"python3", "-m", "pytest", "-q", "-p", "no:cacheprovider"}, failRC: 1, files: map[string]string{
		"a.py":      "def one():\n    return 1\n",
		"test_a.py": "from a import one\n\n\ndef test_one():\n    assert OUTCOME and one() == 1\n",
	}},
	"javascript": {tool: "node", testCmd: []string{"node", "--test"}, failRC: 1, files: map[string]string{
		"lib/a.js":       "exports.one = () => 1;\n",
		"test/a.test.js": "const assert = require('node:assert');\nconst { test } = require('node:test');\nconst { one } = require('../lib/a');\ntest('one', () => { assert.ok(OUTCOME && one() === 1); });\n",
	}},
	"typescript": {tool: "node", testCmd: []string{"node", "--test"}, failRC: 1, files: map[string]string{
		"package.json":   `{ "name": "sx", "private": true, "type": "module" }` + "\n",
		"lib/a.ts":       "export function one(): number { return 1; }\n",
		"test/a.test.ts": "import assert from 'node:assert';\nimport { test } from 'node:test';\nimport { one } from '../lib/a.ts';\ntest('one', () => { assert.ok(OUTCOME && one() === 1); });\n",
	}},
	"ruby": {tool: "ruby", testCmd: []string{"ruby", "-Ilib", "test/a_test.rb"}, failRC: 1, files: map[string]string{
		"lib/a.rb":       "def one\n  1\nend\n",
		"test/a_test.rb": "require 'minitest/autorun'\nrequire_relative '../lib/a'\n\nclass ATest < Minitest::Test\n  def test_one\n    assert(OUTCOME && one == 1)\n  end\nend\n",
	}},
	"php": {tool: "php", probe: []string{"php", "-r", `exit(extension_loaded("pcov") ? 0 : 1);`}, testCmd: []string{"php", "test/ATest.php"}, failRC: 1, files: map[string]string{
		"lib/A.php":      "<?php\nfunction one() { return 1; }\n",
		"test/ATest.php": "<?php\nrequire_once __DIR__ . '/../lib/A.php';\nif (!(OUTCOME && one() === 1)) { fwrite(STDERR, \"failed\\n\"); exit(1); }\necho \"ok\\n\";\n",
	}},
	"rust": {tool: "cargo", probe: rustLLVMToolsProbe, testCmd: []string{"cargo", "test"}, failRC: 101, files: map[string]string{
		"Cargo.toml":   "[package]\nname = \"sx\"\nversion = \"0.1.0\"\nedition = \"2021\"\npublish = false\n\n[dependencies]\n",
		"src/lib.rs":   "pub fn one() -> i32 { 1 }\n",
		"tests/one.rs": "#[test]\nfn one() {\n    assert!(OUTCOME && sx::one() == 1);\n}\n",
	}},
}

// EVERY COVERAGECMD SAYS HOW ITS SUITE EXITED, from a real run of each
// runtime installed here: a passing suite prints `# suite-exit 0`, a failing
// one its runner's own failure status, and the report still reads as before.
// Unit evidence takes a unit's coverage only from a green run, and a wrapper
// that reduces whatever the suite exited with would otherwise hide a red one.
func TestEveryCoverageCmdReportsTheSuiteExit(t *testing.T) {
	for name, p := range registry {
		r, ok := p.(CoverageReporter)
		if !ok {
			continue
		}
		fx, ok := suiteExitProject[name]
		if !ok {
			t.Errorf("%s reports coverage but has no suite-exit project", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(fx.tool); err != nil {
				t.Skipf("%s not installed", fx.tool)
			}
			if fx.probe != nil {
				if err := exec.Command(fx.probe[0], fx.probe[1:]...).Run(); err != nil { // #nosec G204 -- a fixed probe from the table above
					t.Skipf("%v failed (%v): no coverage instrument here", fx.probe, err)
				}
			}
			t.Setenv("CARGO_TARGET_DIR", t.TempDir())
			for _, tc := range []struct {
				outcome string
				want    int
			}{{"true", 0}, {"false", fx.failRC}} {
				dir := t.TempDir()
				for rel, text := range fx.files {
					if name == "python" {
						text = strings.ReplaceAll(text, "OUTCOME", map[string]string{"true": "True", "false": "False"}[tc.outcome])
					} else {
						text = strings.ReplaceAll(text, "OUTCOME", tc.outcome)
					}
					path := filepath.Join(dir, rel)
					if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				cmd, ok := r.CoverageCmd(fx.testCmd)
				if !ok {
					t.Fatalf("CoverageCmd refused %v", fx.testCmd)
				}
				c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- CoverageCmd over a fixed command
				c.Dir = dir
				var stderr bytes.Buffer
				c.Stderr = &stderr
				out, _ := c.Output()
				rc, ok := SuiteExit(string(out))
				if !ok || rc != tc.want {
					t.Fatalf("outcome %s: SuiteExit = (%d, %v), want (%d, true)\nstdout:\n%s\nstderr:\n%s", tc.outcome, rc, ok, tc.want, out, stderr.String())
				}
				counts, err := r.CoverageCounts(string(out), dir)
				if err != nil {
					t.Fatalf("outcome %s: the report no longer reads: %v\nstdout:\n%s", tc.outcome, err, out)
				}
				if len(counts) == 0 {
					t.Errorf("outcome %s: the report counted no file", tc.outcome)
				}
			}
		})
	}
}

func TestSuiteExitReadsTheLastTrailer(t *testing.T) {
	cases := []struct {
		out string
		rc  int
		ok  bool
	}{
		{"# suite-exit 0\ncorral-js-coverage: v2\n1 2 a.js\n", 0, true},
		{"--- FAIL\n# suite-exit 1\nmode: set\n", 1, true},
		// a suite that prints the text cannot speak for the run: the
		// script's own line comes after the suite's output
		{"# suite-exit 0\n# suite-exit 101\ncorral-rust-coverage: v2\n", 101, true},
		{"corral-js-coverage: v2\n1 2 a.js\n", 0, false},
		{"# suite-exit x\n", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		if rc, ok := SuiteExit(tc.out); rc != tc.rc || ok != tc.ok {
			t.Errorf("SuiteExit(%q) = (%d, %v), want (%d, %v)", tc.out, rc, ok, tc.rc, tc.ok)
		}
	}
}
