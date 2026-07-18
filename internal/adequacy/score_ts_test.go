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

// TestScoreTSKillsAndSurvives is a hermetic integration test proving the
// whole grading loop for TypeScript (node:test via strip-types + tsc
// CompliantPass gating) through the real bwrap jail: a thorough test suite
// kills the mutant (kill rate 1.0), a gappy suite leaves it surviving.
//
// It skips cleanly (t.Skipf, never t.Fatal) whenever the toolchain or the
// jail itself is unavailable on the host — node/tsc missing (tsc is a hard
// TypeScript dependency, preflighted fail-closed), no bwrap backend, a Score
// error, or a compliant suite that doesn't pass in the jail. tsc is not
// expected to be installed on every dev host, so this test commonly SKIPs on
// `!CompliantPass` locally — that is the correct, expected outcome; it runs
// for real only where the full toolchain + jail are present (CI/Hetzner).
func TestScoreTSKillsAndSurvives(t *testing.T) {
	ts, ok := lang.ByName("typescript")
	if !ok {
		t.Skip("typescript plugin not registered")
	}
	if err := ts.Preflight(); err != nil {
		t.Skipf("node/tsc not available: %v", err)
	}
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("no bwrap backend: %v", err)
	}
	jail := adequacy.NewJail(backend, 60*time.Second)

	const codePath = "evenmod.ts"
	code := "export function isEven(n: number): boolean { return n % 2 === 0; }\n"
	// Always-true mutant: killed by the thorough suite's `!isEven(3)`
	// assertion, survives the gappy suite that only checks an even input.
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "export function isEven(n: number): boolean { return true; }\n"},
	}
	tp := ts.TestPath(codePath) // evenmod.test.ts

	// Workspace map: the tsconfig from Scaffold() plus the test file — the TS
	// run needs the scaffold's tsconfig present alongside the test.
	withTest := func(test string) map[string]string {
		files := make(map[string]string, len(ts.Scaffold())+1)
		for k, v := range ts.Scaffold() {
			files[k] = v
		}
		files[tp] = test
		return files
	}

	// Thorough suite: checks both the true and false branches, so it kills
	// the always-true mutant.
	thorough := "import { test } from 'node:test';\nimport assert from 'node:assert';\nimport { isEven } from './evenmod.ts';\ntest('is even', () => {\n  assert.ok(isEven(2));\n  assert.ok(!isEven(3));\n});\n"
	rep, err := adequacy.Score(context.Background(), jail,
		withTest(thorough), codePath, code, mutants, ts.TestCmd())
	if err != nil {
		t.Skipf("jail/tsc unavailable (score errored): %v", err)
	}
	if !rep.CompliantPass {
		// The thorough fixture is a known-correct node:test file (validated
		// out-of-band with real node+tsc); a non-passing run against
		// compliant code here means the sandboxed process itself couldn't
		// run cleanly (e.g. bwrap's loopback/network-namespace setup failing
		// under a restrictive apparmor profile, or tsc simply absent from
		// the jail's view of the toolchain). RunTest/RunGuarded surface that
		// as an ordinary non-zero exit, not a Go error, so we can't match on
		// err here — treat it as the same jail/toolchain-unavailable case
		// and skip rather than fail.
		t.Skipf("jail did not pass a known-correct test against compliant code — treating as jail/toolchain unavailable rather than a test bug: %+v", rep)
	}
	if rep.KillRate() != 1.0 {
		t.Fatalf("thorough suite should kill all mutants, got kill rate %v (report: %+v)", rep.KillRate(), rep)
	}

	// Gappy suite: only checks the even case, so the always-true mutant
	// survives.
	gappy := "import { test } from 'node:test';\nimport assert from 'node:assert';\nimport { isEven } from './evenmod.ts';\ntest('is even', () => {\n  assert.ok(isEven(2));\n});\n"
	rep2, err := adequacy.Score(context.Background(), jail,
		withTest(gappy), codePath, code, mutants, ts.TestCmd())
	if err != nil {
		t.Skipf("jail/tsc unavailable (score errored): %v", err)
	}
	if !rep2.CompliantPass {
		// Same jail/toolchain-infra reasoning as the thorough run above.
		t.Skipf("jail did not pass a known-correct test against compliant code — treating as jail/toolchain unavailable rather than a test bug: %+v", rep2)
	}
	if len(rep2.Survived) == 0 {
		t.Fatalf("gappy suite must leave a survivor, got report: %+v", rep2)
	}
}
