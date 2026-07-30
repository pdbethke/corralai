// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFirstExecutableTokenSkipsLeadingEnvAssignments proves the fix for a
// legitimate, common operator idiom this codebase's own jail-command
// building uses too (see python.go's pyCachePrefixEnv): `-- PYTHONPATH=src
// pytest -q` names "pytest" as the program to run, not "PYTHONPATH=src".
func TestFirstExecutableTokenSkipsLeadingEnvAssignments(t *testing.T) {
	bin, ok := firstExecutableToken([]string{"PYTHONPATH=src", "pytest", "-q"})
	if !ok || bin != "pytest" {
		t.Fatalf("firstExecutableToken = (%q, %v), want (\"pytest\", true)", bin, ok)
	}
}

// TestFirstExecutableTokenSkipsMultipleLeadingEnvAssignments covers more
// than one assignment stacked, still a common shell idiom.
func TestFirstExecutableTokenSkipsMultipleLeadingEnvAssignments(t *testing.T) {
	bin, ok := firstExecutableToken([]string{"FOO=1", "BAR=2", "pytest", "-q"})
	if !ok || bin != "pytest" {
		t.Fatalf("firstExecutableToken = (%q, %v), want (\"pytest\", true)", bin, ok)
	}
}

// TestFirstExecutableTokenRejectsShellPipelines proves a `&&`-joined
// compound command (`cd sub && pytest -q`) is recognized as NOT a single
// program invocation — a preflight check has no one binary to safely name
// from it, and must decline rather than treating "cd" (a shell builtin,
// never a real executable) as the program.
func TestFirstExecutableTokenRejectsShellPipelines(t *testing.T) {
	for _, testCmd := range [][]string{
		{"cd", "sub", "&&", "pytest", "-q"},
		{"pytest", "-q", "||", "true"},
		{"pytest", ";", "echo", "done"},
	} {
		if _, ok := firstExecutableToken(testCmd); ok {
			t.Errorf("firstExecutableToken(%v) = ok, want false — a shell pipeline names no single executable", testCmd)
		}
	}
}

// TestFirstExecutableTokenRejectsRubyStockShellSnippet is the DIRECT
// regression test for the reported bug: rubyPlugin.TestCmd() returns a
// single argv element that is an entire multi-line shell script (built to
// be space-joined and run under `sh -c` inside the jail — see ruby.go's own
// doc comment on TestCmd), not a literal executable name. Feeding it to
// exec.LookPath as-is produced exactly the reported error: `required tool
// "t=\"$(ls *_test.rb ...\"... not found on PATH`.
func TestFirstExecutableTokenRejectsRubyStockShellSnippet(t *testing.T) {
	rp, ok := ByName("ruby")
	if !ok {
		t.Fatal("no ruby plugin registered")
	}
	stock := rp.TestCmd()
	if _, ok := firstExecutableToken(stock); ok {
		t.Fatalf("firstExecutableToken(ruby's stock TestCmd() = %v) = ok, want false — it is a shell snippet, not a literal executable", stock)
	}
}

// TestPreflightBinFallsBackForRubyStockShellSnippet is the end-to-end proof
// at the level `rubyPlugin.Preflight` actually calls: `certify --repo`'s
// own `localExecutor.testCmd` (cmd/corral/certify_repo.go) substitutes the
// plugin's stock TestCmd() whenever the operator gave no `-- <cmd>`, and
// THAT is what reaches Preflight as testCmd — not nil. Before the fix,
// preflightBin returned the raw shell snippet as the binary to check;
// after, it must fall back to the plugin's own stock default ("ruby"),
// host-independent (this asserts the RESOLVED name, not whether a real
// ruby binary happens to be installed on the test host).
func TestPreflightBinFallsBackForRubyStockShellSnippet(t *testing.T) {
	rp, ok := ByName("ruby")
	if !ok {
		t.Fatal("no ruby plugin registered")
	}
	got := preflightBin(rp.TestCmd(), "ruby")
	if got != "ruby" {
		t.Fatalf("preflightBin(ruby's stock TestCmd(), \"ruby\") = %q, want the fallback %q", got, "ruby")
	}
}

// TestPythonPreflightHonorsEnvPrefixedOperatorCommand is the second
// reported instance of the same root cause: a legitimately shell-shaped
// OPERATOR command (`-- PYTHONPATH=src pytest -q`, an idiom this codebase's
// own pyCachePrefixEnv uses too) runs fine in the jail (the jail executes it
// under `sh -c`) but must not be refused at preflight because
// "PYTHONPATH=src" isn't a real executable.
func TestPythonPreflightHonorsEnvPrefixedOperatorCommand(t *testing.T) {
	dir := t.TempDir()
	writeFakeExe(t, dir, "pytest", "pytest 7.4.0", 0)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p, _ := ByName("python")
	if err := p.Preflight([]string{"PYTHONPATH=src", "pytest", "-q"}); err != nil {
		t.Fatalf("an env-assignment-prefixed operator command must not be refused as if PYTHONPATH=src were the executable, got: %v", err)
	}
}

// TestPythonPreflightFallsBackForShellCompoundOperatorCommand covers the
// `-- cd sub && pytest -q` shape: no single executable can be safely named
// from it, so Preflight must fall back to its own stock check
// (pyPreflightStockDefault) rather than treating "cd" (a shell builtin) as
// the program to look up — which would fail closed for the WRONG reason
// (misdiagnosing a legitimate command as broken) rather than either passing
// on a real host toolchain or failing honestly on a genuinely absent one.
// This only asserts firstExecutableToken's verdict (host-independent);
// TestFirstExecutableTokenRejectsShellPipelines already pins the shape
// itself, so this pins that Preflight actually reaches the fallback branch
// instead of erroring on "cd" specifically.
func TestPythonPreflightFallsBackForShellCompoundOperatorCommand(t *testing.T) {
	p, _ := ByName("python")
	err := p.Preflight([]string{"cd", "sub", "&&", "pytest", "-q"})
	// Whatever the stock check decides (host-dependent: pass if this test
	// host has python3+pytest, fail otherwise) is fine — the property this
	// test pins is that it is NOT the "cd" naming error a literal-testCmd[0]
	// read would produce.
	if err != nil && strings.Contains(err.Error(), `"cd"`) {
		t.Fatalf("Preflight treated the shell builtin \"cd\" as the executable to check: %v", err)
	}
}

// writeFakeSleepingExe writes a fake executable that blocks forever (until
// killed) — used to prove the preflight probe cannot hang the whole audit
// on a wrapper binary that never returns.
func writeFakeSleepingExe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell-script executables are POSIX-only")
	}
	p := filepath.Join(dir, name)
	// `sleep infinity` (GNU) isn't portable; a very long, boundedly-finite
	// sleep is: this test's own timeout budget is orders of magnitude
	// shorter, so it never actually runs to completion.
	script := "#!/bin/sh\nsleep 3600\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatalf("write fake sleeping exe %s: %v", p, err)
	}
	return p
}

// lowered preflightProbeTimeout test bound. 300ms, not the 50ms this file
// used before the re-review: at 50ms, a "doubling" regression (elapsed
// ~100ms) is indistinguishable from ordinary process-spawn/scheduler jitter
// on a shared CI machine, so the old `elapsed > 2*time.Second` ceiling could
// never actually catch it — exactly the gap the re-review flagged. 300ms
// mirrors the coordinator's own reproduction value; loweredProbeCeiling
// below sits between the correct (~300-350ms) and doubled (~601ms observed
// in that repro) outcomes, so it actually discriminates the two.
const loweredProbeTimeout = 300 * time.Millisecond
const loweredProbeCeiling = 500 * time.Millisecond

// TestPythonPreflightProbeTimesOutRatherThanHanging is review item 5: with
// testCmd argv-aware, the probe's binary is now OPERATOR-named — no longer
// just pythonBin()'s two hardcoded choices — and a wrapper that blocks
// (a broken version-manager shim, a hung subprocess) must not hang the
// entire audit before --timeout's own budget even starts. Lowers the
// package var for the duration of this test so it runs fast rather than
// waiting out the real 10s production bound.
func TestPythonPreflightProbeTimesOutRatherThanHanging(t *testing.T) {
	orig := preflightProbeTimeout
	preflightProbeTimeout = loweredProbeTimeout
	t.Cleanup(func() { preflightProbeTimeout = orig })

	dir := t.TempDir()
	hung := writeFakeSleepingExe(t, dir, "python")

	p, _ := ByName("python")
	start := time.Now()
	err := p.Preflight([]string{hung, "-m", "pytest", "-q"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Preflight must fail when the probe binary hangs, not silently pass")
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Fatalf("error must say the probe TIMED OUT, not just that it failed: %v", err)
	}
	// Tight enough to actually catch a "waits an extra full bound after ctx
	// cancels" regression (WaitDelay alone, no process-group Cancel — the
	// re-review's exact repro: a 300ms bound observed taking 601ms), not
	// just a hang measured in whole seconds.
	if elapsed > loweredProbeCeiling {
		t.Fatalf("Preflight took %s (bound %s) — the probe is waiting roughly DOUBLE its bound, not just failing to bound the wait at all", elapsed, loweredProbeTimeout)
	}
}

// TestPythonPreflightStockDefaultProbeAlsoTimesOut proves the SAME bound
// applies to the no-testCmd stock path (pyPreflightStockDefault) — review
// item 5 notes this path had "the same shape" before (no timeout either),
// so a hung system pytest wrapper must be bounded too, not just the
// operator-named path.
func TestPythonPreflightStockDefaultProbeAlsoTimesOut(t *testing.T) {
	dir := t.TempDir()
	writeFakeSleepingExe(t, dir, "python3")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	orig := preflightProbeTimeout
	preflightProbeTimeout = loweredProbeTimeout
	t.Cleanup(func() { preflightProbeTimeout = orig })

	p, _ := ByName("python")
	start := time.Now()
	err := p.Preflight(nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Preflight (stock path) must fail when the probe binary hangs, not silently pass")
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Fatalf("error must say the probe TIMED OUT: %v", err)
	}
	if elapsed > loweredProbeCeiling {
		t.Fatalf("Preflight (stock path) took %s (bound %s) — the probe is waiting roughly DOUBLE its bound, not just failing to bound the wait at all", elapsed, loweredProbeTimeout)
	}
}

// TestPythonPreflightProbeDoesNotLeakProcesses is the other half of review
// item 2: a WaitDelay-only fix bounds the WAIT but does nothing about the
// process itself — exec.CommandContext's default Cancel only signals the
// DIRECT child, so a script interpreter's grandchild (a backgrounded
// worker, exactly the shape `sh -c "sleep 120 & echo hello; exit 0"`
// reproduces per sandbox.GuardProcess's own doc comment) survives as a
// leaked, still-running process after Preflight returns. GuardProcess's
// process-group kill must reap it.
func TestPythonPreflightProbeDoesNotLeakProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}
	orig := preflightProbeTimeout
	preflightProbeTimeout = loweredProbeTimeout
	t.Cleanup(func() { preflightProbeTimeout = orig })

	dir := t.TempDir()
	p := filepath.Join(dir, "python")
	// Backgrounds a grandchild that inherits the pipe and outlives the
	// direct child (`sh`) by design — the exact shape GuardProcess's own
	// doc comment names as the non-obvious failure mode.
	marker := filepath.Join(dir, "still-running")
	script := "#!/bin/sh\n(sleep 3600; ) & echo $! > " + marker + "\nsleep 3600\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}

	plug, _ := ByName("python")
	if err := plug.Preflight([]string{p, "-m", "pytest", "-q"}); err == nil {
		t.Fatal("Preflight must fail on a hung probe binary")
	}

	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the backgrounded grandchild's pid marker was never written — the test fixture itself is broken: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("could not parse the marked pid %q: %v", pidBytes, err)
	}
	// A brief grace period for the kill to actually land — process
	// teardown is not synchronous with Preflight's return.
	time.Sleep(300 * time.Millisecond)
	if proc, err := os.FindProcess(pid); err == nil {
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			t.Fatalf("grandchild pid %d is still running after Preflight returned — GuardProcess's process-group kill did not reap it", pid)
		}
	}
}
