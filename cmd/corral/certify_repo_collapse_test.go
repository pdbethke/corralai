// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// collapseFixture is the exclusion list the brief names: 50 skipped-dir files
// under dist, 3 gitignored under scratch, one gitignored at the root, 6 python
// and 4 typescript test files, one test with no language, 7 no-paired-test.
func collapseFixture() (ex []reposcan.Exclusion, noPaired []reposcan.Exclusion) {
	for i := 0; i < 50; i++ {
		ex = append(ex, reposcan.Exclusion{Path: fmt.Sprintf("dist/f%02d.js", i), Reason: reposcan.ReasonSkippedDir, Dir: "dist"})
	}
	for i := 0; i < 3; i++ {
		ex = append(ex, reposcan.Exclusion{Path: fmt.Sprintf("scratch/s%d.py", i), Reason: reposcan.ReasonGitignored, Dir: "scratch"})
	}
	ex = append(ex, reposcan.Exclusion{Path: "notes.tmp", Reason: reposcan.ReasonGitignored, Dir: ".", Loose: true})
	for i := 0; i < 6; i++ {
		ex = append(ex, reposcan.Exclusion{Path: fmt.Sprintf("tests/test_%d.py", i), Reason: reposcan.ReasonIsTest})
	}
	for i := 0; i < 4; i++ {
		ex = append(ex, reposcan.Exclusion{Path: fmt.Sprintf("web/t%d.test.ts", i), Reason: reposcan.ReasonIsTest})
	}
	ex = append(ex, reposcan.Exclusion{Path: "weird.tst", Reason: reposcan.ReasonIsTest})
	for i := 0; i < 7; i++ {
		e := reposcan.Exclusion{Path: fmt.Sprintf("pkg/p%d.go", i), Reason: reposcan.ReasonNoPairedTest}
		ex = append(ex, e)
		noPaired = append(noPaired, e)
	}
	return ex, noPaired
}

func TestCollapsedExclusionRows(t *testing.T) {
	ex, noPaired := collapseFixture()
	rows, rest := collapsedExclusionRows(ex)

	type k struct {
		reason, path, lang string
		files              int
	}
	want := []k{
		{reposcan.ReasonSkippedDir, "dist/", "", 50},
		{reposcan.ReasonGitignored, "./*", "", 1},
		{reposcan.ReasonGitignored, "scratch/", "", 3},
		{reposcan.ReasonIsTest, "", "", 1},
		{reposcan.ReasonIsTest, "", "python", 6},
		{reposcan.ReasonIsTest, "", "typescript", 4},
	}
	var got []k
	for _, r := range rows {
		got = append(got, k{r.Reason, r.Path, r.Lang, r.Files})
		if r.ParentSHA256 != "" || r.PreflightState != "" || r.Gradable || r.Disposition != "rejected" {
			t.Errorf("collapsed row carries more than a count: %+v", r)
		}
		if r.Evidence != exclusionEvidence(r.Reason, "") {
			t.Errorf("evidence %q, want %q", r.Evidence, exclusionEvidence(r.Reason, ""))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v\nwant   %+v", got, want)
	}
	if !reflect.DeepEqual(rest, noPaired) {
		t.Errorf("rest = %v, want the 7 no-paired-test exclusions in order", rest)
	}
	inDirs, tests := collapsedCounts(rows)
	if inDirs != 54 || tests != 11 {
		t.Errorf("collapsedCounts = %d, %d; want 54, 11", inDirs, tests)
	}
}

func TestBuildScanFileRowsCollapsesUnlessEveryFileIsAsked(t *testing.T) {
	ex, _ := collapseFixture()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "f00.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	collapsed := buildScanFileRows(nil, ex, reposcan.CoverageMap{}, "", root, io.Discard, false)
	sum := 0
	for _, r := range collapsed {
		if r.Disposition == "rejected" {
			sum += r.FileCount()
		}
	}
	if sum != 72 {
		t.Errorf("collapsed rows stand for %d files, want 72", sum)
	}
	if len(collapsed) != 6+7 {
		t.Errorf("collapsed record has %d rows, want 13", len(collapsed))
	}

	every := buildScanFileRows(nil, ex, reposcan.CoverageMap{}, "", root, io.Discard, true)
	if len(every) != 72 {
		t.Fatalf("everyFile rows = %d, want 72", len(every))
	}
	hashed := 0
	for _, r := range every {
		if r.Files != 0 {
			t.Errorf("everyFile row %s has Files %d", r.Path, r.Files)
		}
		if r.Path == "dist/f00.js" && r.ParentSHA256 != "" {
			hashed++
		}
	}
	if hashed != 1 {
		t.Errorf("an existing file lost its hash in the every-file record")
	}
}

func TestNothingToCollapseLeavesTheRowsUnchanged(t *testing.T) {
	_, noPaired := collapseFixture()
	a := buildScanFileRows(nil, noPaired, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, false)
	b := buildScanFileRows(nil, noPaired, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, true)
	if len(a) != 7 || !reflect.DeepEqual(a, b) {
		t.Errorf("false and true differ with nothing to collapse:\n%+v\n%+v", a, b)
	}
}

// A path that is both a job row and under a collapsed directory is one file:
// the job row keeps it and the directory row must not count it again.
func TestACollapsedDirectoryDoesNotCountAJobRowTwice(t *testing.T) {
	ex := []reposcan.Exclusion{
		{Path: "dist/a.js", Reason: reposcan.ReasonSkippedDir, Dir: "dist"},
		{Path: "dist/b.js", Reason: reposcan.ReasonSkippedDir, Dir: "dist"},
		{Path: "dist/b.js", Reason: reposcan.ReasonSkippedDir, Dir: "dist"},
	}
	res := []reposcan.FileResult{{Job: reposcan.Job{Path: "dist/a.js", Lang: "javascript"}, Reason: reposcan.ReasonExecutorError}}
	rows := buildScanFileRows(res, ex, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, false)
	total := 0
	for _, r := range rows {
		total += r.FileCount()
	}
	if total != 2 {
		t.Errorf("rows stand for %d files, want 2: %+v", total, rows)
	}
}

func TestANestedSkippedDirectoryIsCountedOnceUnderTheOutermost(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"dist/a.js", "dist/build/b.js", "dist/build/c.js"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	rows := buildScanFileRows(nil, excl, reposcan.CoverageMap{}, "", root, io.Discard, false)
	if len(rows) != 1 || rows[0].Path != "dist/" || rows[0].Files != 3 {
		t.Errorf("rows = %+v, want one dist/ row counting 3", rows)
	}
}

func TestACollapsedTestFileRowIsAValidRecordRow(t *testing.T) {
	files := []scanstore.File{
		{Path: "", Lang: "python", Disposition: "rejected", Reason: reposcan.ReasonIsTest, Files: 6, Evidence: exclusionEvidence(reposcan.ReasonIsTest, "")},
		{Path: "", Lang: "typescript", Disposition: "rejected", Reason: reposcan.ReasonIsTest, Files: 4, Evidence: exclusionEvidence(reposcan.ReasonIsTest, "")},
		{Path: "dist/", Disposition: "rejected", Reason: reposcan.ReasonSkippedDir, Files: 50},
	}
	rows := buildAuditRows(files, 1, bundleMeta{Repo: "o/r", Commit: "deadbeef"})
	dir := t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "o/r", Commit: "deadbeef", ScanID: 1, CorralVersion: "vtest"}, Files: rows}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatalf("writing an entry with empty-path rows: %v", err)
	}
	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir refused: %v", err)
	}
	defer db.Close()
	read, err := auditpush.ReadBundle(db, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range read.Files {
		got[r.Lang+"|"+r.Path] += r.Files
	}
	if got["python|"] != 6 || got["typescript|"] != 4 || got["|dist/"] != 50 || len(read.Files) != 3 {
		t.Errorf("rows read back as %v (%d rows)", got, len(read.Files))
	}
}

// A loose ignored file is counted under "<parent>/*", never under the
// directory row "<parent>/": the first says nothing about its neighbours.
func TestLooseIgnoredFilesAreCountedUnderTheirParentStar(t *testing.T) {
	ex := []reposcan.Exclusion{
		{Path: "src/.env", Reason: reposcan.ReasonGitignored, Dir: "src", Loose: true},
		{Path: "src/.key", Reason: reposcan.ReasonGitignored, Dir: "src", Loose: true},
		{Path: ".env", Reason: reposcan.ReasonGitignored, Dir: ".", Loose: true},
		{Path: "scratch/x.py", Reason: reposcan.ReasonGitignored, Dir: "scratch"},
		{Path: "src/gen/y.py", Reason: reposcan.ReasonGitignored, Dir: "src/gen"},
	}
	rows, _ := collapsedExclusionRows(ex)
	got := map[string]int{}
	for _, r := range rows {
		got[r.Path] = r.Files
	}
	want := map[string]int{"scratch/": 1, "src/*": 2, "./*": 1, "src/gen/": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}
