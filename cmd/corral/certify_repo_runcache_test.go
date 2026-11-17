// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The file's own account of its cache must survive every boundary (AGENTS.md:
// a field-by-field converter fails open on whatever field nobody remembered
// to add): FileResult -> scanstore.File -> buildAuditRows -> entry on disk ->
// ReadLedgerDir -> fileFromRow, and -> LoadDir -> corral_audits.run_cache,
// NULL for a file with nothing to say.
func TestRunCacheSurvivesTheLedgerAndTheWarehouse(t *testing.T) {
	const unshared = "unshared: sandbox backend bwrap cannot overlay a directory on the filesystem of /tmp"
	const neverWarmed = "unshared: never warmed (the dev suite's command could not be given the flag that forbids a cached result)"
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go", Lang: "go"}, Gradable: true, RunCache: "shared"},
		{Job: reposcan.Job{Path: "b.go", Lang: "go"}, Gradable: true, RunCache: unshared},
		{Job: reposcan.Job{Path: "c.py", Lang: "python"}, Gradable: true},
		{Job: reposcan.Job{Path: "d.go", Lang: "go"}, Gradable: true, RunCache: neverWarmed},
	}
	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, false)
	want := map[string]string{"a.go": "shared", "b.go": unshared, "c.py": "", "d.go": neverWarmed}
	for _, f := range files {
		if f.RunCache != want[f.Path] {
			t.Errorf("buildScanFileRows: %s RunCache = %q, want %q", f.Path, f.RunCache, want[f.Path])
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
	got := map[string]string{}
	for _, r := range entries[0].Bundle.Files {
		got[r.Path] = fileFromRow(r).RunCache
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("fileFromRow: %s RunCache = %q, want %q", p, got[p], w)
		}
	}

	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var col string
	if err := db.QueryRow(`SELECT run_cache FROM corral_audits WHERE path = 'b.go'`).Scan(&col); err != nil || col != unshared {
		t.Errorf("warehouse run_cache for b.go = %q, err %v, want %q", col, err, unshared)
	}
	if err := db.QueryRow(`SELECT run_cache FROM corral_audits WHERE path = 'd.go'`).Scan(&col); err != nil || col != neverWarmed {
		t.Errorf("warehouse run_cache for d.go = %q, err %v, want %q", col, err, neverWarmed)
	}
	var nulls int
	if err := db.QueryRow(`SELECT count(*) FROM corral_audits WHERE path = 'c.py' AND run_cache IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("a file with no run cache must write NULL (got %d NULL rows, err %v)", nulls, err)
	}
	read, err := auditpush.ReadBundle(db, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	back := map[string]string{}
	for _, r := range read.Files {
		back[r.Path] = r.RunCache
	}
	for p, w := range want {
		if back[p] != w {
			t.Errorf("read.go's reader: %s RunCache = %q, want %q", p, back[p], w)
		}
	}
}

// A file whose cache the wiring decided is recorded on its result, and the
// scan says so on that file's own line only when it is not shared.
func TestExecuteRecordsTheRunCacheAndSaysWhenItIsNotShared(t *testing.T) {
	for _, tc := range []struct {
		record  string
		wantSay string
	}{
		{"shared", ""},
		{"unshared: sandbox backend bwrap cannot overlay a directory", "  build cache: not shared for src/p.go: sandbox backend bwrap cannot overlay a directory\n"},
		{"", ""},
	} {
		var buf bytes.Buffer
		ex := localExecutor{
			baselineRuns: 2,
			progress:     &buf,
			newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
				*in.runCache = tc.record // what buildJailWiring decides, through the pointer
				return flakyBaseline{}, func() {}, nil
			},
			audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
				*in.runCache = tc.record
				return advpool.Verdict{}, nil
			},
		}
		res, err := ex.Execute(context.Background(), reposcan.Job{Path: "src/p.go", Goal: reposcan.Goal{Text: "g"}})
		if err != nil {
			t.Fatal(err)
		}
		if res.RunCache != tc.record {
			t.Errorf("record %q: the result carries RunCache %q", tc.record, res.RunCache)
		}
		if tc.wantSay == "" && strings.Contains(buf.String(), "build cache") {
			t.Errorf("record %q: said something about the cache:\n%s", tc.record, buf.String())
		}
		if tc.wantSay != "" && !strings.Contains(buf.String(), tc.wantSay) {
			t.Errorf("record %q: want %q in:\n%s", tc.record, tc.wantSay, buf.String())
		}
	}
}

// Said once per scan, per language whose runner has a run cache, and only on
// the jail substrate: a language with none, and the workspace substrate, say
// nothing.
func TestTheScanSaysOnceWhichLanguagesShareARunCache(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	jobs := []reposcan.Job{{Path: "a.go", Lang: "go"}, {Path: "b.go", Lang: "go"}, {Path: "c.py", Lang: "python"}}
	var buf bytes.Buffer
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), iso: iso}, jobs)
	const line = "  build cache: grading runs for go share a frozen GOCACHE, warmed by the unmutated run\n"
	if strings.Count(buf.String(), line) != 1 || strings.Count(buf.String(), "build cache") != 1 {
		t.Errorf("want exactly the go line once, got:\n%s", buf.String())
	}
	buf.Reset()
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), substrate: substrateWorkspace, iso: iso}, jobs)
	if strings.Contains(buf.String(), "build cache") {
		t.Errorf("the workspace substrate has no jail and no run cache, got:\n%s", buf.String())
	}
}

// A backend that cannot overlay (container, sandbox-exec, an older bwrap)
// shares no cache, so the header must not say every go file shares one.
func TestTheScanHeaderSaysNothingOnABackendThatCannotOverlay(t *testing.T) {
	jobs := []reposcan.Job{{Path: "a.go", Lang: "go"}}
	var buf bytes.Buffer
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), checkArgv: []string{"go", "test", "./..."}, iso: &wrapRecordingIsolator{}}, jobs)
	if strings.Contains(buf.String(), "build cache") {
		t.Errorf("a backend that cannot overlay: the header claims a shared cache:\n%s", buf.String())
	}
}

var _ = scanstore.File{}

// A file that was not graded records nothing: a baseline that failed
// (COULD-NOT-GRADE) says "" on both the --repo and the --local paths, through
// one helper, however the wiring had decided before the baseline ran.
func TestAFileThatWasNotGradedRecordsNoRunCache(t *testing.T) {
	failed := advpool.Verdict{BaselineFailed: true}
	if got := fileRunCache(reposcan.FileResult{Verdict: failed, Gradable: false, RunCache: "shared"}); got != "" {
		t.Errorf("fileRunCache for an ungraded file = %q, want empty", got)
	}
	if got := fileRunCache(reposcan.FileResult{Gradable: true, RunCache: "shared"}); got != "shared" {
		t.Errorf("fileRunCache for a graded file = %q, want shared", got)
	}

	// --repo: Execute on a file whose audit baseline failed.
	ex := localExecutor{
		baselineRuns: 2,
		newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
			*in.runCache = "shared"
			return flakyBaseline{}, func() {}, nil
		},
		audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
			*in.runCache = "shared"
			return failed, nil
		},
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "src/p.go", Lang: "go", Goal: reposcan.Goal{Text: "g"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := buildScanFileRows([]reposcan.FileResult{res}, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, false)
	for _, r := range rows {
		if r.RunCache != "" {
			t.Errorf("--repo row for a file whose baseline failed records RunCache %q", r.RunCache)
		}
	}

	// --local: localFileResult.
	rc := "shared"
	if got := localFileResult(localAuditInput{runCache: &rc, codePath: "p.go"}, failed, advpool.RunTelemetry{}, time.Now()); got.RunCache != "" {
		t.Errorf("--local result for a file whose baseline failed records RunCache %q", got.RunCache)
	}
}

// The scan header says grading runs share a cache only when the scan's base
// command can be rewritten to forbid a cached result; otherwise it says
// nothing and the per-file lines speak.
func TestTheScanHeaderSaysNothingForACommandTheRewriteCannotRead(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	jobs := []reposcan.Job{{Path: "a.go", Lang: "go"}}
	for _, argv := range [][]string{{"go", "test", "-no-such-flag", "./..."}, {"sh", "-c", "go test ./..."}, {"go", "vet", "./..."}} {
		var buf bytes.Buffer
		printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), checkArgv: argv, iso: iso}, jobs)
		if strings.Contains(buf.String(), "build cache") {
			t.Errorf("base command %v: the header claims a shared cache:\n%s", argv, buf.String())
		}
	}
	var buf bytes.Buffer
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), checkArgv: []string{"go", "test", "./..."}, iso: iso}, jobs)
	if !strings.Contains(buf.String(), "build cache: grading runs for go share") {
		t.Errorf("a readable go test command should say so:\n%s", buf.String())
	}
}
