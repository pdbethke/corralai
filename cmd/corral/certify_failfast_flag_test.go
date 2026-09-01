// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// A flag that parses and is then dropped on the floor is this codebase's
// recurring defect. `--no-fail-fast` has to reach the per-file audit input,
// per job.
func TestNoFailFastReachesEveryJobsAuditInput(t *testing.T) {
	for _, off := range []bool{false, true} {
		ex := &localExecutor{noFailFast: off}
		got := ex.auditInputFor(reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"})
		if got.noFailFast != off {
			t.Errorf("noFailFast=%v was dropped between the flag and the audit input", off)
		}
	}
}

// ...and the default is ON: the fail-fast seam must actually be resolvable for
// a language whose plugin implements it, or the whole change is inert.
func TestDefaultFailFastResolvesForASupportedRunner(t *testing.T) {
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	var f adequacy.FailFastFor = func(cmd []string) ([]string, bool) { return lang.FailFastArgsFor(p, cmd) }
	args, ok := f([]string{"python3", "-m", "pytest", "tests/"})
	if !ok || len(args) != 1 || args[0] != "-x" {
		t.Fatalf("resolved fail-fast = %v ok=%v, want [-x]", args, ok)
	}
}
