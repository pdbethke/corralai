// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// writeFakeExe writes a tiny shell script at dir/name that prints stdout and
// exits with code, then returns its full path. Used to build fake
// interpreters/toolchains the test controls completely — never the real
// system python/pytest, so these tests do not depend on (or risk being
// confused by) whatever happens to be installed on the host running them.
func writeFakeExe(t *testing.T, dir, name, stdout string, code int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell-script executables are POSIX-only")
	}
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\necho '" + stdout + "'\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatalf("write fake exe %s: %v", p, err)
	}
	return p
}

// TestPythonPreflightHonorsExplicitVenvInterpreter is the fix for the real
// bug: a venv interpreter the operator names after `--` (e.g.
// /tmp/flask/.venv/bin/python) is NOT on PATH under its bare name, and the
// stock pythonBin() guess (python3/python off PATH) has no way to see it.
// Preflight must validate THAT interpreter, not the host's system one.
func TestPythonPreflightHonorsExplicitVenvInterpreter(t *testing.T) {
	dir := t.TempDir()
	venvPython := writeFakeExe(t, dir, "python", "pytest 7.4.0", 0)

	p, _ := ByName("python")
	if err := p.Preflight([]string{venvPython, "-m", "pytest", "-q"}); err != nil {
		t.Fatalf("Preflight with an explicit, working venv interpreter must pass, got: %v", err)
	}
}

// TestPythonPreflightFailsClosedNamingTheOperatorsInterpreter proves the
// gate still fails CLOSED when the operator's own named interpreter cannot
// actually run pytest — and that the error names the interpreter the
// operator gave, not a stock python3/python guess (the misdiagnosis this
// fix removes).
func TestPythonPreflightFailsClosedNamingTheOperatorsInterpreter(t *testing.T) {
	dir := t.TempDir()
	// A real, present, executable interpreter — but pytest is NOT
	// importable under it (exit 1, as `python -m pytest --version` would
	// report for a module that isn't installed).
	brokenPython := writeFakeExe(t, dir, "python", "No module named pytest", 1)

	p, _ := ByName("python")
	err := p.Preflight([]string{brokenPython, "-m", "pytest", "-q"})
	if err == nil {
		t.Fatal("Preflight must fail closed when the operator's interpreter cannot import pytest")
	}
	if !strings.Contains(err.Error(), brokenPython) {
		t.Fatalf("error must name the operator's own interpreter %q, got: %v", brokenPython, err)
	}
}

// TestPythonPreflightFailsClosedForAnAbsentExplicitInterpreter covers the
// operator naming a path that doesn't exist at all (a typo, a venv that was
// never created) — still a fail-closed refusal, not a silent fall-through
// to the host's stock python.
func TestPythonPreflightFailsClosedForAnAbsentExplicitInterpreter(t *testing.T) {
	p, _ := ByName("python")
	missing := filepath.Join(t.TempDir(), "does-not-exist", "python")
	err := p.Preflight([]string{missing, "-m", "pytest", "-q"})
	if err == nil {
		t.Fatal("Preflight must fail closed for an interpreter path that does not exist")
	}
}

// TestPythonPreflightBarePytestChecksThatBinaryDirectly covers the bare
// `pytest -q` shape (documented in action.yml / github-action.md as the
// canonical test-command example) — the check must validate the operator's
// own `pytest` binary, not silently substitute pythonBin().
func TestPythonPreflightBarePytestChecksThatBinaryDirectly(t *testing.T) {
	dir := t.TempDir()
	fakePytest := writeFakeExe(t, dir, "pytest", "pytest 7.4.0", 0)

	p, _ := ByName("python")
	if err := p.Preflight([]string{fakePytest, "-q"}); err != nil {
		t.Fatalf("Preflight with a working, explicitly-pathed pytest must pass, got: %v", err)
	}
}

// TestPythonPreflightUnrecognizedShapeStillChecksPresence covers an
// operator command this plugin cannot parse into an interpreter+module
// shape (e.g. a tox/poetry wrapper) — Preflight must not guess at an
// importability probe it cannot construct honestly, but it still owes a
// real presence check on the command's own first token.
func TestPythonPreflightUnrecognizedShapeStillChecksPresence(t *testing.T) {
	p, _ := ByName("python")

	if err := p.Preflight([]string{"tox-wrapper-that-does-not-exist-anywhere", "-e", "py311"}); err == nil {
		t.Fatal("Preflight must still fail closed when the operator's own command binary is absent, even in an unrecognized shape")
	}

	dir := t.TempDir()
	fakeTox := writeFakeExe(t, dir, "tox", "", 0)
	if err := p.Preflight([]string{fakeTox, "-e", "py311"}); err != nil {
		t.Fatalf("an unrecognized-but-present command must pass the presence check, got: %v", err)
	}
}
