// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// recordWalk records collapseFixture's walk (56+... files) into a fresh ledger
// directory, once collapsed and once with every file, as two entries, and
// returns the entries read back.
func recordWalk(t *testing.T) (collapsed, every auditpush.LedgerEntry, dir string) {
	t.Helper()
	ex, _ := collapseFixture()
	dir = t.TempDir()
	for i, everyFile := range []bool{false, true} {
		rows := buildAuditRows(buildScanFileRows(nil, ex, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard, everyFile),
			int64(i+1), bundleMeta{Repo: "o/r", Commit: "deadbeef"})
		b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "o/r", Commit: "deadbeef", ScanID: int64(i + 1), CorralVersion: "vtest"}, Files: rows}
		if _, err := auditpush.PushBundle(dir, b); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadLedgerDir: %d entries, err %v", len(entries), err)
	}
	return entries[0], entries[1], dir
}

// notAuditedByReason is the reader under test: files per reason through
// fileFromRow, the way `corral scans show` receives them.
func notAuditedByReason(e auditpush.LedgerEntry) map[string]int {
	out := map[string]int{}
	for _, r := range e.Bundle.Files {
		if f := fileFromRow(r); f.Disposition != "audited" {
			out[f.Reason] += f.FileCount()
		}
	}
	return out
}

func TestScansShowTotalsAreTheSameCollapsedOrNot(t *testing.T) {
	c, e, _ := recordWalk(t)
	got, want := notAuditedByReason(c), notAuditedByReason(e)
	if len(c.Bundle.Files) >= len(e.Bundle.Files) {
		t.Fatalf("fixture did not collapse: %d rows vs %d", len(c.Bundle.Files), len(e.Bundle.Files))
	}
	if !reflect.DeepEqual(got, want) || want["skipped-dir"] != 50 || want["is-test"] != 11 || want["gitignored"] != 4 {
		t.Errorf("collapsed %v, every-file %v; want identical with skipped-dir 50, is-test 11, gitignored 4", got, want)
	}
}

func TestScansShowPrintsACollapsedRowWithItsCount(t *testing.T) {
	files := []scanstore.File{
		{Path: "dist/", Disposition: "rejected", Reason: "skipped-dir", Files: 50},
		{Lang: "python", Disposition: "rejected", Reason: "is-test", Files: 6},
		{Disposition: "rejected", Reason: "is-test", Files: 1},
		{Path: "one.go", Disposition: "rejected", Reason: "no-language"},
	}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7"}, openFake(&fakeScansReader{files: files}), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"dist/ (50 file(s), skipped-dir)", "python test files (6 file(s), is-test)", "test files (1 file(s), is-test)", "one.go"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "one.go (") {
		t.Errorf("a one-file row must print its bare path:\n%s", out.String())
	}
}

// An entry decoded from JSON without "Files" (written before the column), a
// collapsed one and an every-file one: each totals right.
func TestReadersHandleOldCollapsedAndEveryFileEntriesTogether(t *testing.T) {
	c, e, _ := recordWalk(t)
	var old ledger.Row
	if err := json.Unmarshal([]byte(`{"Path":"a.js","Disposition":"rejected","Reason":"skipped-dir"}`), &old); err != nil {
		t.Fatal(err)
	}
	oldEntry := auditpush.LedgerEntry{Bundle: auditpush.Bundle{Files: []auditpush.Row{old, old}}}
	if got := notAuditedByReason(oldEntry); got["skipped-dir"] != 2 {
		t.Errorf("an entry with no Files column must count one per row, got %v", got)
	}
	if !reflect.DeepEqual(notAuditedByReason(c), notAuditedByReason(e)) {
		t.Errorf("collapsed %v != every-file %v", notAuditedByReason(c), notAuditedByReason(e))
	}
}

// The warehouse door: SUM(ledger.FilesSQL) over rejected rows equals the Go
// rule's total, for the collapsed and the every-file entry alike.
func TestWarehouseFileCountMatchesTheGoRule(t *testing.T) {
	c, e, dir := recordWalk(t)
	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for scan, entry := range map[int]auditpush.LedgerEntry{1: c, 2: e} {
		want := 0
		for _, r := range entry.Bundle.Files {
			want += r.FileCount()
		}
		var got int
		q := `SELECT SUM(` + ledger.FilesSQL + `) FROM corral_audits WHERE scan_id = ?`
		if err := db.QueryRow(q, scan).Scan(&got); err != nil || got != want {
			t.Errorf("scan %d: SQL counts %d files (err %v), Go rule %d", scan, got, err, want)
		}
	}
}

func TestBriefNamesACollapsedRowWithItsCount(t *testing.T) {
	c, _, _ := recordWalk(t)
	c.Pushed = c.Pushed.UTC()
	b := buildBrief("d", []auditpush.LedgerEntry{c}, []string{"dist/f00.js"}, 10)
	out := renderBrief(b)
	if !strings.Contains(out, "dist/ (50 file(s), skipped-dir)") {
		t.Errorf("a file inside a collapsed directory must be answered by its counted row:\n%s", out)
	}
	whole := renderBrief(buildBrief("d", []auditpush.LedgerEntry{c}, []string{"."}, 10))
	if !strings.Contains(whole, "python test files (6 file(s), is-test)") || strings.Contains(whole, "\n\n\n") {
		t.Errorf("a path-less counted row needs a label, not an empty path:\n%s", whole)
	}
}
