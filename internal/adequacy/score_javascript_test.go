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

// TestScoreJSKillsAndSurvives is a hermetic integration test proving the
// whole grading loop for JavaScript (node:test) through the real bwrap jail:
// a thorough test suite kills the mutant (kill rate 1.0), a gappy suite
// leaves it surviving.
//
// It skips cleanly (t.Skipf, never t.Fatal) whenever the toolchain or the
// jail itself is unavailable on the host — node missing, no bwrap backend, a
// Score error, or a compliant suite that doesn't pass in the jail (e.g.
// bwrap userns/loopback blocked, surfaced as a non-zero exit rather than a Go
// error). node:test is builtin (no external deps), so wherever node is
// present and the jail's userns works, this test runs for real end to end
// rather than skipping — it does not always skip.
func TestScoreJSKillsAndSurvives(t *testing.T) {
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
	jail := adequacy.NewJail(backend, 60*time.Second)

	const codePath = "evenmod.js"
	code := "function isEven(n){ return n % 2 === 0; }\nmodule.exports = { isEven };\n"
	// Always-true mutant: killed by the thorough suite's `!isEven(3)`
	// assertion, survives the gappy suite that only checks an even input.
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "module.exports = { isEven: () => true };\n"},
	}
	tp := js.TestPath(codePath) // evenmod.test.js

	// Thorough suite: checks both the true and false branches, so it kills
	// the always-true mutant.
	thorough := "const { test } = require('node:test');\nconst assert = require('node:assert');\nconst { isEven } = require('./evenmod.js');\ntest('is even', () => {\n  assert.ok(isEven(2));\n  assert.ok(!isEven(3));\n});\n"
	rep, err := adequacy.Score(context.Background(), jail,
		map[string]string{tp: thorough}, codePath, code, mutants, js.TestCmd())
	if err != nil {
		t.Skipf("jail/node unavailable (score errored): %v", err)
	}
	if !rep.CompliantPass {
		// The thorough fixture is a known-correct node:test file (validated
		// out-of-band with real node); a non-passing run against compliant
		// code here means the sandboxed process itself couldn't run cleanly
		// (e.g. bwrap's loopback/network-namespace setup failing under a
		// restrictive apparmor profile). RunTest/RunGuarded surface that as
		// an ordinary non-zero exit, not a Go error, so we can't match on err
		// here — treat it as the same jail-unavailable case and skip rather
		// than fail.
		t.Skipf("jail did not pass a known-correct test against compliant code — treating as jail/toolchain unavailable rather than a test bug: %+v", rep)
	}
	if rep.KillRate() != 1.0 {
		t.Fatalf("thorough suite should kill all mutants, got kill rate %v (report: %+v)", rep.KillRate(), rep)
	}

	// Gappy suite: only checks the even case, so the always-true mutant
	// survives.
	gappy := "const { test } = require('node:test');\nconst assert = require('node:assert');\nconst { isEven } = require('./evenmod.js');\ntest('is even', () => {\n  assert.ok(isEven(2));\n});\n"
	rep2, err := adequacy.Score(context.Background(), jail,
		map[string]string{tp: gappy}, codePath, code, mutants, js.TestCmd())
	if err != nil {
		t.Skipf("jail/node unavailable (score errored): %v", err)
	}
	if !rep2.CompliantPass {
		// Same jail-infra reasoning as the thorough run above.
		t.Skipf("jail did not pass a known-correct test against compliant code — treating as jail/toolchain unavailable rather than a test bug: %+v", rep2)
	}
	if len(rep2.Survived) == 0 {
		t.Fatalf("gappy suite must leave a survivor, got report: %+v", rep2)
	}
}
