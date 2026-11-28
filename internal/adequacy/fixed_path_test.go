// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// The fixed in-jail workspace path moved every run of one tree to the SAME
// directory. These tests prove the two things that move could have changed:
// that Go does not replay a test result across runs now that the directory no
// longer differs, and that no reader of what a run prints answers differently
// at /corral-work than at the host path the same tree used to run at.

// goTree is a module with one passing test.
func goTree() map[string]string {
	return map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.21\n",
		"p/p.go":      "package p\n\nfunc One() int { return 1 }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
	}
}

// goRunCacheJail is a jail whose run cache is GOCACHE, with room for a real
// go build. It skips where the host cannot share a cache.
func goRunCacheJail(t *testing.T) RunCacheJail {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	backend := overlayJailOrSkip(t)
	if !sandbox.PlacesWorkspace(backend) {
		t.Skipf("%s cannot place a workspace at a fixed path", backendVersion(backend))
	}
	t.Setenv("TMPDIR", t.TempDir())
	j := NewJail(backend, 3*time.Minute, WithRunCache(&lang.EvidenceCache{Env: "GOCACHE"})).(RunCacheJail)
	t.Cleanup(func() { _ = j.Close() })
	if c := j.RunCache(); c.Target == "" {
		t.Skipf("no shared cache on this host (%s); the frozen-cache path is unexercised", c.Unshared)
	}
	return j
}

func cachedLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "(cached)") {
			lines = append(lines, l)
		}
	}
	return lines
}

// At the fixed path every run of one tree is in one directory, so the
// directory no longer keeps Go from replaying a result. The only guard left is
// that the cache is shared only when its warming command forbids a cached
// result. Three parts:
//
//   - the guarantee: a cache warmed with -count=1 leaves nothing to replay,
//     however many frozen runs follow without it;
//   - the control: warmed WITHOUT -count=1, the very same frozen run DOES
//     replay ("(cached)"), with no -trimpath, so the guarantee above is a
//     measurement and not an inability to see a replay;
//   - a changed source byte fails the frozen run (a result is keyed on the
//     source, not just replayed).
func TestNoGoResultIsReplayedAtTheFixedPath(t *testing.T) {
	ctx := context.Background()
	files := goTree()
	cmd := []string{"go", "test", "./..."}

	t.Run("guarantee", func(t *testing.T) {
		j := goRunCacheJail(t)
		ok, out, err := j.WarmRun(ctx, files, []string{"go", "test", "./...", "-count=1"})
		if err != nil || !ok {
			t.Fatalf("warm: ok=%v err=%v\n%s", ok, err, out)
		}
		for i := 1; i <= 2; i++ {
			ok, out, err := j.(DetailedJail).RunTestDetailed(ctx, files, cmd)
			if err != nil || !ok {
				t.Fatalf("frozen run %d: ok=%v err=%v\n%s", i, ok, err, out)
			}
			if !strings.Contains(string(out), "ok ") {
				t.Fatalf("frozen run %d printed no result line, so \"no (cached)\" proves nothing:\n%s", i, out)
			}
			if c := cachedLines(string(out)); len(c) != 0 {
				t.Errorf("frozen run %d replayed a result after a -count=1 warm: %q", i, c)
			}
		}
	})

	t.Run("control", func(t *testing.T) {
		j := goRunCacheJail(t)
		ok, out, err := j.WarmRun(ctx, files, cmd)
		if err != nil || !ok {
			t.Fatalf("warm: ok=%v err=%v\n%s", ok, err, out)
		}
		ok, out, err = j.(DetailedJail).RunTestDetailed(ctx, files, cmd)
		if err != nil || !ok {
			t.Fatalf("frozen run: ok=%v err=%v\n%s", ok, err, out)
		}
		c := cachedLines(string(out))
		if len(c) == 0 {
			t.Fatalf("control: warmed without -count=1, a frozen run at %s did not replay the result, so this test cannot see a replay (and the fixed path is not giving go a stable directory):\n%s", sandbox.WorkTarget, out)
		}
		t.Logf("control replayed: %q", c)
	})

	t.Run("mutation", func(t *testing.T) {
		j := goRunCacheJail(t)
		if ok, out, err := j.WarmRun(ctx, files, []string{"go", "test", "./...", "-count=1"}); err != nil || !ok {
			t.Fatalf("warm: ok=%v err=%v\n%s", ok, err, out)
		}
		mutated := maps.Clone(files)
		mutated["p/p.go"] = strings.Replace(files["p/p.go"], "return 1", "return 2", 1)
		if mutated["p/p.go"] == files["p/p.go"] {
			t.Fatal("the mutation changed nothing")
		}
		ok, out, err := j.(DetailedJail).RunTestDetailed(ctx, mutated, cmd)
		if err != nil || ok || !strings.Contains(string(out), "FAIL") {
			t.Errorf("a frozen run over a mutated source: ok=%v err=%v, want a clean FAIL\n%s", ok, err, out)
		}
	})
}

// A coverage fixture and how the evidence pass runs it: the tree under
// internal/lang/testdata, the test command, the toolchain that must be on
// the host (and so in the jail), and what the reducer is parsed against.
type readerCase struct {
	name, lang, dir string
	testCmd         []string
	tool            string
	probe           []string
	// root is what ParseCoverage / CoverageCounts is given: Go's module path,
	// otherwise a directory that is the run's root as the CALLER knows it.
	root string
}

var readerCases = []readerCase{
	{name: "go", lang: "go", dir: "go", testCmd: []string{"go", "test", "-count=1", "./..."}, tool: "go", root: "corralfixture"},
	{name: "python", lang: "python", dir: "python", testCmd: []string{"python3", "-m", "pytest", "-q"}, tool: "python3",
		probe: []string{"python3", "-c", "import coverage, pytest"}},
	{name: "ruby", lang: "ruby", dir: "ruby", testCmd: []string{"ruby", "-Ilib", "test/calc_test.rb"}, tool: "ruby"},
	{name: "javascript", lang: "javascript", dir: "js", testCmd: []string{"node", "--test"}, tool: "node"},
	{name: "php", lang: "php", dir: "php", testCmd: []string{"php", "test/CalcTest.php"}, tool: "php",
		probe: []string{"php", "-r", `exit(extension_loaded("pcov") ? 0 : 1);`}},
	{name: "rust", lang: "rust", dir: "rust", testCmd: []string{"cargo", "test"}, tool: "cargo",
		probe: []string{"sh", "-c", `b="$(rustc --print sysroot)/lib/rustlib/$(rustc -vV | sed -n 's/^host: //p')/bin"; test -x "$b/llvm-cov" || command -v llvm-cov`}},
}

// fixtureTree reads a testdata directory into the files map a jail run takes.
func fixtureTree(t *testing.T, sub ...string) map[string]string {
	t.Helper()
	root := filepath.Join(append([]string{"..", "lang", "testdata"}, sub...)...)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, "target"+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 -- a fixed testdata tree
		files[rel] = string(b)
		return err
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("read fixture %v: %d files, %v", sub, len(files), err)
	}
	return files
}

// placements are the two ways one tree runs: where it is now (/corral-work)
// and where it used to be (its host path).
func placements(backend sandbox.Isolator) map[string]bwrapJail {
	return map[string]bwrapJail{
		"fixed": NewJail(backend, 5*time.Minute).(bwrapJail),
		"host":  NewJail(backend, 5*time.Minute, withoutWorkTarget()).(bwrapJail),
	}
}

// requireTool skips a language whose toolchain (or capability probe) is not
// on this host, naming it, so the report can list what ran.
//
// The toolchain must be present INSIDE the jail, not just on the host: a
// user-local python package or a rust toolchain without llvm-tools is on the
// host and not behind the sandbox, and a run there would fail for reasons
// that say nothing about the path. So the probe runs in the jail.
func requireTool(t *testing.T, backend sandbox.Isolator, tool string, probe []string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("SKIP %s: not installed", tool)
	}
	if probe == nil {
		return
	}
	res, err := NewJail(backend, time.Minute).(bwrapJail).EnumerateDetailed(context.Background(), map[string]string{"a.txt": "x"}, probe)
	if err != nil || res.ExitCode != 0 {
		t.Skipf("SKIP %s: the toolchain probe %q fails inside the jail (exit %d, %v): %s", tool, probe, res.ExitCode, err, strings.TrimSpace(res.Output))
	}
}

// Each language's coverage reducer, run by the evidence pass's own command
// in the real jail, reports the same files and counts at /corral-work as at
// the host path, and the report is not empty (two empty answers agree on
// nothing).
func TestCoverageReadersGiveTheSameAnswerAtTheFixedPath(t *testing.T) {
	backend := placingJailOrSkip(t)
	for _, tc := range readerCases {
		t.Run(tc.name, func(t *testing.T) {
			requireTool(t, backend, tc.tool, tc.probe)
			files := fixtureTree(t, "coverage", tc.dir)
			p, _ := lang.ByName(tc.lang)
			r := p.(lang.CoverageReporter)
			cmd, ok := r.CoverageCmd(tc.testCmd)
			if !ok {
				t.Fatalf("no coverage command for %q", tc.testCmd)
			}
			root := tc.root
			if root == "" {
				root = "/the/callers/original/checkout"
			}
			got := map[string]map[string]lang.LineCounts{}
			for name, j := range placements(backend) {
				out, err := j.Enumerate(context.Background(), files, cmd)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				counts, err := r.CoverageCounts(out, root)
				if err != nil {
					t.Fatalf("%s: the report did not parse: %v\n%s", name, err, out)
				}
				executed, err := r.ParseCoverage(out, root)
				if err != nil {
					t.Fatalf("%s: ParseCoverage: %v", name, err)
				}
				if len(executed) != len(counts) {
					t.Errorf("%s: ParseCoverage names %d files, CoverageCounts %d", name, len(executed), len(counts))
				}
				nonzero := 0
				for _, c := range counts {
					if c.Executed > 0 {
						nonzero++
					}
				}
				if nonzero == 0 {
					t.Fatalf("%s: no file with a non-zero executed count in %v; an empty comparison proves nothing\n%s", name, counts, out)
				}
				got[name] = counts
			}
			if !maps.Equal(got["fixed"], got["host"]) {
				t.Errorf("coverage differs by placement:\nfixed %v\nhost  %v", got["fixed"], got["host"])
			}
		})
	}
}

// failFixture is a tree whose suite fails, and the command to run it.
type failCase struct {
	name, lang, tool string
	probe            []string
	files            map[string]string
	cmd              []string
	want             string
}

func failCases(t *testing.T) []failCase {
	t.Helper()
	runners := func(rels ...string) map[string]string {
		all := fixtureTree(t, "runners", "src")
		out := map[string]string{}
		for _, r := range rels {
			for k, v := range all {
				if k == r || strings.HasPrefix(k, r+"/") {
					out[k] = v
				}
			}
		}
		return out
	}
	return []failCase{
		{name: "go", lang: "go", tool: "go", cmd: []string{"go", "test", "-v", "./..."}, want: "TestBoom",
			files: map[string]string{
				"go.mod":    "module failfix\n\ngo 1.20\n",
				"f_test.go": "package failfix\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\nfunc TestBoom(t *testing.T) { t.Fatal(\"boom\") }\n",
			}},
		{name: "ruby", lang: "ruby", tool: "ruby", probe: []string{"ruby", "-e", `require "minitest"`},
			cmd: []string{"ruby", "fail/f_test.rb", "--seed", "1"}, files: runners("fail/f_test.rb"), want: "#test_"},
		{name: "javascript", lang: "javascript", tool: "node",
			cmd: []string{"node", "--test", "fail/f.node.test.js"}, files: runners("fail/f.node.test.js"), want: "Calc::adds (a+b)"},
		{name: "python", lang: "python", tool: "python3", probe: []string{"python3", "-c", "import pytest"},
			cmd: []string{"python3", "-m", "pytest", "-q", "-p", "no:cacheprovider"}, want: "test_boom",
			files: map[string]string{"test_f.py": "def test_ok():\n    pass\n\ndef test_boom():\n    assert 1 == 2\n"}},
		// PHPUnit's own command, as the runner row reads it (the `phpunit`
		// word); the fixture is the runner table's failing suite.
		{name: "php", lang: "php", tool: "phpunit", probe: []string{"phpunit", "--version"},
			cmd: []string{"phpunit", "fail/tests/FTest.php"}, files: runners("fail/tests/FTest.php"), want: "FTest::test"},
		{name: "rust", lang: "rust", tool: "cargo", probe: rustInJailProbe,
			cmd: []string{"cargo", "test"}, want: "boom",
			files: map[string]string{
				"Cargo.toml": "[package]\nname = \"failfix\"\nversion = \"0.1.0\"\nedition = \"2021\"\npublish = false\n\n[dependencies]\n",
				"src/lib.rs": "#[cfg(test)]\nmod tests {\n    #[test]\n    fn ok() {}\n    #[test]\n    fn boom() {\n        assert_eq!(1, 2);\n    }\n}\n",
			}},
	}
}

// rustInJailProbe is the Rust toolchain answering INSIDE the jail: cargo on
// the host is not enough (the jail sees the system-wide rustup only).
var rustInJailProbe = []string{"sh", "-c", "cargo --version && rustc --version"}

// The failing test a runner's own failure parser names from a failing run
// is the same at both placements, and it names one (a run that names no
// failure at either placement would compare equal and prove nothing).
func TestFailureParsersNameTheSameTestAtTheFixedPath(t *testing.T) {
	backend := placingJailOrSkip(t)
	for _, tc := range failCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			requireTool(t, backend, tc.tool, tc.probe)
			p, _ := lang.ByName(tc.lang)
			fp := lang.FailureParserFor(p, lang.Cmd(tc.cmd))
			if fp == nil {
				t.Skipf("SKIP %s: no failure parser for %q", tc.lang, tc.cmd)
			}
			got := map[string]string{}
			for name, j := range placements(backend) {
				ok, out, err := j.RunTestDetailed(context.Background(), tc.files, tc.cmd)
				if err != nil || ok {
					t.Fatalf("%s: the failing fixture ok=%v err=%v\n%s", name, ok, err, out)
				}
				got[name] = fp.FirstFailure(out)
				if !strings.Contains(got[name], tc.want) {
					t.Errorf("%s: first failure %q, want it to name %q\n%s", name, got[name], tc.want, out)
				}
			}
			if got["fixed"] != got["host"] {
				t.Errorf("the killing test differs by placement: fixed %q, host %q", got["fixed"], got["host"])
			}
		})
	}
}

// The unit listing (the working-directory line and the units it names) is the
// same at both placements, and the working-directory line is what the parser
// is given: a unit relative to /corral-work reads like one relative to the
// host path.
func TestUnitListingsAreTheSameAtTheFixedPath(t *testing.T) {
	backend := placingJailOrSkip(t)
	for _, tc := range []struct {
		name, lang, tool string
		probe            []string
		cmd              []string
		files            map[string]string
		want             string
	}{
		{name: "go", lang: "go", tool: "go", cmd: []string{"go", "test", "./..."}, files: goTree(), want: "p"},
		{name: "javascript", lang: "javascript", tool: "node", cmd: []string{"node", "--test"},
			files: map[string]string{"a.test.js": "const t = require('node:test');\nt.test('x', () => {});\n"}, want: "a.test.js"},
		// Ruby lists through rspec; minitest, Ruby's other runner, has no
		// listing in the runner table (lang/runner.go: "no unit evidence: no
		// listing command"), so it has no case here.
		{name: "ruby", lang: "ruby", tool: "rspec", probe: []string{"rspec", "--version"}, cmd: []string{"rspec"},
			files: fixtureTree(t, "runners", "src", "units", "rspec"), want: "spec/a_spec.rb"},
		{name: "php", lang: "php", tool: "phpunit", probe: []string{"phpunit", "--version"}, cmd: []string{"phpunit"},
			files: fixtureTree(t, "runners", "src", "units", "phpunit"), want: "ATest.php"},
		// pytest has no ListUnits in the runner table (Python's unit evidence
		// is not built), so ListUnitsCmd answers no and this case SKIPS,
		// naming it: it is here so the gap is reported, not silently absent.
		{name: "python", lang: "python", tool: "python3", probe: []string{"python3", "-c", "import pytest"},
			cmd: []string{"python3", "-m", "pytest", "-q"}, files: map[string]string{"test_a.py": "def test_a():\n    pass\n"}, want: "test_a.py"},
		{name: "rust", lang: "rust", tool: "cargo", probe: rustInJailProbe, cmd: []string{"cargo", "test", "--workspace"},
			files: fixtureTree(t, "runners", "src", "units", "cargo"), want: "units-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireTool(t, backend, tc.tool, tc.probe)
			p, _ := lang.ByName(tc.lang)
			inv := lang.Cmd(tc.cmd)
			list, ok := lang.ListUnitsCmd(p, inv)
			if !ok {
				t.Skipf("SKIP %s: the runner table has no unit listing for %q", tc.lang, tc.cmd)
			}
			got := map[string][]string{}
			outs := map[string]string{}
			for name, j := range placements(backend) {
				out, err := j.Enumerate(context.Background(), tc.files, list)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				got[name], outs[name] = lang.ParseUnitList(p, inv, out), out
			}
			// A listing that is empty at BOTH placements says nothing about
			// the placement: the runner on this machine listed nothing either
			// way (on CI's image PHP prints hundreds of SNMP start-up lines
			// and the listing is lost past the output cap). That is a
			// could-not-measure, said by name, never a pass. Empty at ONE
			// placement is the defect this test exists to find.
			if len(got["fixed"]) == 0 && len(got["host"]) == 0 {
				t.Skipf("SKIP %s: the listing is empty at both placements, so this machine cannot measure it; output ends:\n%s", tc.lang, tail(outs["fixed"], 600))
			}
			for name, units := range got {
				if !slices.ContainsFunc(units, func(u string) bool { return strings.Contains(u, tc.want) }) {
					t.Fatalf("%s: units %q name no %q (the other placement listed %d)\n%s", name, units, tc.want, len(got["fixed"])+len(got["host"])-len(units), tail(outs[name], 600))
				}
			}
			if !slices.Equal(got["fixed"], got["host"]) {
				t.Errorf("units differ by placement:\nfixed %q\nhost  %q", got["fixed"], got["host"])
			}
		})
	}
}
