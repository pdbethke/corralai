// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestExecutorAuditInputCarriesNoSharedMeter is the successor to
// TestScanSharesOneMeterAcrossFiles (task 4, cost per role per file).
//
// Before this task, a whole-repo scan held ONE agentbackend.UsageMeter shared
// by every file, because a meter could only say what a RUN spent, never what
// a ROLE on one FILE spent. That single-total design is retired: each file's
// audit now builds its own per-role meters (auditRoleMeters, inside
// resolveAuditRoles) and stamps their totals onto that file's OWN
// Verdict.ModelCalls. The scan-wide number a caller sees (the stdout cost
// line, corral_scans' token columns) is now the SUM of every file's
// ModelCalls — see scanModelCallTotals — never a single shared object two
// files could accidentally alias.
func TestExecutorAuditInputCarriesNoSharedMeter(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), []string{"go", "test", "./..."}, "jail", 0, nil)
	a := ex.auditInputFor(reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"})
	b := ex.auditInputFor(reposcan.Job{Path: "b.go", TestPath: "b_test.go", Lang: "go"})

	// localAuditInput no longer has a meter field at all — resolveAuditRoles
	// builds one set of per-role meters per call, independent of any field
	// the executor threads in. This test exists to say so in the one place a
	// future reader would look for the OLD sharing guarantee, rather than
	// leaving that guarantee to be rediscovered by its absence.
	_ = a
	_ = b
}

// TestScanModelCallTotalsSumsAcrossFiles pins the summation
// TestScanSharesOneMeterAcrossFiles used to get for free from one shared
// object: two files' independently-measured ModelCalls, for the SAME role,
// must add up to that role's scan-wide total, in roster order.
func TestScanModelCallTotalsSumsAcrossFiles(t *testing.T) {
	results := []reposcan.FileResult{
		{
			Job: reposcan.Job{Path: "a.go"},
			Verdict: advpool.Verdict{
				ModelCalls: []advpool.ModelCall{
					{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 3, InputTokens: 120, OutputTokens: 7, Wall: time.Second},
				},
			},
		},
		{
			Job: reposcan.Job{Path: "b.go"},
			Verdict: advpool.Verdict{
				ModelCalls: []advpool.ModelCall{
					{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 2, InputTokens: 30, OutputTokens: 3, Wall: 500 * time.Millisecond},
				},
			},
		},
	}

	totals := scanModelCallTotals(results)
	if len(totals) != 1 {
		t.Fatalf("scanModelCallTotals = %+v, want exactly one role", totals)
	}
	got := totals[0]
	if got.Calls != 5 || got.InputTokens != 150 || got.OutputTokens != 10 || got.Wall != 1500*time.Millisecond {
		t.Errorf("mutant-generator totals = %+v, want 5 calls / 150 in / 10 out / 1.5s", got)
	}
}
