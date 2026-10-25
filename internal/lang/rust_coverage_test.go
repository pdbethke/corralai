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

// THE COUNTS COME FROM A REAL llvm-cov EXPORT of the fixture crate (the
// captured report TestCapturedCoverageReportsMatchALiveRun holds to a live
// run): src/calc.rs has a tested and an untested function, src/dead.rs is
// compiled and never called, src/unloaded.rs is in no module tree, and
// src/shape.rs has no executable line. The integration test file ran.
func TestRustCoverageCountsFromRealExport(t *testing.T) {
	got := capturedCounts(t, "rust")
	assertFixtureCounts(t, got, "src/calc.rs", "src/dead.rs", "src/unloaded.rs", "src/shape.rs")
	if c, ok := got["tests/calc_test.rs"]; !ok || c.Executed == 0 {
		t.Errorf("tests/calc_test.rs ran: want Executed>0, got present=%v %+v (full: %v)", ok, c, got)
	}
	if _, ok := got["src/lib.rs"]; ok {
		t.Errorf("src/lib.rs only declares modules, so llvm-cov maps no line of it: want ABSENT, got %+v", got["src/lib.rs"])
	}
}

// ONLY A `cargo test` COMMAND IS INSTRUMENTED. The test binaries llvm-cov
// reads are found by re-running the operator's own cargo arguments with
// --no-run, so a command whose arguments cannot be read that way — a shell
// wrapper, nextest's own flags, another tool — is refused rather than
// half-measured.
func TestRustCoverageCmdRefusesNonCargo(t *testing.T) {
	r := rustPlugin{}
	refused := [][]string{
		{},
		{"pytest", "-q"},
		{"go", "test", "./..."},
		{"npm", "test"},
		{"cargo"},
		{"cargo", "build"},
		{"cargo", "nextest", "run"},
		{"cargo-nextest", "nextest", "run"},
		{"sh", "-c", "cargo test"},
		{"cargo", "test", "--no-run"},
		{"cargo", "test", "--message-format=short"},
		{"cargo", "test", "--message-format", "short"},
		{"cargo", "--locked", "test"},
	}
	for _, c := range refused {
		if cmd, ok := r.CoverageCmd(c); ok {
			t.Errorf("CoverageCmd(%q) accepted: %v", c, cmd)
		}
	}
	accepted := [][]string{
		{"cargo", "test"},
		{"cargo", "t"},
		{"/opt/rust/bin/cargo", "test", "-p", "core"},
		{"cargo", "+stable", "test", "--all-features", "--", "--nocapture"},
	}
	for _, c := range accepted {
		cmd, ok := r.CoverageCmd(c)
		if !ok {
			t.Errorf("CoverageCmd(%q) refused a cargo test command", c)
			continue
		}
		if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
			t.Errorf("CoverageCmd(%q) = %q, want a sh -c script", c, cmd)
		}
	}
	// The binaries are listed with the operator's cargo arguments, never the
	// test binaries' (after --), and with the same toolchain.
	cmd, ok := r.CoverageCmd([]string{"cargo", "+stable", "test", "-p", "core", "--", "--nocapture"})
	if !ok {
		t.Fatal("CoverageCmd refused a cargo test command")
	}
	if !strings.Contains(cmd[2], `'cargo' '+stable' 'test' '-p' 'core' '--no-run' '--message-format=json'`) {
		t.Errorf("the --no-run listing does not reuse the operator's cargo arguments:\n%s", cmd[2])
	}
	if !strings.Contains(cmd[2], `'rustc' '+stable'`) {
		t.Errorf("the llvm-tools lookup does not ask the operator's toolchain:\n%s", cmd[2])
	}
}

// NO llvm-tools IS A DISCLOSED FAILURE, never an empty report. The parser
// turns the header's reason into an error; the live half runs the real
// script against a toolchain whose sysroot carries no llvm-tools and a PATH
// without them, and reads that reason back.
func TestRustCoverageUnavailableIsAnError(t *testing.T) {
	r := rustPlugin{}
	report := "   Compiling x v0.1.0\n" + rustCoverageUnavailable + " llvm-profdata and llvm-cov are not installed\n"
	for name, parse := range map[string]func() error{
		"CoverageCounts": func() error { _, err := r.CoverageCounts(report, ""); return err },
		"ParseCoverage":  func() error { _, err := r.ParseCoverage(report, ""); return err },
	} {
		err := parse()
		if err == nil || !strings.Contains(err.Error(), "llvm-profdata and llvm-cov are not installed") {
			t.Errorf("%s: want an error carrying the reason, got %v", name, err)
		}
	}
	if _, err := r.CoverageCounts(rustCoverageUnavailable+"\n", ""); err == nil || !strings.Contains(err.Error(), "no reason given") {
		t.Errorf("an unavailable header with no reason must still be an error that says so, got %v", err)
	}

	// Live: a fake rustc whose sysroot has no llvm-tools, and a PATH that
	// holds only the handful of tools the script needs before it looks.
	bin := t.TempDir()
	sysroot := t.TempDir()
	fake := "#!/bin/sh\ncase \"$*\" in *--print\\ sysroot*) echo " + shellQuote(sysroot) + ";; *-vV*) echo 'host: x86_64-unknown-linux-gnu';; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "rustc"), []byte(fake), 0o700); err != nil { // #nosec G306 -- an executable the test runs
		t.Fatal(err)
	}
	for _, tool := range []string{"sh", "sed", "mktemp", "rm"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd, ok := r.CoverageCmd([]string{"cargo", "test"})
	if !ok {
		t.Fatal("CoverageCmd refused cargo test")
	}
	c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
	c.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir()}
	c.Dir = t.TempDir()
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, runErr := c.Output()
	_, err := r.CoverageCounts(string(out), "")
	if err == nil || !strings.Contains(err.Error(), "llvm-tools") {
		t.Fatalf("want the unavailable reason naming llvm-tools, got %v\nstdout:\n%s\nstderr:\n%s\nrun error: %v", err, out, stderr.String(), runErr)
	}
	if strings.Contains(stderr.String(), "cargo") {
		t.Errorf("the suite ran although the report could never be read:\n%s", stderr.String())
	}
	// The remedy is TEXT. Written in backquotes inside the double-quoted
	// reason, the shell ran `rustup component add llvm-tools` as a command
	// substitution — on the workspace substrate that installs a component on
	// the operator's toolchain — and the reason read "install them with  for
	// the toolchain" (indexmap in the jail, Task 11 fix).
	if !strings.Contains(err.Error(), "rustup component add llvm-tools") {
		t.Errorf("the reason lost its remedy: %v", err)
	}
	if strings.Contains(stderr.String(), "rustup") {
		t.Errorf("the reason's remedy was executed:\n%s", stderr.String())
	}
}
