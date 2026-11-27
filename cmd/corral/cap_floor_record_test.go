// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// The four things a file row can say about where its per-mutant cap's floor
// came from: a warmed run cache (Go in the jail), a measured rebuild run
// (Swift on the workspace substrate), a rebuild run that gave no floor, and
// nothing (no floor source applies).
const (
	floorWarm    = "run-cache warming run, 12.345s"
	floorRebuild = "rebuild run, 17.004s"
	floorNone    = "none: adequacy: the suite did not pass on the rebuild run of Sources/C/C.swift"
)

// The file's account of its cap floor survives every boundary, the same walk
// RunCache takes (AGENTS.md: a field-by-field converter fails open on the
// field nobody remembered): FileResult -> scanstore.File -> buildAuditRows ->
// entry on disk -> ReadLedgerDir -> fileFromRow, and -> LoadDir ->
// corral_audits.cap_floor -> ReadBundle, NULL for a file with nothing to say.
func TestCapFloorSurvivesTheLedgerAndTheWarehouse(t *testing.T) {
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go", Lang: "go"}, Gradable: true, RunCache: "shared", CapFloor: floorWarm},
		{Job: reposcan.Job{Path: "Sources/B/B.swift", Lang: "swift"}, Gradable: true, CapFloor: floorRebuild},
		{Job: reposcan.Job{Path: "Sources/C/C.swift", Lang: "swift"}, Gradable: true, CapFloor: floorNone},
		{Job: reposcan.Job{Path: "d.py", Lang: "python"}, Gradable: true},
	}
	want := map[string]string{"a.go": floorWarm, "Sources/B/B.swift": floorRebuild, "Sources/C/C.swift": floorNone, "d.py": ""}

	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, false)
	for _, f := range files {
		if f.CapFloor != want[f.Path] {
			t.Errorf("buildScanFileRows: %s CapFloor = %q, want %q", f.Path, f.CapFloor, want[f.Path])
		}
		if f.Path == "a.go" && f.RunCache != "shared" {
			t.Errorf("a.go RunCache = %q: the floor must not change what RunCache records", f.RunCache)
		}
	}

	rows := buildAuditRows(files, 1, bundleMeta{Repo: "o/r", Commit: "deadbeef"})
	dir := t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "o/r", Commit: "deadbeef", ScanID: 1, CorralVersion: "vtest"}, Files: rows}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLedgerDir: %d entries, err %v", len(entries), err)
	}
	for _, r := range entries[0].Bundle.Files {
		if got := fileFromRow(r).CapFloor; got != want[r.Path] {
			t.Errorf("fileFromRow: %s CapFloor = %q, want %q", r.Path, got, want[r.Path])
		}
	}

	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var col string
	if err := db.QueryRow(`SELECT cap_floor FROM corral_audits WHERE path = 'Sources/B/B.swift'`).Scan(&col); err != nil || col != floorRebuild {
		t.Errorf("warehouse cap_floor = %q, err %v, want %q", col, err, floorRebuild)
	}
	var nulls int
	if err := db.QueryRow(`SELECT count(*) FROM corral_audits WHERE path = 'd.py' AND cap_floor IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("a file with no floor source must write NULL (got %d NULL rows, err %v)", nulls, err)
	}
	read, err := auditpush.ReadBundle(db, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range read.Files {
		if r.CapFloor != want[r.Path] {
			t.Errorf("read.go's reader: %s CapFloor = %q, want %q", r.Path, r.CapFloor, want[r.Path])
		}
	}
}

// Execute copies what the wiring recorded onto the file's result, on the
// --repo path; localFileResult does on the --local path. A file that was not
// graded records nothing, by the same rule RunCache follows.
func TestTheCapFloorReachesTheFileResult(t *testing.T) {
	for _, tc := range []struct {
		verdict advpool.Verdict
		want    string
	}{{advpool.Verdict{}, floorRebuild}, {advpool.Verdict{BaselineFailed: true}, ""}} {
		ex := localExecutor{
			baselineRuns: 2,
			newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
				*in.capFloor = floorRebuild // what buildJailWiring records, through the pointer
				return flakyBaseline{}, func() {}, nil
			},
			audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
				return tc.verdict, nil
			},
		}
		res, err := ex.Execute(context.Background(), reposcan.Job{Path: "Sources/B/B.swift", Lang: "swift", Goal: reposcan.Goal{Text: "g"}})
		if err != nil {
			t.Fatal(err)
		}
		if res.CapFloor != tc.want {
			t.Errorf("--repo, baseline failed %v: CapFloor = %q, want %q", tc.verdict.BaselineFailed, res.CapFloor, tc.want)
		}
		floor := floorRebuild
		local := localFileResult(localAuditInput{codePath: "B.swift", capFloor: &floor}, tc.verdict, advpool.RunTelemetry{}, time.Now())
		if local.CapFloor != tc.want {
			t.Errorf("--local, baseline failed %v: CapFloor = %q, want %q", tc.verdict.BaselineFailed, local.CapFloor, tc.want)
		}
	}
}

// warmedJail is a RunCacheJail whose cache was warmed by a run of warm.
type warmedJail struct{ warm time.Duration }

func (warmedJail) RunTest(context.Context, map[string]string, []string) (bool, error) {
	return true, nil
}
func (warmedJail) WarmRun(context.Context, map[string]string, []string) (bool, []byte, error) {
	return true, nil, nil
}
func (warmedJail) RunCache() sandbox.SharedCache { return sandbox.SharedCache{Target: "/cache"} }
func (j warmedJail) Warmed() bool                { return j.warm > 0 }
func (j warmedJail) WarmDuration() time.Duration { return j.warm }
func (warmedJail) Close() error                  { return nil }

var _ adequacy.RunCacheJail = warmedJail{}

// capFloorOf reads the floor from the jail the file was graded on, the one
// source Score reads it from, and names which kind of run set it.
func TestCapFloorOfNamesTheRunThatSetTheFloor(t *testing.T) {
	if got := capFloorOf(warmedJail{warm: 12345 * time.Millisecond}); got != floorWarm {
		t.Errorf("warmed run cache: %q, want %q", got, floorWarm)
	}
	if got := capFloorOf(warmedJail{}); got != "" {
		t.Errorf("a run cache never warmed sets no floor; got %q", got)
	}
	pool, _, codeKey, cmd := rebuildFixture(t, "OK\n")
	if got := capFloorOf(pool); got != "" {
		t.Errorf("a pool with no rebuild run: %q, want empty", got)
	}
	if _, err := pool.MeasureRebuild(context.Background(), nil, codeKey, "OK\n", "// probe\nOK\n", cmd); err != nil {
		t.Fatal(err)
	}
	if got := capFloorOf(pool); !strings.HasPrefix(got, "rebuild run, ") {
		t.Errorf("a pool with a rebuild run: %q, want it named", got)
	}
}

// THE RECORD FOLLOWS SCORE'S DECISION. With --test-timeout the operator's
// value is every mutant's cap and no floor is applied
// (adequacy.CapFloorApplies), even though the dev pass still warms a shared
// run cache. The row must then record no floor: naming the warming run would
// put a floor that did not apply into a measured field. Driven through the
// jail branch's real cleanup, where the record is written.
func TestTheJailRecordsNoFloorWhenTheOperatorSetTheCap(t *testing.T) {
	warm := warmedJail{warm: 12345 * time.Millisecond}
	for _, tc := range []struct {
		testTimeout time.Duration
		want        string
	}{{0, floorWarm}, {60 * time.Second, ""}} {
		w := &jailWiring{cleanup: func() {}}
		runCache, floor := new(string), new(string)
		w.closeWithCleanup(warm, runCache, floor, gradingRunCache{Record: runCacheShared}, tc.testTimeout)
		w.cleanup()
		if *floor != tc.want {
			t.Errorf("test-timeout %v: CapFloor = %q, want %q", tc.testTimeout, *floor, tc.want)
		}
		if *runCache != runCacheShared {
			t.Errorf("test-timeout %v: RunCache = %q, want %q unchanged", tc.testTimeout, *runCache, runCacheShared)
		}
	}
}

// A run cache that was shared and never warmed was an expected floor source
// that gave none: "none: <why>", with the why RunCache already carries, the
// rule a rebuild run that gave no floor follows. A runner with no run cache
// at all records nothing.
func TestAnUnwarmedSharedCacheRecordsNoneWithRunCachesReason(t *testing.T) {
	w := &jailWiring{cleanup: func() {}}
	runCache, floor := new(string), new(string)
	w.closeWithCleanup(warmedJail{}, runCache, floor, gradingRunCache{Record: runCacheShared}, 0)
	w.cleanup()
	why, ok := strings.CutPrefix(*runCache, "unshared: ")
	if !ok || !strings.HasPrefix(why, "never warmed (") {
		t.Fatalf("RunCache = %q, want the never-warmed record", *runCache)
	}
	if *floor != "none: "+why {
		t.Errorf("CapFloor = %q, want %q", *floor, "none: "+why)
	}

	w = &jailWiring{cleanup: func() {}}
	runCache, floor = new(string), new(string)
	w.closeWithCleanup(warmedJail{}, runCache, floor, gradingRunCache{}, 0)
	w.cleanup()
	if *floor != "" {
		t.Errorf("no run cache: CapFloor = %q, want empty", *floor)
	}
}

// THE SCAN SAYS IT. On certify --repo a file's own output is discarded
// (auditInputFor), so a floor source that was due and gave none is said
// through the executor's notes, naming the file and the reason, whatever the
// source: a rebuild run that gave none, a shared run cache never warmed. A
// measured floor, no floor source, and a file that was not graded (which
// records nothing) say nothing.
func TestTheScanSaysWhenAFileGotNoCapFloor(t *testing.T) {
	const neverWarmed = "none: never warmed (no dev pass ran)"
	for _, tc := range []struct {
		path    string
		record  string
		verdict advpool.Verdict
		wantSay string
	}{
		{"Sources/C/C.swift", floorNone, advpool.Verdict{}, "  time cap floor: none for Sources/C/C.swift: adequacy: the suite did not pass on the rebuild run of Sources/C/C.swift; each mutant gets the cap its baseline alone gives\n"},
		{"src/p.go", neverWarmed, advpool.Verdict{}, "  time cap floor: none for src/p.go: never warmed (no dev pass ran); each mutant gets the cap its baseline alone gives\n"},
		{"Sources/B/B.swift", floorRebuild, advpool.Verdict{}, ""},
		{"src/w.go", floorWarm, advpool.Verdict{}, ""},
		{"d.py", "", advpool.Verdict{}, ""},
		{"Sources/C/C.swift", floorNone, advpool.Verdict{BaselineFailed: true}, ""},
	} {
		var buf bytes.Buffer
		ex := localExecutor{
			baselineRuns: 2,
			progress:     &buf,
			newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
				return flakyBaseline{}, func() {}, nil
			},
			audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
				*in.capFloor = tc.record // what the audit's wiring records, through the pointer
				return tc.verdict, nil
			},
		}
		if _, err := ex.Execute(context.Background(), reposcan.Job{Path: tc.path, Goal: reposcan.Goal{Text: "g"}}); err != nil {
			t.Fatal(err)
		}
		got := buf.String()
		if tc.wantSay == "" && strings.Contains(got, "cap floor") {
			t.Errorf("%s, record %q, baseline failed %v: said something about the floor:\n%s", tc.path, tc.record, tc.verdict.BaselineFailed, got)
		}
		if tc.wantSay != "" && strings.Count(got, tc.wantSay) != 1 {
			t.Errorf("%s, record %q: want %q once in:\n%s", tc.path, tc.record, tc.wantSay, got)
		}
	}
}
