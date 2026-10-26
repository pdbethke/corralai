// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TRANSFORMED CODE IS COUNTED THROUGH THE MAP THE RUNNER EVALUATED IT WITH.
// vitest runs every module of a project through node:vm, as transformed
// code carrying an inline source map. Node keeps no map for vm-compiled code,
// so until corral's preload (jsCoveragePreload) recorded those maps, the
// script-range guard refused every vitest-run file ("V8 ran 111533
// characters under its URL and the file on disk has 41460", REA, Task 11a).
//
// The fixture is testdata/coverage/vitest: lib/calc.ts is called by the
// suite, lib/dead.ts is imported and never called, lib/unloaded.ts is never
// imported. Its report and the V8 inputs behind it are captured from a real
// vitest 3.2.7 run (the version scripts/capture-runner-fixtures.sh pins):
// set CORRALAI_VITEST to that install's vitest.mjs and
// CORRALAI_RECAPTURE_COVERAGE=1 to rewrite them.

const vitestReport = "vitest-report.txt"

// vitestArgs is the fixture's command after `node <vitest.mjs>`. --pool=threads
// because vitest 3.2.7's default pool, forks, wrote no V8 coverage for its
// worker on this fixture (measured: one coverage file, the main process's,
// with no lib/ file in it), and the threads pool is what REA's composition
// project runs under.
var vitestArgs = []string{"run", "--globals", "--no-cache", "--pool=threads"}

func vitestFixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "coverage", "vitest"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The live half: CoverageCmd over the real vitest prints the captured
// report, byte for byte.
func TestVitestCoverageReportMatchesALiveRun(t *testing.T) {
	vt := os.Getenv("CORRALAI_VITEST")
	if vt == "" {
		t.Skip("CORRALAI_VITEST is not set — the live half needs a vitest 3.2.7 install's vitest.mjs (npm install vitest@3.2.7)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	root := vitestFixtureRoot(t)
	ts, _ := ByName("typescript")
	cmd, ok := ts.(CoverageReporter).CoverageCmd(append([]string{"node", vt}, vitestArgs...))
	if !ok {
		t.Fatal("CoverageCmd refused the vitest command")
	}
	c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
	c.Dir = root
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	path := filepath.Join("testdata", "coverage", vitestReport)
	if os.Getenv("CORRALAI_RECAPTURE_COVERAGE") == "1" {
		if werr := os.WriteFile(path, out, 0o600); werr != nil {
			t.Fatal(werr)
		}
		recaptureVitestInputs(t, vt, root)
		t.Logf("recaptured %s (run error: %v)", path, err)
		return
	}
	want, rerr := os.ReadFile(path) // #nosec G304 -- a fixed testdata path
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(out) != string(want) {
		t.Errorf("a live vitest run no longer prints the captured report.\nlive:\n%s\ncaptured:\n%s\nrun error: %v\nSTDERR:\n%s", out, want, err, stderr.String())
	}
}

// recaptureVitestInputs keeps what the reducer read on a real run: the V8
// coverage documents and the preload's recorded maps, cut to the fixture's
// own files and with its root written ROOT, so the reducer can be checked
// against them on a machine with node and no vitest.
func recaptureVitestInputs(t *testing.T, vt, root string) {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "corral_maps.cjs"), []byte(jsCoveragePreload), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("node", append([]string{vt}, vitestArgs...)...) // #nosec G204 -- the fixture's own vitest run
	c.Dir = root
	c.Env = append(os.Environ(), "NODE_V8_COVERAGE="+filepath.Join(d, "cov"), "NODE_OPTIONS=--require "+filepath.Join(d, "corral_maps.cjs"))
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("vitest: %v\n%s", err, out)
	}
	dst := filepath.Join("testdata", "coverage", "vitest-v8")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	prefix := "file://" + root + "/"
	var docs []string
	entries, _ := os.ReadDir(filepath.Join(d, "cov"))
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(d, "cov", e.Name())) // #nosec G304 -- this run's own temp dir
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Result []json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		var kept []json.RawMessage
		for _, r := range doc.Result {
			var s struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(r, &s) == nil && strings.HasPrefix(s.URL, prefix) {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			continue
		}
		b, err := json.Marshal(map[string]any{"result": kept})
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, strings.ReplaceAll(string(b), root, "ROOT"))
	}
	slices.Sort(docs)
	if err := os.WriteFile(filepath.Join(dst, "cov.jsonl"), []byte(strings.Join(docs, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var maps []string
	mapFiles, _ := os.ReadDir(filepath.Join(d, "maps"))
	for _, e := range mapFiles {
		raw, err := os.ReadFile(filepath.Join(d, "maps", e.Name())) // #nosec G304 -- this run's own temp dir
		if err != nil {
			t.Fatal(err)
		}
		for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var r struct {
				Filename string `json:"filename"`
			}
			if json.Unmarshal([]byte(ln), &r) == nil && strings.HasPrefix(r.Filename, root+"/") {
				maps = append(maps, strings.ReplaceAll(ln, root, "ROOT"))
			}
		}
	}
	slices.Sort(maps)
	if err := os.WriteFile(filepath.Join(dst, "maps.jsonl"), []byte(strings.Join(maps, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runVitestInputs runs the shipped reducer over the captured inputs, rooted
// at the fixture, with or without the recorded maps.
func runVitestInputs(t *testing.T, withMaps bool) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed — the reducer runs in node")
	}
	root := vitestFixtureRoot(t)
	src := filepath.Join("testdata", "coverage", "vitest-v8")
	d := t.TempDir()
	write := func(dir, name, body string) {
		if err := os.MkdirAll(filepath.Join(d, dir), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, dir, name), []byte(strings.ReplaceAll(body, "ROOT", root)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cov, err := os.ReadFile(filepath.Join(src, "cov.jsonl")) // #nosec G304 -- a fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	for i, doc := range strings.Split(strings.TrimSpace(string(cov)), "\n") {
		write("cov", fmt.Sprintf("coverage-%d.json", i), doc)
	}
	if withMaps {
		maps, err := os.ReadFile(filepath.Join(src, "maps.jsonl")) // #nosec G304 -- a fixed testdata path
		if err != nil {
			t.Fatal(err)
		}
		write("maps", "1-0.jsonl", string(maps))
	}
	script := filepath.Join(d, "corral_reduce.js")
	if err := os.WriteFile(script, []byte(jsCoverageReduce), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("node", script, filepath.Join(d, "cov")) // #nosec G204 -- node and a temp script this test wrote
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		t.Fatalf("reducer: %v", err)
	}
	return string(out)
}

// The reducer, over what a real vitest run handed it, prints the captured
// report: the called file executed, the imported-and-never-called file
// measured with nothing run, the never-imported file absent.
func TestJSReducerCountsVitestThroughRecordedMaps(t *testing.T) {
	out := runVitestInputs(t, true)
	want, err := os.ReadFile(filepath.Join("testdata", "coverage", vitestReport)) // #nosec G304 -- a fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	if got, w := out, strings.TrimPrefix(string(want), "# suite-exit 0\n"); got != w {
		t.Errorf("reducer over the captured inputs:\n%s\nwant (the captured live report):\n%s", got, w)
	}
	counts, err := jsPlugin{}.CoverageCounts(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := counts["lib/calc.ts"]; c.Executed == 0 || c.Executed >= c.Executable {
		t.Errorf("lib/calc.ts = %+v: add ran and sub did not, so some but not all lines executed", c)
	}
	if c, ok := counts["lib/dead.ts"]; !ok || c.Executed != 0 || c.Executable == 0 {
		t.Errorf("lib/dead.ts = %+v (present %v): imported, never called — measured, nothing executed", c, ok)
	}
	if _, ok := counts["lib/unloaded.ts"]; ok {
		t.Error("lib/unloaded.ts was never loaded and must be absent")
	}
}

// Without a recorded map the guard still refuses transformed code: the same
// inputs, minus the maps, count no fixture source.
func TestJSReducerStillRefusesTransformedCodeWithNoMap(t *testing.T) {
	out := runVitestInputs(t, false)
	for _, f := range []string{"lib/calc.ts", "lib/dead.ts"} {
		if !strings.Contains(out, "# uncounted "+f+": V8 ran ") {
			t.Errorf("%s must be uncounted without its map:\n%s", f, out)
		}
	}
}

// THE PRELOAD NEVER CHANGES WHAT A SUITE DOES. A suite that inspects node:vm
// (results, thrown errors and their identity, Script subclassing and
// new.target, compileFunction, the functions' names and lengths, the named
// ESM exports) gives the same TAP report, exit status and stderr with the
// preload as without it, and the preload did record (it is not a no-op).
func TestJSCoveragePreloadPreservesVMBehaviour(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	root, err := filepath.Abs(filepath.Join("testdata", "coverage", "vmprobe"))
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	preload := filepath.Join(d, "corral_maps.cjs")
	if err := os.WriteFile(preload, []byte(jsCoveragePreload), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(nodeOptions string) (string, string, int) {
		c := exec.Command("node", "--test", "--test-reporter=tap", "--test-concurrency=1", "vm.test.cjs", "esm.test.mjs") // #nosec G204 -- fixed arguments
		c.Dir = root
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "NODE_OPTIONS=") {
				env = append(env, kv)
			}
		}
		if nodeOptions != "" {
			env = append(env, "NODE_OPTIONS="+nodeOptions)
		}
		c.Env = env
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		rc := 0
		if err := c.Run(); err != nil {
			rc = -1
			if ee, ok := err.(*exec.ExitError); ok {
				rc = ee.ExitCode()
			}
		}
		var kept []string
		for _, ln := range strings.Split(stdout.String(), "\n") {
			if !strings.Contains(ln, "duration_ms") {
				kept = append(kept, ln)
			}
		}
		return strings.Join(kept, "\n"), stderr.String(), rc
	}
	plainOut, plainErr, plainRC := run("")
	preOut, preErr, preRC := run("--require " + preload)
	if plainRC != 0 {
		t.Fatalf("the suite fails without the preload (rc %d):\n%s\n%s", plainRC, plainOut, plainErr)
	}
	if preOut != plainOut || preErr != plainErr || preRC != plainRC {
		t.Errorf("the preload changed the suite's behaviour.\nwithout (rc %d):\n%s\n%s\nwith (rc %d):\n%s\n%s", plainRC, plainOut, plainErr, preRC, preOut, preErr)
	}
	recorded, _ := os.ReadDir(filepath.Join(d, "maps"))
	if len(recorded) == 0 {
		t.Error("the preload recorded no map: the suite evaluates mapped code, so it was not loaded")
	}
}

// The preload is APPENDED to the operator's NODE_OPTIONS: a suite that needs
// its own flags keeps them.
func TestJSCoverageCmdAppendsToNodeOptions(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	cmd, ok := jsPlugin{}.CoverageCmd([]string{"node", "-e", "console.error('NO=' + process.env.NODE_OPTIONS)"})
	if !ok {
		t.Fatal("CoverageCmd refused node -e")
	}
	c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
	c.Dir = t.TempDir()
	c.Env = append(os.Environ(), "NODE_OPTIONS=--no-warnings")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	_ = c.Run()
	line := ""
	for _, ln := range strings.Split(stderr.String(), "\n") {
		if strings.HasPrefix(ln, "NO=") {
			line = ln
		}
	}
	if !strings.HasPrefix(line, "NO=--no-warnings --require ") || !strings.HasSuffix(line, `corral_maps.cjs"`) {
		t.Errorf("NODE_OPTIONS inside the run = %q, want the operator's flags then the preload", line)
	}
}

// A function whose own range maps to no original position is code the runner
// added (vitest's module wrapper and export getters sit on generated lines
// with no mapping at all — measured on REA: every unmappable range was an
// unnamed function on such a line). Its inner ranges are not counted either:
// a cold block of it that happens to map would otherwise paint an original
// line that ran as never executed.
func TestJSReducerSkipsAFunctionWhoseOwnRangeMapsNowhere(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed — the reducer runs in node")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("function f() {\n  return 1;\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Generated: an added line, then a.ts line for line.
	gen := "XX\nfunction f() {\n  return 1;\n}"
	m, _ := json.Marshal(map[string]any{"version": 3, "sources": []string{"a.ts"}, "names": []string{}, "mappings": ";AAAA;AACA;AACA"})
	rec, _ := json.Marshal(map[string]any{"kind": "script", "filename": filepath.Join(root, "a.ts"), "length": len(gen),
		"lineOffset": 0, "columnOffset": 0, "lineLengths": []int{2, 14, 11, 1}, "map": string(m)})
	d := t.TempDir()
	for _, sub := range []string{"cov", "maps"} {
		if err := os.Mkdir(filepath.Join(d, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "maps", "1-0.jsonl"), append(rec, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	v8 := fmt.Sprintf(`{"result":[{"url":"file://%s/a.ts","functions":[
 {"functionName":"","ranges":[{"startOffset":0,"endOffset":%d,"count":1}]},
 {"functionName":"f","ranges":[{"startOffset":3,"endOffset":%d,"count":1}]},
 {"functionName":"","ranges":[{"startOffset":0,"endOffset":2,"count":1},{"startOffset":18,"endOffset":29,"count":0}]}]}]}`, root, len(gen), len(gen))
	if err := os.WriteFile(filepath.Join(d, "cov", "coverage-0.json"), []byte(v8), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(d, "corral_reduce.js")
	if err := os.WriteFile(script, []byte(jsCoverageReduce), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("node", script, filepath.Join(d, "cov")) // #nosec G204 -- node and a temp script this test wrote
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		t.Fatalf("reducer: %v", err)
	}
	if !strings.Contains(string(out), "\n3 3 a.ts\n") {
		t.Errorf("a.ts: f ran in full, and the added function's block must not mark a line of it cold:\n%s", out)
	}
}
