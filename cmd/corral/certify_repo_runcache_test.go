// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

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
	const unshared = "unshared: this bwrap cannot overlay a directory on the filesystem of /tmp"
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go", Lang: "go"}, Gradable: true, RunCache: "shared"},
		{Job: reposcan.Job{Path: "b.go", Lang: "go"}, Gradable: true, RunCache: unshared},
		{Job: reposcan.Job{Path: "c.py", Lang: "python"}, Gradable: true},
	}
	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, false)
	want := map[string]string{"a.go": "shared", "b.go": unshared, "c.py": ""}
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
		{"unshared: this bwrap cannot overlay a directory", "  build cache: not shared for src/p.go: this bwrap cannot overlay a directory\n"},
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
	jobs := []reposcan.Job{{Path: "a.go", Lang: "go"}, {Path: "b.go", Lang: "go"}, {Path: "c.py", Lang: "python"}}
	var buf bytes.Buffer
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir()}, jobs)
	const line = "  build cache: grading runs for go share a frozen GOCACHE, warmed by the unmutated run\n"
	if strings.Count(buf.String(), line) != 1 || strings.Count(buf.String(), "build cache") != 1 {
		t.Errorf("want exactly the go line once, got:\n%s", buf.String())
	}
	buf.Reset()
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), substrate: substrateWorkspace}, jobs)
	if strings.Contains(buf.String(), "build cache") {
		t.Errorf("the workspace substrate has no jail and no run cache, got:\n%s", buf.String())
	}
}

var _ = scanstore.File{}
