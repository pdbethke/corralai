// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// An audit costs O(mutants × the TARGET's suite runtime), because scoring runs
// the whole suite once per mutant. Measured 2026-07-31: 1.46s/suite for
// pallets/flask but 77s for psf/requests, where the suite is ~96% of a file's
// audit. A repo with a 2-minute suite and 25 audited files is ~35 hours of
// compute per audit — the arithmetic that makes a hosted tier impossible.
//
// Running only the file's OWN paired test collapses that multiplier. It is NOT
// merely an optimisation, which is why it is an explicit option rather than a
// silent default: it changes the QUESTION being answered from "did anything in
// this repo catch the bug?" to "do the tests for this file actually test this
// file?" — and a mutant some unrelated test happened to catch now reads as a
// survivor, so the reported gap count goes UP.
func TestPythonFileScopedTestCmd(t *testing.T) {
	p, _ := ByName("python")
	fs, ok := p.(FileScopedTester)
	if !ok {
		t.Fatal("the python plugin must implement FileScopedTester — pytest takes a path directly")
	}

	got, ok := fs.FileScopedTestCmd("tests/test_cli.py")
	if !ok {
		t.Fatal("FileScopedTestCmd returned not-ok for a real test path")
	}
	if len(got) == 0 || got[len(got)-1] != "tests/test_cli.py" {
		t.Fatalf("FileScopedTestCmd = %v, want the test path as the final argument", got)
	}
	// It must still be a pytest invocation, not a bare path.
	joined := ""
	for _, a := range got {
		joined += a + " "
	}
	if !contains(joined, "pytest") {
		t.Fatalf("FileScopedTestCmd = %v, want a pytest invocation", got)
	}
}

// TestPythonFileScopedTestCmd_EmptyPathOptsOut pins the fail-closed default: with
// no paired test path there is nothing to scope TO, and returning a bare `pytest`
// would silently run the WHOLE suite while the caller believed it was scoped —
// the expensive behaviour the option exists to avoid, now invisible.
func TestPythonFileScopedTestCmd_EmptyPathOptsOut(t *testing.T) {
	p, _ := ByName("python")
	fs := p.(FileScopedTester)
	if _, ok := fs.FileScopedTestCmd(""); ok {
		t.Fatal("an empty test path must opt OUT, not silently widen to the whole suite")
	}
}

// TestNonPythonPluginsDoNotClaimFileScoping pins that a language whose
// file-scoped invocation has not been verified does NOT claim the interface.
// Guessing wrong here would run the wrong tests and silently change every kill
// rate for that language.
func TestNonPythonPluginsDoNotClaimFileScoping(t *testing.T) {
	for _, name := range []string{"go", "ruby", "javascript", "typescript"} {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("plugin %q not registered", name)
		}
		if _, claims := p.(FileScopedTester); claims {
			t.Errorf("%s claims FileScopedTester — only implement it with a verified per-file invocation for that runner", name)
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
