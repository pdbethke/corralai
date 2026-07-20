// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"context"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestScoreJSNonTerminatingMutantIsKilledFast is the integration proof for
// the whole-run-hang bug this fix closes: a mutant that turns a function
// into an infinite loop must NOT make Score (and therefore `corral certify
// --local`) hang until the run's entire --timeout budget is exhausted. It
// must be scored as a fast KILL instead, bounded by the auto-derived
// per-mutant timeout (a small multiple of the healthy baseline's own
// runtime), not by the whole-run deadline.
//
// This exercises the REAL bwrap jail end-to-end (a real node process, real
// sandboxed exec, real sandbox.RunGuarded timeout plumbing) — not a fake
// Jail — so it is the test that would have caught the original bug (a fake
// Jail can't observe a hang at all). JavaScript/node:test is used (rather
// than python/pytest, mirroring TestScoreJSKillsAndSurvives) because node is
// builtin with no external deps and is what actually runs, not skips, on a
// dev host where pytest is a --user pip install invisible inside the jail's
// tmpfs $HOME (see the JAIL-VISIBILITY gotcha: frameworks must be installed
// system-wide under /usr for the sandboxed process to see them). It still
// skips cleanly (t.Skipf, never t.Fatal) whenever node or the jail itself is
// unavailable on the host.
func TestScoreJSNonTerminatingMutantIsKilledFast(t *testing.T) {
	js, ok := lang.ByName("javascript")
	if !ok {
		t.Skip("javascript plugin not registered")
	}
	if err := js.Preflight(); err != nil {
		t.Skipf("node not available: %v", err)
	}
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("no bwrap backend: %v", err)
	}
	// The jail's OWN construction timeout is a generous ceiling for the
	// baseline (mirrors certify_local.go's NewJail(iso, *timeout) — the
	// baseline gets the full run budget to pass once); the per-MUTANT cap is
	// what actually bounds the hang, auto-derived inside Score itself.
	jail := adequacy.NewJail(backend, 5*time.Minute)

	const codePath = "loopmod.js"
	// Compliant code: a normal function the baseline test calls and returns
	// immediately from.
	compliant := "function maybeLoop(n){ return n; }\nmodule.exports = { maybeLoop };\n"
	// Mutant: the SAME function rewritten as a genuine non-terminating loop —
	// once the test below calls it, the node process never returns on its
	// own; only the jail's timeout can end the run.
	mutants := []adequacy.Mutant{
		{ID: "m-infinite-loop", Code: "function maybeLoop(n){ while (true) {} }\nmodule.exports = { maybeLoop };\n"},
	}
	tp := js.TestPath(codePath)
	test := "const { test } = require('node:test');\nconst assert = require('node:assert');\nconst { maybeLoop } = require('./loopmod.js');\ntest('maybe loop', () => {\n  assert.strictEqual(maybeLoop(1), 1);\n});\n"

	start := time.Now()
	rep, err := adequacy.Score(context.Background(), jail,
		map[string]string{tp: test}, codePath, compliant, mutants, js.TestCmd())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Score must not error on a non-terminating mutant (it should be scored a kill, not abort): %v", err)
	}
	if !rep.CompliantPass {
		t.Skipf("jail did not pass a known-correct test against compliant code — treating as jail/toolchain unavailable rather than a test bug: %+v", rep)
	}

	// The per-mutant timeout is auto-derived as clampMutantTimeout(baseDur):
	// at most 5 minutes even in the worst case, but for a sub-second node:test
	// baseline it floors at 30s. Either way the WHOLE Score call — baseline
	// plus one hanging mutant — must complete well inside a couple of
	// minutes, not silently run until some much larger --timeout (this repo
	// saw a real 25-minute hang from exactly this bug). 3 minutes is a very
	// generous ceiling that only a regression back to the old
	// whole-run-timeout behavior (or a very slow CI host) would blow past.
	if elapsed > 3*time.Minute {
		t.Fatalf("Score took %s against a single non-terminating mutant — the per-mutant timeout is not bounding the hang", elapsed)
	}

	if len(rep.Killed) != 1 || rep.Killed[0] != "m-infinite-loop" {
		t.Fatalf("the non-terminating mutant must be scored KILLED (non-termination = a detected divergence from the healthy baseline), got killed=%v survived=%v", rep.Killed, rep.Survived)
	}
	if len(rep.Survived) != 0 {
		t.Fatalf("no survivors expected: %+v", rep)
	}

	t.Logf("non-terminating mutant killed via timeout in %s (bounded by the auto-derived per-mutant cap, not the whole-run budget)", elapsed)
}
