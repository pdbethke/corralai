// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestPHPInterpreterPrefersAnExplicitVersionedArgv0 pins the fix's first
// rule: an operator's own test command naming an explicit php variant
// (php8.5, php8, ...) is used VERBATIM — it is the operator's own choice,
// stronger evidence than any stock guess, and needs no further resolution.
func TestPHPInterpreterPrefersAnExplicitVersionedArgv0(t *testing.T) {
	for _, tc := range []struct {
		name    string
		testCmd []string
		want    string
	}{
		{"bare php8.5 with flags and the phpunit target", []string{"php8.5", "-n", "-d", "extension_dir=/usr/lib/php/20230831", "vendor/bin/phpunit", "tests/"}, "php8.5"},
		{"bare php8", []string{"php8", "vendor/bin/phpunit"}, "php8"},
		{"unversioned php", []string{"php", "vendor/bin/phpunit"}, "php"},
		{"a path containing php8.3", []string{"/opt/php8.3/bin/php8.3", "vendor/bin/phpunit"}, "/opt/php8.3/bin/php8.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := phpInterpreter(tc.testCmd)
			if err != nil {
				t.Fatalf("phpInterpreter(%v): %v", tc.testCmd, err)
			}
			if got != tc.want {
				t.Errorf("phpInterpreter(%v) = %q, want %q verbatim (not further resolved)", tc.testCmd, got, tc.want)
			}
		})
	}
}

// TestPHPInterpreterFallsBackToLookPathWhenArgv0IsNotAPHPVariant covers the
// dominant real shape (bare `vendor/bin/phpunit`, or no testCmd at all):
// neither names a php interpreter directly, so phpInterpreter must fall
// back to resolving "php" itself — and the result must be an ABSOLUTE,
// symlink-free path (EvalSymlinks(got) == got), the form the sandbox's own
// /usr bind-mount can actually see, never the bare possibly-symlinked name.
func TestPHPInterpreterFallsBackToLookPathWhenArgv0IsNotAPHPVariant(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("no php on PATH — cannot exercise the LookPath fallback on this host")
	}
	for _, tc := range []struct {
		name    string
		testCmd []string
	}{
		{"bare vendor/bin/phpunit, no interpreter named", []string{"vendor/bin/phpunit", "tests/"}},
		{"no testCmd at all (CompileCheck's own case)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := phpInterpreter(tc.testCmd)
			if err != nil {
				t.Fatalf("phpInterpreter(%v): %v", tc.testCmd, err)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("phpInterpreter(%v) = %q, want an absolute path", tc.testCmd, got)
			}
			real, err := filepath.EvalSymlinks(got)
			if err != nil {
				t.Fatalf("EvalSymlinks(%q): %v", got, err)
			}
			if real != got {
				t.Errorf("phpInterpreter(%v) = %q, which is STILL a symlink (resolves further to %q) — the jail's mount table cannot follow it", tc.testCmd, got, real)
			}
		})
	}
}

// TestPHPInterpreterFollowsAMultiHopSymlinkChain is the direct proof for the
// acceptance-run bug: Debian's /usr/bin/php -> /etc/alternatives/php ->
// /usr/bin/php8.5 is a TWO-hop chain. This builds an equivalent chain from
// scratch (no dependency on the host's real php or alternatives system) and
// confirms phpInterpreter follows it all the way to the real, executable
// file — not merely one hop.
func TestPHPInterpreterFollowsAMultiHopSymlinkChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink chain fixture is POSIX-shaped")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "php8.5-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\necho fake php\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// hop 1: an "alternatives"-style indirection, hop 2: the /usr/bin/php
	// name a bare LookPath("php") would actually find.
	alt := filepath.Join(dir, "alternatives-php")
	if err := os.Symlink(real, alt); err != nil {
		t.Fatal(err)
	}
	binPHP := filepath.Join(dir, "php")
	if err := os.Symlink(alt, binPHP); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	got, err := phpInterpreter(nil)
	if err != nil {
		t.Fatalf("phpInterpreter(nil): %v", err)
	}
	if got != real {
		t.Errorf("phpInterpreter(nil) = %q, want the CHAIN'S FINAL real path %q", got, real)
	}
}

// TestPHPInterpreterErrorsWhenPHPIsNowhereToBeFound: with no php-variant
// argv0 and no "php" resolvable on PATH at all, phpInterpreter must fail
// closed, not return a guessed name that would only fail later, deeper
// inside a run.
func TestPHPInterpreterErrorsWhenPHPIsNowhereToBeFound(t *testing.T) {
	dir := t.TempDir() // empty: nothing named "php" anywhere on it
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", dir)

	if _, err := phpInterpreter(nil); err == nil {
		t.Fatal("phpInterpreter(nil) with no php on PATH must return an error, not a guess")
	}
}

// TestPHPCompileCheckUsesTheDerivedInterpreter is the fake-jail pin for
// item 1's other half: CompileCheck's own argv[0] must be EXACTLY what
// phpInterpreter(nil) resolves — not the bare literal "php" the acceptance
// run's 40 mutants all died on with "sh: 1: php: not found".
func TestPHPCompileCheckUsesTheDerivedInterpreter(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("no php on PATH — cannot exercise the real resolution on this host")
	}
	want, err := phpInterpreter(nil)
	if err != nil {
		t.Fatalf("phpInterpreter(nil): %v", err)
	}
	p, _ := ByName("php")
	cc := p.CompileCheck("Invoice.php", "InvoiceTest.php")
	for i, cmd := range cc {
		if len(cmd) == 0 || cmd[0] != want {
			t.Errorf("CompileCheck()[%d][0] = %q, want the derived interpreter %q", i, cmd, want)
		}
	}

	// The fake jail: actually RUN the derived command against a valid PHP
	// file, proving it is not just string-equal but genuinely executable —
	// the exact thing "sh: 1: php: not found" broke for all 40 mutants.
	dir := t.TempDir()
	valid := filepath.Join(dir, "Invoice.php")
	if err := os.WriteFile(valid, []byte("<?php\nclass Invoice {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(cc[0][0], "-l", valid).CombinedOutput()
	if err != nil {
		t.Fatalf("the derived interpreter could not run `-l` on a valid file: %v\n%s", err, out)
	}
}

// TestPHPJailPreflightRefusesADanglingInterpreter golden-tests the refusal
// text a jail-unreachable interpreter produces: it must name the
// /etc/alternatives trap and suggest an explicit interpreter in the test
// command, since that is the fix an operator reading it actually needs to
// make (exactly the shape the acceptance run's two burned attempts needed
// before spending anything).
func TestPHPJailPreflightRefusesADanglingInterpreter(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{})
	if err != nil {
		t.Skipf("no working sandbox backend on this host: %v", err)
	}
	err = phpJailPreflight(iso, filepath.Join(t.TempDir(), "no-such-php-here"))
	if err == nil {
		t.Fatal("phpJailPreflight must refuse an interpreter that cannot run inside the sandbox")
	}
	msg := err.Error()
	if !strings.Contains(msg, "/etc/alternatives") {
		t.Errorf("refusal %q must name the /etc/alternatives trap", msg)
	}
	if !strings.Contains(msg, "php8") && !strings.Contains(strings.ToLower(msg), "explicit interpreter") {
		t.Errorf("refusal %q must suggest naming an explicit interpreter in the test command", msg)
	}
}

// TestPHPJailPreflightPassesForARealInterpreter is the control: the SAME
// probe against a genuinely working, jail-visible interpreter must pass —
// this is not a blanket "any jail run fails" regression.
func TestPHPJailPreflightPassesForARealInterpreter(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{})
	if err != nil {
		t.Skipf("no working sandbox backend on this host: %v", err)
	}
	interp, err := phpInterpreter(nil)
	if err != nil {
		t.Skipf("no php on PATH to resolve for this control case: %v", err)
	}
	if err := phpJailPreflight(iso, interp); err != nil {
		t.Fatalf("phpJailPreflight(%q) must pass for a real, resolved interpreter: %v", interp, err)
	}
}

// fakePHPIsolator is a minimal sandbox.Isolator standing in for real bwrap
// mount semantics: it runs the probe command through a plain shell whose
// PATH is pinned to binDir ONLY. This reproduces the exact "a resolved
// absolute path works, but the bare env-resolved name does not" shape a
// real bwrap jail's /usr-only mount produces for a Debian /etc/alternatives
// chain — from scratch, so the test needs neither the host's own
// alternatives system nor writing into a real /usr.
type fakePHPIsolator struct{ binDir string }

func (fakePHPIsolator) Name() string     { return "fake" }
func (fakePHPIsolator) Preflight() error { return nil }
func (f fakePHPIsolator) Wrap(command string, _ sandbox.Options, _ []string) ([]string, error) {
	return []string{"sh", "-c", "PATH=" + f.binDir + " " + command}, nil
}

// mustFakePHPBin writes an executable "php version probe" script at
// binDir/name that exits 0, creating binDir if needed.
func mustFakePHPBin(t *testing.T, binDir, name string) {
	t.Helper()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPHPInterpreterAndProbeDiffersForStockVsExplicit pins F1's fix: the
// stock command (no interpreter of its own in argv) yields a probe of the
// BARE name "php" — what vendor/bin/phpunit's own `#!/usr/bin/env php`
// shebang asks env to resolve at run time, inside the JAIL's own PATH — even
// though the interpreter used for CompileCheck's direct invocation is the
// fully host-resolved absolute path. An explicit php8.5 command uses that
// SAME token for both: no shebang/env indirection is involved when the
// operator's own command execs it directly.
func TestPHPInterpreterAndProbeDiffersForStockVsExplicit(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("no php on PATH — cannot exercise the resolution on this host")
	}
	interp, probe, err := phpInterpreterAndProbe(nil)
	if err != nil {
		t.Fatalf("phpInterpreterAndProbe(nil): %v", err)
	}
	if probe != "php" {
		t.Errorf("phpInterpreterAndProbe(nil) probe = %q, want the bare name \"php\" (what env in phpunit's shebang resolves)", probe)
	}
	if !filepath.IsAbs(interp) {
		t.Errorf("phpInterpreterAndProbe(nil) interp = %q, want an absolute resolved path (still used for CompileCheck)", interp)
	}

	interp2, probe2, err := phpInterpreterAndProbe([]string{"php8.5", "vendor/bin/phpunit"})
	if err != nil {
		t.Fatalf("phpInterpreterAndProbe(explicit php8.5): %v", err)
	}
	if interp2 != "php8.5" || probe2 != "php8.5" {
		t.Errorf("phpInterpreterAndProbe(explicit php8.5) = (%q, %q), want (\"php8.5\", \"php8.5\") — no shebang indirection for an explicit interpreter", interp2, probe2)
	}
}

// TestPHPJailPreflightProbesTheBareNameForTheStockCommand is F1's central
// proof: with a fixture where the RESOLVED absolute path works but the bare
// name "php" is absent (the dangling-symlink shape), Preflight must REFUSE
// for the stock command (vendor/bin/phpunit, no interpreter named — the
// exact shape of the acceptance run's first paid-failure attempt) and PASS
// for an explicit php8.5 command.
func TestPHPJailPreflightProbesTheBareNameForTheStockCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	// Only the VERSIONED name exists — bare "php" is absent, modelling a
	// resolved-but-dangling-bare-name interpreter: phpInterpreter(nil)
	// would resolve to some OTHER real path outside bin/ on this host (or
	// fail, if none), but the STOCK command's own probe must go through
	// bin/ alone via the fake isolator below.
	mustFakePHPBin(t, bin, "php8.5")
	iso := fakePHPIsolator{binDir: bin}

	// The stock case: phpInterpreterAndProbe's own probe value for a
	// no-interpreter testCmd is the bare name "php" — absent from bin/ —
	// so phpJailPreflight must refuse.
	_, probe, err := phpInterpreterAndProbe([]string{"vendor/bin/phpunit"})
	if err != nil {
		t.Skipf("no php on PATH to resolve for the stock branch on this host: %v", err)
	}
	if jerr := phpJailPreflight(iso, probe); jerr == nil {
		t.Fatal("phpJailPreflight must REFUSE the stock command's probe when the bare name is absent from the jail's PATH")
	}

	// The explicit case: probe = "php8.5" itself, present in bin/, so it
	// must PASS — proving this is not a blanket "the fake jail always
	// fails" artifact.
	_, probe2, err := phpInterpreterAndProbe([]string{"php8.5", "vendor/bin/phpunit"})
	if err != nil {
		t.Fatalf("phpInterpreterAndProbe(explicit php8.5): %v", err)
	}
	if jerr := phpJailPreflight(iso, probe2); jerr != nil {
		t.Fatalf("phpJailPreflight must PASS for the explicit interpreter's own probe: %v", jerr)
	}
}

// TestPHPSuggestedInterpreterFallsBackToAFixedVersion is F3's pin: with no
// php[0-9.]* binaries anywhere on PATH, the suggestion falls back to a
// fixed, named version rather than the empty string or the bare "php" that
// dangled in the first place.
func TestPHPSuggestedInterpreterFallsBackToAFixedVersion(t *testing.T) {
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", t.TempDir()) // empty: nothing named php* anywhere on it

	got := phpSuggestedInterpreter()
	if got == "" || got == "php" {
		t.Fatalf("phpSuggestedInterpreter() = %q, want a concrete versioned fallback", got)
	}
}

// TestPHPSuggestedInterpreterPrefersAHostInstalledVersion: when a versioned
// php IS on PATH, the suggestion names THAT one rather than a hardcoded
// guess that might not even be installed.
func TestPHPSuggestedInterpreterPrefersAHostInstalledVersion(t *testing.T) {
	dir := t.TempDir()
	mustFakePHPBin(t, dir, "php9.9")
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", dir)

	if got := phpSuggestedInterpreter(); got != "php9.9" {
		t.Errorf("phpSuggestedInterpreter() = %q, want the host's own installed \"php9.9\"", got)
	}
}
