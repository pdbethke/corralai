// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// TestWorkspaceRunnerPythonPycacheStaleness reproduces, deterministically,
// the __pycache__ soundness hole on the WORKSPACE substrate: it mutates the
// SAME real checkout in place across every run (unlike the jail, which
// materializes a fresh, disposable temp directory per run — see
// jail.go's writeWorkspace), so a mutant written back to the SAME path at
// the SAME byte length within the SAME wall-clock second as the run that
// populated CPython's .pyc cache for that path can hit that STALE cache
// instead of recompiling — the mutant never actually executes, and a suite
// that would certainly have failed against it reads as a false pass (a
// phantom "survivor" in adequacy.Score terms).
//
// Racing an actual wall-clock second would make this test flaky (it would
// only fail intermittently, depending on host speed and second-boundary
// luck) — precisely the kind of test superpowers:test-driven-development
// forbids. Instead this FORCES the condition: os.Chtimes pins the source
// file to one fixed timestamp before every write, byte-for-byte replicating
// what "same mtime, same size" means to CPython's cache key without
// depending on how fast the test process happens to run.
func TestWorkspaceRunnerPythonPycacheStaleness(t *testing.T) {
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	// Guard with the plugin's OWN preflight rather than a hand-rolled probe.
	// This test grades THROUGH the python plugin, so "can this host run it"
	// has exactly one correct answer and the plugin already owns it —
	// python3 (or python) present AND pytest importable under it.
	//
	// The previous guard checked only for an interpreter. Every GitHub runner
	// has python3; none has pytest by default. So on a bare runner this test
	// FAILED rather than skipping — and that failure is precisely what made
	// corral's first CI self-audit of its own repo report `COULD-NOT-GRADE …
	// ungradable: 1 (baseline-failed)`: the suite under audit could not pass
	// in a clean environment, so nothing could be graded against it.
	//
	// Skipping (not failing) is the established shape here: a missing
	// toolchain makes a language test prove nothing, and deploy.yml's
	// `-v` skip census exists so that a skip is never mistaken for a pass.
	if err := p.Preflight(nil); err != nil {
		t.Skipf("python toolchain cannot run this test on this host (%v) — this test is about __pycache__ handling, not about provisioning", err)
	}

	const original = "def add(a, b):\n    return a + b\n"
	// mutant is a single-point flip of the operator, deliberately the SAME
	// byte length as original (both 32 bytes) — the exact shape the
	// __pycache__ key (mtime_seconds, size) cannot distinguish from the
	// original when the mtime also matches.
	const mutant = "def add(a, b):\n    return a - b\n"
	if len(original) != len(mutant) {
		t.Fatalf("fixture bug: original/mutant must be the same byte length, got %d/%d", len(original), len(mutant))
	}
	const test = "from mod import add\n\ndef test_add():\n    assert add(2, 3) == 5\n"

	fixedMtime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// writeAndPin overlays mod.py with content and forces its mtime to the
	// SAME fixed instant, every time — removing wall-clock timing from the
	// mtime side of the cache key entirely.
	writeAndPin := func(root, content string) {
		t.Helper()
		modPath := filepath.Join(root, "mod.py")
		if err := os.WriteFile(modPath, []byte(content), 0o644); err != nil {
			t.Fatalf("writing mod.py: %v", err)
		}
		if err := os.Chtimes(modPath, fixedMtime, fixedMtime); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	newWorkspace := func(t *testing.T, opts ...adequacy.WorkspaceOption) (root string, w *adequacy.WorkspaceRunner) {
		t.Helper()
		root = t.TempDir()
		writeAndPin(root, original)
		if err := os.WriteFile(filepath.Join(root, "test_mod.py"), []byte(test), 0o644); err != nil {
			t.Fatalf("writing test_mod.py: %v", err)
		}
		return root, adequacy.NewWorkspaceRunner(root, 30*time.Second, opts...)
	}

	testCmd := []string{"python3", "-m", "pytest", "-q"}
	ctx := context.Background()

	// --- Reproduction: no per-run env, forced identical mtime+size. ---
	// This proves the hole is real and is exactly the (mtime, size) cache
	// key — not a coincidence of timing — by removing timing from the
	// equation and STILL getting a false pass.
	t.Run("without fix, forced identical mtime and size survives", func(t *testing.T) {
		root, w := newWorkspace(t)
		ok, out, err := w.RunTestVerbose(ctx, map[string]string{}, testCmd)
		if err != nil || !ok {
			t.Fatalf("baseline (compliant code) must pass: ok=%v err=%v out=%s", ok, err, out)
		}

		writeAndPin(root, mutant)
		ok, out, err = w.RunTestVerbose(ctx, map[string]string{}, testCmd)
		if err != nil {
			t.Fatalf("mutant run errored: %v (out=%s)", err, out)
		}
		if !ok {
			t.Fatalf("expected the unfixed reproduction to FALSELY pass (stale .pyc hit) — got a genuine failure instead (out=%s); this reproduction may be stale on this host/python version", out)
		}
	})

	// --- The fix: WithPerRunEnv(pyPlugin.WorkspaceRunEnv), same forced
	// mtime+size. Must genuinely re-execute the mutant and fail. ---
	t.Run("with WorkspaceRunEnv fix, forced identical mtime and size is caught", func(t *testing.T) {
		root, w := newWorkspace(t, adequacy.WithPerRunEnv(p.WorkspaceRunEnv))
		ok, out, err := w.RunTestVerbose(ctx, map[string]string{}, testCmd)
		if err != nil || !ok {
			t.Fatalf("baseline (compliant code) must pass: ok=%v err=%v out=%s", ok, err, out)
		}

		writeAndPin(root, mutant)
		ok, out, err = w.RunTestVerbose(ctx, map[string]string{}, testCmd)
		if err != nil {
			t.Fatalf("mutant run errored: %v (out=%s)", err, out)
		}
		if ok {
			t.Fatalf("mutant SURVIVED under the fix (stale .pyc still read) — WorkspaceRunEnv did not close the hole; out=%s", out)
		}
	})

	// --- A shared prefix reused across calls is NOT the fix: setting
	// PYTHONPYCACHEPREFIX once and reusing it for both the baseline and
	// the mutant still lets the mutant hit the baseline's own cache entry
	// in that shared directory. Only a FRESH directory per call closes
	// the hole — this guards against a future "helpful" simplification
	// that hoists the temp dir out of WorkspaceRunEnv and computes it
	// once. ---
	t.Run("a single shared PYTHONPYCACHEPREFIX across calls is still vulnerable", func(t *testing.T) {
		sharedDir := t.TempDir()
		sharedEnv := func() (env []string, cleanup func()) {
			return []string{"PYTHONPYCACHEPREFIX=" + sharedDir}, func() {}
		}
		root, w := newWorkspace(t, adequacy.WithPerRunEnv(sharedEnv))
		ok, out, err := w.RunTestVerbose(ctx, map[string]string{}, testCmd)
		if err != nil || !ok {
			t.Fatalf("baseline (compliant code) must pass: ok=%v err=%v out=%s", ok, err, out)
		}

		writeAndPin(root, mutant)
		ok, out, err = w.RunTestVerbose(ctx, map[string]string{}, testCmd)
		if err != nil {
			t.Fatalf("mutant run errored: %v (out=%s)", err, out)
		}
		if !ok {
			t.Fatalf("expected a REUSED prefix dir to still falsely pass (this proves per-call freshness, not just redirection, is what matters) — got a genuine failure instead (out=%s); if this is now failing intentionally after a real fix, update this test's premise", out)
		}
	})
}
