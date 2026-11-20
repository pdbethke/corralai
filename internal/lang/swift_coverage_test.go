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
// is what the Swift 6.3.2 toolchain's llvm-cov exported for one coverage run of
// the fixture package testdata/coverage/swift, run the way
// swiftPlugin.CoverageCmd runs it (scripts/capture-runner-fixtures.sh,
// PROVENANCE.txt), its directory written as /fixture/swift-coverage. These
// tests run CoverageCmd's real script with a stand-in toolchain (fakeSwift)
// whose llvm-cov prints that JSON, so the script, the merge and export it
// shares with Rust and the reducer are what is tested, and only the toolchain
// is stood in for.
const swiftCodecovFixture = "testdata/runners/swift-codecov.json"

// swiftCodecovPrefix is the fixture package's directory as the captured JSON
// writes it.
const swiftCodecovPrefix = "/fixture/swift-coverage/"

// fakeSwiftCodecov is where the stand-in `swift` says the coverage export goes,
// relative to the working directory: the codecov directory, whose parent is
// SwiftPM's products directory, as on every toolchain measured (see
// CoverageCmd).
const fakeSwiftCodecov = ".build/corral-coverage/debug/codecov/Calc.json"

// fakeSwift writes a stand-in Swift toolchain into a new directory and returns
// that directory, which is also the PATH the script runs with: `swift`, the
// three LLVM tools the coverage script runs from beside it, and links to the
// system tools the script needs (systemTools). Every call of a stand-in is
// appended to <bin>/calls as "<tool> <args>".
//
//   - swift answers `--show-codecov-path` with <cwd>/fakeSwiftCodecov (exit
//     showExit). Any other call plays a coverage run and exits runExit: unless
//     json is "" (a build that failed), it writes two profiles into the
//     codecov directory, one named with a space as SwiftPM's "Swift Testing"
//     profiles are, and puts into the products directory two products that
//     carry a coverage map (CalcTests.so, CalcTests-test-runner), a file that
//     does not (description.json), and a directory holding an object that does
//     (CalcTests.build/a.o, which is not a product).
//   - llvm-objdump --section-headers F prints F: a stand-in object's sections
//     are its text, so one "carries a coverage map" when it holds __llvm_covmap.
//   - llvm-profdata merge -sparse -o OUT IN... writes the inputs to OUT.
//   - llvm-cov export prints json with the fixture's directory replaced by the
//     working directory.
func fakeSwift(t *testing.T, json string, showExit, runExit int) (bin string) {
	t.Helper()
	bin = t.TempDir()
	src := filepath.Join(bin, "export.json")
	if err := os.WriteFile(src, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := shellQuote(filepath.Join(bin, "calls"))
	log := "echo \"$(basename \"$0\") $*\" >> " + calls + "\n"
	swift := "#!/bin/sh\n" + log +
		"p=\"$(pwd -P)/" + fakeSwiftCodecov + "\"; c=\"${p%/*}\"; b=\"${c%/*}\"\n" +
		"case \" $* \" in *' --show-codecov-path '*) echo \"$p\"; exit " + strconv.Itoa(showExit) + ";; esac\n" +
		"echo 'the suite ran' >&2\n"
	if json != "" {
		swift += "mkdir -p \"$c\" \"$b/CalcTests.build\" || exit 9\n" +
			"echo profile > \"$c/XCTest1_0.1.profraw\"; echo profile > \"$c/Swift Testing1_0.1.profraw\"\n" +
			"echo 'section __llvm_covmap' > \"$b/CalcTests.so\"; echo 'section __llvm_covmap' > \"$b/CalcTests-test-runner\"\n" +
			"echo '{}' > \"$b/description.json\"; echo 'section __llvm_covmap' > \"$b/CalcTests.build/a.o\"\n"
	}
	swift += "exit " + strconv.Itoa(runExit) + "\n"
	tools := map[string]string{
		"swift":         swift,
		"llvm-objdump":  "#!/bin/sh\n" + log + "[ \"$1\" = --section-headers ] && cat \"$2\"\n",
		"llvm-profdata": "#!/bin/sh\n" + log + "[ \"$1 $2 $3\" = 'merge -sparse -o' ] || exit 7\nout=$4; shift 4; printf '%s\\n' \"$@\" > \"$out\"\n",
		"llvm-cov":      "#!/bin/sh\n" + log + "[ \"$1\" = export ] || exit 7\nsed \"s#" + swiftCodecovPrefix + "#$(pwd -P)/#g\" " + shellQuote(src) + "\n",
	}
	for name, script := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- an executable the test runs
			t.Fatal(err)
		}
	}
	systemTools(t, bin)
	return bin
}

// systemTools links into dir the system tools the coverage script runs, so a
// directory holding them can be the whole PATH.
func systemTools(t *testing.T, dir string) {
	t.Helper()
	for _, tool := range []string{"sh", "sed", "mktemp", "rm", "mkdir", "dirname", "basename", "awk", "cat", "pwd", "tail", "readlink", "grep"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue // a shell builtin on this machine
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeSwiftCalls is what the stand-in toolchain in bin was called with, one
// call per line.
func fakeSwiftCalls(t *testing.T, bin string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(bin, "calls")) // #nosec G304 -- the stand-in's own log
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// fakeSwiftCall is the one call of tool in calls, or "" with an error when
// there is not exactly one.
func fakeSwiftCall(t *testing.T, calls []string, tool string) string {
	t.Helper()
	var got []string
	for _, c := range calls {
		if strings.HasPrefix(c, tool+" ") {
			got = append(got, c)
		}
	}
	if len(got) != 1 {
		t.Errorf("want one %s call, got %q", tool, got)
		return ""
	}
	return got[0]
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
				c := exec.Command(bin, llvmCovExportReduce, f) // #nosec G204 -- the reducer under test
				c.Env = []string{"CORRAL_LLVMCOV_A=" + swiftCodecovPrefix, "CORRAL_LLVMCOV_B=" + swiftCodecovPrefix,
					"CORRAL_LLVMCOV_SKIP=" + llvmCovSkipped(swiftPlugin{}).String()}
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

// NO REPORT IS A DISCLOSED FAILURE, never an empty one. A run that did not
// pass is not read, though it may have written profiles: a test process that
// crashes writes none for itself, and the rest would read as the whole suite
// (measured on Swift 6.3.2 and 6.4.2, see CoverageCmd). A run that passed and
// wrote no profile, or found no product carrying a coverage map, is disclosed
// too; and when SwiftPM cannot say where its coverage goes, or the toolchain's
// LLVM tools are not beside its swift, the suite is not run at all.
func TestSwiftCoverageUnavailableIsAnError(t *testing.T) {
	r := swiftPlugin{}
	report := "Building for debugging...\n" + swiftCoverageUnavailable + " swift test exited 1, so its coverage is not read\n"
	for name, parse := range map[string]func() error{
		"CoverageCounts": func() error { _, err := r.CoverageCounts(report, ""); return err },
		"ParseCoverage":  func() error { _, err := r.ParseCoverage(report, ""); return err },
	} {
		if err := parse(); err == nil || !strings.Contains(err.Error(), "swift test exited 1, so its coverage is not read") {
			t.Errorf("%s: want an error carrying the reason, got %v", name, err)
		}
	}
	if _, err := r.CoverageCounts(swiftCoverageUnavailable+"\n", ""); err == nil || !strings.Contains(err.Error(), "no reason given") {
		t.Errorf("an unavailable line with no reason must still be an error that says so, got %v", err)
	}
	if reason, ok := CoverageUnavailable(report); !ok || !strings.HasPrefix(reason, "swift test exited 1") {
		t.Errorf("CoverageUnavailable = %q, %v: the Swift notice must be one the callers read", reason, ok)
	}

	for _, tc := range []struct {
		name    string
		json    string
		runExit int
		want    string
	}{
		{"failing run that wrote profiles", readFixture(t, "swift-codecov.json"), 1, "swift test exited 1, so its coverage is not read"},
		{"build that failed", "", 1, "swift test exited 1, so its coverage is not read"},
		{"passing run that wrote no profile", "", 0, "swift test exited 0 and wrote no coverage profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := fakeSwift(t, tc.json, 0, tc.runExit)
			stdout, _ := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), bin)
			_, err := r.CoverageCounts(stdout, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want the unavailable reason %q, got %v\nstdout:\n%s", tc.want, err, stdout)
			}
			if rc, ok := SuiteExit(stdout); !ok || rc != tc.runExit {
				t.Errorf("SuiteExit = %d, %v; want the suite's %d", rc, ok, tc.runExit)
			}
			for _, c := range fakeSwiftCalls(t, bin) {
				if strings.HasPrefix(c, "llvm-") && !strings.HasPrefix(c, "llvm-objdump ") {
					t.Errorf("a run that is not read was merged or exported: %s", c)
				}
			}
		})
	}

	t.Run("passing run with no product carrying a coverage map", func(t *testing.T) {
		bin := fakeSwift(t, readFixture(t, "swift-codecov.json"), 0, 0)
		// The stand-in's products are recognised by their text; an objdump that
		// finds no coverage map in any of them is a products directory with none.
		if err := os.WriteFile(filepath.Join(bin, "llvm-objdump"), []byte("#!/bin/sh\necho 'no sections'\n"), 0o700); err != nil { // #nosec G306 -- an executable the test runs
			t.Fatal(err)
		}
		stdout, _ := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), bin)
		if _, err := r.CoverageCounts(stdout, ""); err == nil || !strings.Contains(err.Error(), "carries a coverage map") {
			t.Errorf("want the unavailable reason naming the missing coverage map, got %v\nstdout:\n%s", err, stdout)
		}
	})

	t.Run("no codecov path", func(t *testing.T) {
		bin := fakeSwift(t, readFixture(t, "swift-codecov.json"), 1, 0)
		stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), bin)
		_, err := r.CoverageCounts(stdout, "")
		if err == nil || !strings.Contains(err.Error(), "--show-codecov-path") {
			t.Errorf("want the unavailable reason naming --show-codecov-path, got %v\nstdout:\n%s", err, stdout)
		}
		if strings.Contains(stderr, "the suite ran") {
			t.Errorf("the suite ran although its profiles could never be found:\n%s", stderr)
		}
		if calls := fakeSwiftCalls(t, bin); len(calls) != 1 {
			t.Errorf("want only the --show-codecov-path call, got:\n%s", strings.Join(calls, "\n"))
		}
	})

	for _, tool := range []string{"llvm-profdata", "llvm-cov", "llvm-objdump"} {
		t.Run("no "+tool+" beside swift", func(t *testing.T) {
			bin := fakeSwift(t, readFixture(t, "swift-codecov.json"), 0, 0)
			if err := os.Remove(filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
			stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), bin)
			if _, err := r.CoverageCounts(stdout, ""); err == nil || !strings.Contains(err.Error(), filepath.Join(bin, tool)) {
				t.Errorf("want the unavailable reason naming %s, got %v\nstdout:\n%s", filepath.Join(bin, tool), err, stdout)
			}
			if strings.Contains(stderr, "the suite ran") {
				t.Errorf("the suite ran although its profiles could never be read:\n%s", stderr)
			}
		})
	}
}

// THE PROFILES ARE MERGED AND EXPORTED OVER EVERY PRODUCT, by the mechanism
// Rust's coverage shares (llvmCovMergeExportShell): the profiles the run wrote
// into SwiftPM's codecov directory, none left by an earlier run, and as
// objects exactly the files of the products directory (the codecov
// directory's parent) that carry a coverage map, so neither a file without one
// nor an object file inside a target's build directory is handed to llvm-cov.
func TestSwiftCoverageExportsEveryProduct(t *testing.T) {
	dir := physicalTempDir(t)
	codecov := filepath.Dir(filepath.Join(dir, fakeSwiftCodecov))
	if err := os.MkdirAll(codecov, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codecov, "stale_0.1.profraw"), []byte("an earlier run's"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := fakeSwift(t, readFixture(t, "swift-codecov.json"), 0, 0)
	stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, dir, bin)
	if _, err := (swiftPlugin{}).CoverageCounts(stdout, ""); err != nil {
		t.Fatalf("CoverageCounts: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	calls := fakeSwiftCalls(t, bin)
	merge := fakeSwiftCall(t, calls, "llvm-profdata")
	if merge != "" {
		for _, want := range []string{filepath.Join(codecov, "XCTest1_0.1.profraw"), filepath.Join(codecov, "Swift Testing1_0.1.profraw")} {
			if !strings.Contains(merge, want) {
				t.Errorf("the merge was not given %s: %s", want, merge)
			}
		}
		if strings.Contains(merge, "stale") {
			t.Errorf("an earlier run's profile was merged: %s", merge)
		}
	}
	export := fakeSwiftCall(t, calls, "llvm-cov")
	products := filepath.Dir(codecov)
	var objects []string
	fields := strings.Fields(export)
	for i, f := range fields {
		if f == "-object" && i+1 < len(fields) {
			objects = append(objects, fields[i+1])
		}
	}
	if want := []string{filepath.Join(products, "CalcTests-test-runner"), filepath.Join(products, "CalcTests.so")}; !slices.Equal(objects, want) {
		t.Errorf("objects = %q, want %q (export call: %s)", objects, want, export)
	}
	for _, flag := range []string{"-format=text", "-summary-only", "-instr-profile="} {
		if !strings.Contains(export, flag) {
			t.Errorf("the export call lacks %s: %s", flag, export)
		}
	}
}

// THE LLVM TOOLS ARE THE SWIFT TOOLCHAIN'S OWN, found beside the swift the
// command runs after following its links, never elsewhere on PATH: CI puts a
// link to the pinned toolchain's swift first on PATH, in a directory of its
// own, and an llvm-profdata from another LLVM release cannot read the
// profile's format (Rust's coverage failed that way in CI).
func TestSwiftCoverageFindsTheToolchainsLLVMToolsThroughALink(t *testing.T) {
	bin := fakeSwift(t, readFixture(t, "swift-codecov.json"), 0, 0)
	links := t.TempDir()
	if err := os.Symlink(filepath.Join(bin, "swift"), filepath.Join(links, "swift")); err != nil {
		t.Fatal(err)
	}
	systemTools(t, links)
	stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), links)
	got, err := (swiftPlugin{}).CoverageCounts(stdout, "")
	if err != nil {
		t.Fatalf("CoverageCounts: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if want := (LineCounts{Executed: 3, Executable: 6}); got["Sources/Calc/Calc.swift"] != want {
		t.Errorf("Sources/Calc/Calc.swift = %+v, want %+v", got["Sources/Calc/Calc.swift"], want)
	}
}

// ONLY A PLAIN `swift test` RUN IS INSTRUMENTED, the shapes the row runs
// (swiftTestAt), and the coverage flags are added after the operator's own
// arguments. A command that names its own package or build directory is
// refused: the reducer places paths relative to the working directory and
// skips the build directory there, so a package or scratch path elsewhere
// would be read with the wrong paths. The coverage build has a scratch path
// of its own (see CoverageCmd), so --disable-code-coverage, which would
// leave it uninstrumented, is refused too. The build system is the
// operator's: the profiles are read over every product under native and
// swiftbuild alike, so none is added and none refused.
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
		{"swift", "test", "--build-system", "swiftbuild"},
		{"swift", "test", "--build-system=swiftbuild"},
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
		extra := args[len(c):]
		if !slices.Contains(extra, "--scratch-path") || (!slices.Contains(c, "--enable-code-coverage") && !slices.Contains(extra, "--enable-code-coverage")) {
			t.Errorf("CoverageCmd(%q) added %q, want --enable-code-coverage and its scratch path", c, extra)
		}
		if bs := swiftBuildSystems(extra); len(bs) != 0 {
			t.Errorf("CoverageCmd(%q) added build system %q; the operator's own is kept", c, bs)
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

// THE SKIPPED DIRECTORIES ARE MATCHED LITERALLY UNDER EVERY awk. awk -v
// processes escape sequences in the value it assigns, and gawk (the awk of
// GitHub's ubuntu-latest runner) turns the `\.` of `^(\.build|\.swiftpm)/`
// into `.`, warning "escape sequence `\.' treated as plain `.'", so a
// project directory such as Xbuild/ read as .build/ and was left out of the
// report; mawk kept the backslash. Run through the whole script, with awk on
// PATH being each variant this machine has.
func TestLLVMCovSkipIsLiteralUnderEveryAwk(t *testing.T) {
	raw := strings.ReplaceAll(readFixture(t, "swift-codecov.json"), swiftCodecovPrefix+"Sources/Calc/Dead.swift", swiftCodecovPrefix+"Xbuild/Dead.swift")
	ran := 0
	for _, awk := range []string{"awk", "mawk", "gawk"} {
		p, err := exec.LookPath(awk)
		if err != nil {
			continue
		}
		ran++
		t.Run(awk, func(t *testing.T) {
			bin := fakeSwift(t, raw, 0, 0)
			link := filepath.Join(bin, "awk")
			if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(p, link); err != nil {
				t.Fatal(err)
			}
			stdout, stderr := runSwiftCoverage(t, []string{"swift", "test"}, physicalTempDir(t), bin)
			got, err := swiftPlugin{}.CoverageCounts(stdout, "")
			if err != nil {
				t.Fatalf("CoverageCounts: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if want := (LineCounts{Executed: 0, Executable: 4}); got["Xbuild/Dead.swift"] != want {
				t.Errorf("Xbuild/Dead.swift = %+v, want %+v: a project directory was skipped as if it were .build\nstdout:\n%s", got["Xbuild/Dead.swift"], want, stdout)
			}
			if strings.Contains(stdout, ".build/") {
				t.Errorf("the build directory was not skipped:\n%s", stdout)
			}
			if strings.Contains(stderr, "escape sequence") {
				t.Errorf("awk rewrote an escape in the reducer's input:\n%s", stderr)
			}
		})
	}
	if ran == 0 {
		t.Skip("no awk on PATH")
	}
}
