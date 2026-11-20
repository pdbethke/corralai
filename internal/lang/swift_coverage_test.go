// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// THE COUNTS COME FROM A REAL llvm-cov EXPORT. testdata/runners/swift-codecov.json
// is what SwiftPM (Swift 6.3.2) wrote for one coverage run of the fixture
// package testdata/coverage/swift (scripts/capture-runner-fixtures.sh,
// PROVENANCE.txt), its directory written as /fixture/swift-coverage. These
// tests run swiftPlugin.CoverageCmd's real script with a stand-in `swift`
// that writes that JSON where `--show-codecov-path` says it is, so the script
// and the reducer Rust shares are what is tested, and only Swift is stood in
// for.
const swiftCodecovFixture = "testdata/runners/swift-codecov.json"

// swiftCodecovPrefix is the fixture package's directory as the captured JSON
// writes it.
const swiftCodecovPrefix = "/fixture/swift-coverage/"

// fakeSwift writes a stand-in `swift` into a new directory and returns that
// directory, which is also the PATH the script runs with: it holds the
// stand-in and the tools the coverage script needs. It answers
// `--show-codecov-path` with <cwd>/.build/corral-coverage/codecov/Calc.json
// (exit showExit), and otherwise plays a coverage run: it writes json to that
// path with the fixture's directory replaced by the working directory, unless
// json is "", and exits runExit. Every call is appended to <bin>/calls.
func fakeSwift(t *testing.T, json string, showExit, runExit int) (bin string) {
	t.Helper()
	bin = t.TempDir()
	src := ""
	if json != "" {
		src = filepath.Join(bin, "export.json")
		if err := os.WriteFile(src, []byte(json), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(filepath.Join(bin, "calls")) + "\n" +
		"p=\"$(pwd -P)/.build/corral-coverage/codecov/Calc.json\"\n" +
		"case \" $* \" in *' --show-codecov-path '*) echo \"$p\"; exit " + strconv.Itoa(showExit) + ";; esac\n" +
		"echo 'the suite ran' >&2\n"
	if src != "" {
		script += "mkdir -p \"$(dirname \"$p\")\" && sed \"s#" + swiftCodecovPrefix + "#$(pwd -P)/#g\" " + shellQuote(src) + " > \"$p\"\n"
	}
	script += "exit " + strconv.Itoa(runExit) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "swift"), []byte(script), 0o700); err != nil { // #nosec G306 -- an executable the test runs
		t.Fatal(err)
	}
	for _, tool := range []string{"sh", "sed", "mktemp", "rm", "mkdir", "dirname", "awk", "cat", "pwd", "tail"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue // a shell builtin on this machine
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

// runSwiftCoverage runs CoverageCmd(testCmd) in dir with PATH=path, and
// returns its stdout and stderr.
func runSwiftCoverage(t *testing.T, testCmd []string, dir, path string) (stdout, stderr string) {
	t.Helper()
	cmd, ok := swiftPlugin{}.CoverageCmd(testCmd)
	if !ok {
		t.Fatalf("CoverageCmd refused %q", testCmd)
	}
	c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
	c.Env = []string{"PATH=" + path, "HOME=" + t.TempDir()}
	c.Dir = dir
	var errBuf bytes.Buffer
	c.Stderr = &errBuf
	out, err := c.Output()
	if err != nil {
		t.Logf("run error: %v", err)
	}
	return string(out), errBuf.String()
}

// physicalTempDir is a new temp directory as `pwd -P` names it.
func physicalTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Hand-counted from the fixture's sources: Calc.swift's add (lines 2 to 4)
// ran and sub (6 to 8) did not, Dead.swift's never (2 to 5) was compiled and
// never called. Shape.swift declares a protocol, which has no executable
// line, and Unbuilt/Orphan.swift is in no target, so neither is measured.
// Both test files ran. Nothing under .build (the test runner and discovery
// sources SwiftPM generates there) is the project's.
func TestSwiftCoverageCountsFromCapturedExport(t *testing.T) {
	raw := readFixture(t, "swift-codecov.json")
	path := fakeSwift(t, raw, 0, 0)
	stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), path)
	got, err := swiftPlugin{}.CoverageCounts(stdout, "")
	if err != nil {
		t.Fatalf("CoverageCounts: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if want := (LineCounts{Executed: 3, Executable: 6}); got["Sources/Calc/Calc.swift"] != want {
		t.Errorf("Sources/Calc/Calc.swift = %+v, want %+v", got["Sources/Calc/Calc.swift"], want)
	}
	if want := (LineCounts{Executed: 0, Executable: 4}); got["Sources/Calc/Dead.swift"] != want {
		t.Errorf("Sources/Calc/Dead.swift = %+v, want %+v", got["Sources/Calc/Dead.swift"], want)
	}
	assertFixtureCounts(t, got, "Sources/Calc/Calc.swift", "Sources/Calc/Dead.swift", "Unbuilt/Orphan.swift", "Sources/Calc/Shape.swift")
	for _, f := range []string{"Tests/CalcTests/CalcTests.swift", "Tests/CalcXCTests/CalcXCTests.swift"} {
		if c, ok := got[f]; !ok || c.Executed == 0 {
			t.Errorf("%s ran: want Executed>0, got present=%v %+v", f, ok, c)
		}
	}
	if keys := slices.Sorted(maps.Keys(got)); len(keys) != 4 {
		t.Errorf("want exactly the two sources and two tests, got %q", keys)
	}
	if rc, ok := SuiteExit(stdout); !ok || rc != 0 {
		t.Errorf("SuiteExit = %d, %v; want the suite's 0", rc, ok)
	}
	if !strings.Contains(stderr, "the suite ran") {
		t.Errorf("the suite's own output must go to stderr, got stderr:\n%s", stderr)
	}
}

// The captured report (TestCapturedCoverageReportsMatchALiveRun holds it to
// a live run where swift is installed) is the reduction of the captured
// export: both are one coverage run of the same fixture.
func TestSwiftCapturedReportIsTheCapturedExportReduced(t *testing.T) {
	raw := readFixture(t, "swift-codecov.json")
	path := fakeSwift(t, raw, 0, 0)
	stdout, _ := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), path)
	want, err := os.ReadFile(filepath.Join("testdata", "coverage", "swift-report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(want) {
		t.Errorf("the reduced export differs from the captured report.\nreduced:\n%s\ncaptured:\n%s", stdout, want)
	}
}

// FILES OUTSIDE THE WORKING DIRECTORY ARE LEFT OUT, as the build directory
// is: run from the package's Sources/Calc, the same export keeps only the
// two files under it, and nothing above it is read as the project's.
func TestSwiftCoverageLeavesOutFilesOutsideTheTree(t *testing.T) {
	raw := strings.ReplaceAll(readFixture(t, "swift-codecov.json"), swiftCodecovPrefix+"Sources/Calc/", swiftCodecovPrefix+"pkg/Sources/Calc/")
	// The stand-in writes the export with the fixture's directory replaced by
	// the directory it runs in; running in <tmp>/pkg/Sources/Calc puts
	// everything else above the working directory.
	root := physicalTempDir(t)
	pkg := filepath.Join(root, "pkg", "Sources", "Calc")
	if err := os.MkdirAll(pkg, 0o750); err != nil {
		t.Fatal(err)
	}
	raw = strings.ReplaceAll(raw, swiftCodecovPrefix, root+"/")
	raw = strings.ReplaceAll(raw, root+"/pkg/Sources/Calc/", swiftCodecovPrefix)
	path := fakeSwift(t, raw, 0, 0)
	stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, pkg, path)
	got, err := swiftPlugin{}.CoverageCounts(stdout, "")
	if err != nil {
		t.Fatalf("CoverageCounts: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, []string{"Calc.swift", "Dead.swift"}) {
		t.Errorf("want only the two files under the working directory, got %q", keys)
	}
}

// AN EMPTY OR TRUNCATED EXPORT IS AN ERROR, never a report of the files
// before the cut. Checked on the reducer itself, under every awk this machine
// has (Ubuntu's images default to mawk), and through the whole script.
func TestLLVMCovReducerRefusesAnIncompleteExport(t *testing.T) {
	raw := readFixture(t, "swift-codecov.json")
	inputs := map[string]string{
		"empty":     "",
		"truncated": raw[:len(raw)/2],
		"cut-tail":  strings.TrimSuffix(strings.TrimRight(raw, "\n"), "}"),
	}
	for _, awk := range []string{"awk", "mawk", "gawk"} {
		bin, err := exec.LookPath(awk)
		if err != nil {
			continue
		}
		t.Run(awk, func(t *testing.T) {
			run := func(json string) (string, error) {
				f := filepath.Join(t.TempDir(), "export.json")
				if err := os.WriteFile(f, []byte(json), 0o600); err != nil {
					t.Fatal(err)
				}
				c := exec.Command(bin, "-v", "a="+swiftCodecovPrefix, "-v", "b="+swiftCodecovPrefix, // #nosec G204 -- the reducer under test
					"-v", "skip="+llvmCovSkipped(swiftPlugin{}).String(), llvmCovExportReduce, f)
				out, err := c.Output()
				return string(out), err
			}
			for name, json := range inputs {
				if out, err := run(json); err == nil || out != "" {
					t.Errorf("%s: want a refusal with nothing printed, got err=%v out=%q", name, err, out)
				}
			}
			out, err := run(raw)
			if err != nil || !strings.Contains(out, "3 6 Sources/Calc/Calc.swift\n") || strings.Contains(out, ".build/") {
				t.Errorf("the whole export: err=%v out=\n%s", err, out)
			}
		})
	}

	for name, json := range inputs {
		if name == "empty" {
			json = " " // the stand-in writes no file for ""
		}
		path := fakeSwift(t, json, 0, 0)
		stdout, _ := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), path)
		_, err := swiftPlugin{}.CoverageCounts(stdout, "")
		if err == nil || !strings.Contains(err.Error(), "cut short") {
			t.Errorf("%s export through the script: want the unavailable reason, got %v\nstdout:\n%s", name, err, stdout)
		}
		if strings.Contains(stdout, swiftCoverageHeaderV2) {
			t.Errorf("%s export: a header was printed for a report that was never read:\n%s", name, stdout)
		}
	}
}

// NO REPORT IS A DISCLOSED FAILURE, never an empty one. On Swift 6.3.2 a run
// with a failing test or a build that failed writes no export (measured on
// the fixture package); the stale export of an earlier run must not stand in
// for it. And when SwiftPM cannot say where the export goes, the suite is not
// run at all.
func TestSwiftCoverageUnavailableIsAnError(t *testing.T) {
	r := swiftPlugin{}
	report := "Building for debugging...\n" + swiftCoverageUnavailable + " swift test exited 1 and wrote no coverage report\n"
	for name, parse := range map[string]func() error{
		"CoverageCounts": func() error { _, err := r.CoverageCounts(report, ""); return err },
		"ParseCoverage":  func() error { _, err := r.ParseCoverage(report, ""); return err },
	} {
		if err := parse(); err == nil || !strings.Contains(err.Error(), "swift test exited 1 and wrote no coverage report") {
			t.Errorf("%s: want an error carrying the reason, got %v", name, err)
		}
	}
	if _, err := r.CoverageCounts(swiftCoverageUnavailable+"\n", ""); err == nil || !strings.Contains(err.Error(), "no reason given") {
		t.Errorf("an unavailable line with no reason must still be an error that says so, got %v", err)
	}
	if reason, ok := CoverageUnavailable(report); !ok || !strings.HasPrefix(reason, "swift test exited 1") {
		t.Errorf("CoverageUnavailable = %q, %v: the Swift notice must be one the callers read", reason, ok)
	}

	t.Run("failing run with a stale export", func(t *testing.T) {
		dir := physicalTempDir(t)
		stale := filepath.Join(dir, ".build", "corral-coverage", "codecov", "Calc.json")
		if err := os.MkdirAll(filepath.Dir(stale), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stale, []byte(strings.ReplaceAll(readFixture(t, "swift-codecov.json"), swiftCodecovPrefix, dir+"/")), 0o600); err != nil {
			t.Fatal(err)
		}
		path := fakeSwift(t, "", 0, 1)
		stdout, _ := runSwiftCoverage(t, []string{"swift", "test"}, dir, path)
		_, err := r.CoverageCounts(stdout, "")
		if err == nil || !strings.Contains(err.Error(), "exited 1 and wrote no coverage report") {
			t.Errorf("want the unavailable reason naming the exit status, got %v\nstdout:\n%s", err, stdout)
		}
		if rc, ok := SuiteExit(stdout); !ok || rc != 1 {
			t.Errorf("SuiteExit = %d, %v; want the suite's 1", rc, ok)
		}
	})

	t.Run("no codecov path", func(t *testing.T) {
		path := fakeSwift(t, readFixture(t, "swift-codecov.json"), 1, 0)
		bin := path
		stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), path)
		_, err := r.CoverageCounts(stdout, "")
		if err == nil || !strings.Contains(err.Error(), "--show-codecov-path") {
			t.Errorf("want the unavailable reason naming --show-codecov-path, got %v\nstdout:\n%s", err, stdout)
		}
		if strings.Contains(stderr, "the suite ran") {
			t.Errorf("the suite ran although its report could never be found:\n%s", stderr)
		}
		calls, _ := os.ReadFile(filepath.Join(bin, "calls")) // #nosec G304 -- the stand-in's own log
		if strings.Count(string(calls), "\n") != 1 {
			t.Errorf("want only the --show-codecov-path call, got:\n%s", calls)
		}
	})
}

// ONLY A PLAIN `swift test` RUN IS INSTRUMENTED, the shapes the row runs
// (swiftTestAt), and the coverage flags are added after the operator's own
// arguments. A command that names its own package or build directory is
// refused: the reducer places paths relative to the working directory and
// skips the build directory there, so a package or scratch path elsewhere
// would be read with the wrong paths. The coverage build has a scratch path
// of its own (see CoverageCmd), so --disable-code-coverage, which would
// leave it uninstrumented, is refused too.
func TestSwiftCoverageCmdShapes(t *testing.T) {
	r := swiftPlugin{}
	refused := [][]string{
		{},
		{"swift"},
		{"swift", "build"},
		{"swift", "test", "list"},
		{"sh", "-c", "swift test"},
		{"swift", "test", "--", "x"},
		{"cargo", "test"},
		{"swift", "test", "--disable-code-coverage"},
		{"swift", "test", "--package-path", "sub"},
		{"swift", "test", "--package-path=sub"},
		{"swift", "test", "--scratch-path", "/tmp/b"},
		{"swift", "test", "--scratch-path=/tmp/b"},
		{"swift", "test", "--build-path", "b"},
		{"swift", "test", "--build-path=b"},
		{"swift", "test", "--show-codecov-path"},
		// Any build system but native: its export is not the whole suite's
		// (see CoverageCmd), and the operator's choice is not overridden.
		{"swift", "test", "--build-system", "swiftbuild"},
		{"swift", "test", "--build-system=swiftbuild"},
		{"swift", "test", "--build-system", "xcode"},
		{"swift", "test", "--build-system"},
	}
	for _, c := range refused {
		if cmd, ok := r.CoverageCmd(c); ok {
			t.Errorf("CoverageCmd(%q) accepted: %q", c, cmd)
		}
	}
	for _, c := range [][]string{
		{"swift", "test"},
		{"/usr/bin/swift", "test", "-c", "release"},
		{"swift", "test", "--filter", `^(?:CalcXCTests)\.`},
		{"swift", "test", "--enable-code-coverage"},
		{"swift", "test", "--parallel"},
		{"swift", "test", "--build-system", "native"},
		{"swift", "test", "--build-system=native"},
	} {
		cmd, ok := r.CoverageCmd(c)
		if !ok {
			t.Errorf("CoverageCmd(%q) refused a swift test run", c)
			continue
		}
		if len(cmd) < 4 || cmd[0] != "sh" || cmd[1] != "-c" || cmd[3] != coverageArgv0 {
			t.Errorf("CoverageCmd(%q) = %q, want a sh -c script with the command as its positional parameters", c, cmd)
			continue
		}
		args := cmd[4:]
		if !slices.Equal(args[:len(c)], c) {
			t.Errorf("CoverageCmd(%q): the operator's command must come first and unchanged, got %q", c, args)
		}
		if extra := args[len(c):]; !slices.Contains(extra, "--scratch-path") || (!slices.Contains(c, "--enable-code-coverage") && !slices.Contains(extra, "--enable-code-coverage")) {
			t.Errorf("CoverageCmd(%q) added %q, want --enable-code-coverage and its scratch path", c, extra)
		}
		if bs := swiftBuildSystems(args); !slices.Equal(bs, []string{"native"}) {
			t.Errorf("CoverageCmd(%q) runs with build systems %q, want native exactly once", c, bs)
		}
		if !slices.Contains(InterpretersIn(cmd), c[0]) {
			t.Errorf("InterpretersIn(%q) = %q, want the operator's swift among them", cmd, InterpretersIn(cmd))
		}
	}
}

// swiftBuildSystems is every --build-system value args names, in order.
func swiftBuildSystems(args []string) []string {
	var out []string
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--build-system="); ok {
			out = append(out, v)
		} else if a == "--build-system" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// THE COVERAGE RUN USES THE NATIVE BUILD SYSTEM, both when it asks where the
// export goes and when it runs, because only that build system's export holds
// every test target's files. Measured 2026-11-20 on the coverage fixture plus
// a library target only the XCTest target depends on: under swiftbuild (the
// default from Swift 6.4, opt-in on 6.3.2) the export SwiftPM wrote held the
// files of one test product (6.4.2) or none of the project's at all (6.3.2),
// so files that product does not link were absent and read as never run;
// under native, on 6.3.2 and 6.4.2 alike, every file was there.
func TestSwiftCoverageRunsTheNativeBuildSystem(t *testing.T) {
	bin := fakeSwift(t, readFixture(t, "swift-codecov.json"), 0, 0)
	stdout, _ := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), bin)
	if _, err := (swiftPlugin{}).CoverageCounts(stdout, ""); err != nil {
		t.Fatalf("CoverageCounts: %v\n%s", err, stdout)
	}
	calls, err := os.ReadFile(filepath.Join(bin, "calls")) // #nosec G304 -- the stand-in's own log
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want the --show-codecov-path call and the run, got:\n%s", calls)
	}
	for _, l := range lines {
		if bs := swiftBuildSystems(strings.Fields(l)); !slices.Equal(bs, []string{"native"}) {
			t.Errorf("swift %s: build systems %q, want native exactly once", l, bs)
		}
	}
}
