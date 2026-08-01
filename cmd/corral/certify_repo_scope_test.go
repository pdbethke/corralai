// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestScopeTestsUsesThePairedTestFile covers the one change that decides whether
// corral is practical. Scoring runs the target's whole suite once per mutant, so
// an audit costs O(mutants × suite runtime): 1.46s/suite on flask but 77s on
// psf/requests, where the suite is ~96% of a file's audit. Scoping to the file's
// own paired test collapses that multiplier by 30-50×.
//
// Applied at testCmd() deliberately — that one function's result becomes BOTH the
// baseline command and the scoring command, so the two cannot disagree about what
// "the suite" means. A scoped scoring run graded against an unscoped baseline
// would compare different things and silently corrupt every kill rate.
func TestScopeTestsUsesThePairedTestFile(t *testing.T) {
	job := reposcan.Job{Path: "src/pkg/target.py", TestPath: "tests/test_target.py", Lang: "python"}

	t.Run("off by default: the whole suite", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		got := ex.testCmd(job)
		if last := got[len(got)-1]; last == job.TestPath {
			t.Fatalf("testCmd = %v — scoping must be OFF by default; it changes the measurement, not just the speed", got)
		}
	})

	t.Run("on: only the paired test file", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.scopeTests = true
		got := ex.testCmd(job)
		if len(got) == 0 || got[len(got)-1] != job.TestPath {
			t.Fatalf("testCmd = %v, want the paired test path as the final argument", got)
		}
	})

	t.Run("an explicit -- command always wins", func(t *testing.T) {
		// The operator naming a command has already chosen the surface; scoping
		// must never rewrite it out from under them.
		explicit := []string{"make", "test"}
		ex := newLocalExecutor(t.TempDir(), explicit, substrateWorkspace, 0, nil)
		ex.scopeTests = true
		got := ex.testCmd(job)
		if len(got) != 2 || got[0] != "make" {
			t.Fatalf("testCmd = %v, want the operator's explicit command untouched", got)
		}
	})

	t.Run("no paired test: falls back to the full suite", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.scopeTests = true
		got := ex.testCmd(reposcan.Job{Path: "a.py", Lang: "python"})
		if len(got) == 0 {
			t.Fatal("testCmd returned nothing")
		}
		if last := got[len(got)-1]; last == "" {
			t.Fatalf("testCmd = %v — with nothing to scope to it must fall back, never emit a dangling path", got)
		}
	})

	t.Run("a language without verified scoping is unchanged", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.scopeTests = true
		goJob := reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"}
		got := ex.testCmd(goJob)
		if len(got) > 0 && got[len(got)-1] == goJob.TestPath {
			t.Fatalf("testCmd = %v — go does not implement FileScopedTester, so it must keep its stock command", got)
		}
	})
}
