// SPDX-License-Identifier: Elastic-2.0

package cisogate

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/controlspec"
)

// TestRunCisoGate reuses the fakeJail defined in stage_test.go (same
// package) rather than redeclaring one — it already implements
// adequacy.Jail's RunTest exactly as this test needs it.
func TestRunCisoGate(t *testing.T) {
	// fake jail: a test "passes" unless the head code contains "VIOLATION".
	jail := &fakeJail{onRun: func(files map[string]string, cmd []string) bool {
		return !strings.Contains(files["auth.go"], "VIOLATION")
	}}
	base := map[string]string{"go.mod": "module target\ngo 1.26\n"}

	// two vetted tests; the second target's head code violates → overall fail.
	checks := []CisoCheck{
		{Test: controlspec.GateTest{Goal: "g1", Target: "t1", Test: "package target\n// t1"}, HeadCode: "package target\n// clean", CodePath: "auth.go", TestPath: "auth_ciso_test.go"},
		{Test: controlspec.GateTest{Goal: "g2", Target: "t2", Test: "package target\n// t2"}, HeadCode: "package target\n// VIOLATION", CodePath: "auth.go", TestPath: "auth_ciso_test.go"},
	}
	res, err := RunCisoGate(context.Background(), jail, base, checks, []string{"go", "test", "./"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("one vetted test failed → gate must fail (fail-closed)")
	}
	if len(res.Results) != 2 || !res.Results[0].Passed || res.Results[1].Passed {
		t.Fatalf("per-test results wrong: %+v", res.Results)
	}
	// all-pass case
	clean := []CisoCheck{checks[0]}
	if r, _ := RunCisoGate(context.Background(), jail, base, clean, []string{"go", "test", "./"}); !r.Pass {
		t.Fatal("all vetted tests pass → gate passes")
	}
	// empty checks → vacuous pass (caller enforces coverage)
	if r, _ := RunCisoGate(context.Background(), jail, base, nil, []string{"go", "test", "./"}); !r.Pass || len(r.Results) != 0 {
		t.Fatalf("empty checks → vacuous pass, no results: %+v", r)
	}
}
