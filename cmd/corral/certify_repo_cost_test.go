// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestCostLineGolden pins the exact line format the brief specifies: the
// scan total first, then a per-role breakdown in roster order, tokens
// abbreviated (1.2M, 48k; plain below 1000), and a role that made no calls
// simply absent from the list — never a zero entry.
func TestCostLineGolden(t *testing.T) {
	calls := []advpool.ModelCall{
		{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 24, InputTokens: 900_000, OutputTokens: 31_000},
		{Role: advpool.RoleTestWriter, Model: "w-1", Calls: 5, InputTokens: 300_000, OutputTokens: 17_000},
	}
	got := costLine(calls)
	want := "  cost: 1.2M tokens in / 48k out across 29 calls — mutant-generator 0.9M/31k (24 calls), test-writer 0.3M/17k (5 calls)"
	if got != want {
		t.Errorf("costLine =\n%q\nwant\n%q", got, want)
	}
}

// TestCostLineEmptyWhenNothingCalled: a scan that reused every verdict from
// the cache made no model calls at all, and costLine must say nothing rather
// than print a cost line for zero calls.
func TestCostLineEmptyWhenNothingCalled(t *testing.T) {
	if got := costLine(nil); got != "" {
		t.Errorf("costLine(nil) = %q, want empty", got)
	}
	if got := costLine([]advpool.ModelCall{{Role: advpool.RoleTestCritic, Calls: 0}}); got != "" {
		t.Errorf("costLine with a zero-call role = %q, want empty", got)
	}
}

// TestCostLineOmitsAZeroCallRoleFromTheBreakdown: a mixed slice — some roles
// with calls, one without — must drop the zero-call role from the breakdown
// AND from the total, never render it as "role 0/0 (0 calls)".
func TestCostLineOmitsAZeroCallRoleFromTheBreakdown(t *testing.T) {
	calls := []advpool.ModelCall{
		{Role: advpool.RoleMutantGenerator, Calls: 3, InputTokens: 100, OutputTokens: 50},
		{Role: advpool.RoleTestCritic, Calls: 0},
	}
	got := costLine(calls)
	want := "  cost: 100 tokens in / 50 out across 3 calls — mutant-generator 100/50 (3 calls)"
	if got != want {
		t.Errorf("costLine =\n%q\nwant\n%q", got, want)
	}
}

// TestBuildScanModelCallRows proves the mapping from a scan's per-file
// verdicts into the ledger's per-(file,role) grain: one row per role that
// made a call, on the file that made it, and nothing at all for an
// ungradable result or a role with no calls.
func TestBuildScanModelCallRows(t *testing.T) {
	results := []reposcan.FileResult{
		{
			Job:      reposcan.Job{Path: "a.go", Lang: "go"},
			Gradable: true,
			Verdict: advpool.Verdict{
				ModelCalls: []advpool.ModelCall{
					{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 3, InputTokens: 900, OutputTokens: 210, Wall: 4100 * time.Millisecond},
					{Role: advpool.RoleTestWriter, Model: "w-1", Calls: 4, InputTokens: 300, OutputTokens: 130, Wall: 2200 * time.Millisecond},
				},
			},
		},
		{
			Job:      reposcan.Job{Path: "b.go", Lang: "go"},
			Gradable: false,
			Reason:   reposcan.ReasonExecutorError,
		},
	}

	rows := buildScanModelCallRows(results)
	if len(rows) != 2 {
		t.Fatalf("buildScanModelCallRows returned %d rows, want 2", len(rows))
	}
	byRole := make(map[string]scanstore.ModelCall, len(rows))
	for _, r := range rows {
		if r.Path != "a.go" {
			t.Errorf("row %+v: ungradable b.go contributed a row", r)
		}
		byRole[r.Role] = r
	}
	mg, ok := byRole[advpool.RoleMutantGenerator]
	if !ok {
		t.Fatalf("no row for %s", advpool.RoleMutantGenerator)
	}
	if mg.Model != "m-1" || mg.Calls != 3 || mg.InputTokens != 900 || mg.OutputTokens != 210 || mg.WallMillis != 4100 {
		t.Errorf("mutant-generator row = %+v, want model m-1, calls 3, 900/210 tokens, 4100ms", mg)
	}
}

// TestModelCallsReachTheLedger is the round trip the brief names explicitly:
// record a scan whose results carry ModelCalls, and ModelCallsForScan must
// return exactly those rows back.
func TestModelCallsReachTheLedger(t *testing.T) {
	st, err := scanstore.Open(filepath.Join(t.TempDir(), "scans.duckdb"))
	if err != nil {
		t.Fatalf("scanstore.Open: %v", err)
	}
	defer st.Close()

	results := []reposcan.FileResult{
		{
			Job:      reposcan.Job{Path: "pkg/a.go", Lang: "go"},
			Gradable: true,
			Verdict: advpool.Verdict{
				ModelCalls: []advpool.ModelCall{
					{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 3, InputTokens: 900, OutputTokens: 210, Wall: 4100 * time.Millisecond},
				},
			},
		},
	}
	calls := buildScanModelCallRows(results)

	scan := scanstore.Scan{Owner: "o", Repo: "r", Commit: "deadbeef"}
	files := []scanstore.File{{Path: "pkg/a.go", Lang: "go", Disposition: "audited"}}

	id, err := recordCertifyRepoScan(st, scan, files, nil, calls, nil, nil)
	if err != nil {
		t.Fatalf("recordCertifyRepoScan: %v", err)
	}

	got, err := st.ModelCallsForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("ModelCallsForScan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ModelCallsForScan returned %d rows, want 1", len(got))
	}
	want := scanstore.ModelCall{
		ScanID: id, Path: "pkg/a.go", Role: advpool.RoleMutantGenerator, Model: "m-1",
		Calls: 3, InputTokens: 900, OutputTokens: 210, WallMillis: 4100,
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("ledger round trip = %+v, want %+v", got[0], want)
	}
}

// TestVerdictModelCallsRoundTripsThroughTheCache pins the cache round trip:
// a verdict served back from the ledger's verdict_cache must still carry
// ModelCalls, the same way it already carries Timing.
func TestVerdictModelCallsRoundTripsThroughTheCache(t *testing.T) {
	v := advpool.Verdict{
		DevKillRate: 0.5,
		ModelCalls: []advpool.ModelCall{
			{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: 3, InputTokens: 900, OutputTokens: 210, Wall: 4100 * time.Millisecond},
		},
	}
	js, err := marshalVerdict(v)
	if err != nil {
		t.Fatalf("marshalVerdict: %v", err)
	}
	var got advpool.Verdict
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.ModelCalls, v.ModelCalls) {
		t.Errorf("ModelCalls did not round-trip.\n got: %+v\nwant: %+v", got.ModelCalls, v.ModelCalls)
	}
}
