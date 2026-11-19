// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// walkRows is one fixture walk recorded both ways: 50 files in dist/ (skipped),
// 3 in scratch/ (gitignored), 6 python and 5 go test files, plus one audited
// file. everyFile writes a row per file, as before; otherwise one counted row
// per directory and per language.
func walkRows(everyFile bool) []auditpush.Row {
	rows := []auditpush.Row{{Path: "src/lib.rs", Lang: "rust", Disposition: "audited", KillRate: ptr(0.5), MutantsGraded: 2, Survivors: 1}}
	add := func(n int, mk func(i int) auditpush.Row, collapsed auditpush.Row) {
		if !everyFile {
			collapsed.Files = n
			rows = append(rows, collapsed)
			return
		}
		for i := 0; i < n; i++ {
			rows = append(rows, mk(i))
		}
	}
	add(50, func(i int) auditpush.Row {
		return auditpush.Row{Path: fmt.Sprintf("dist/f%d.js", i), Disposition: "rejected", Reason: "skipped-dir"}
	}, auditpush.Row{Path: "dist/", Disposition: "rejected", Reason: "skipped-dir"})
	add(3, func(i int) auditpush.Row {
		return auditpush.Row{Path: fmt.Sprintf("scratch/s%d.py", i), Disposition: "rejected", Reason: "gitignored"}
	}, auditpush.Row{Path: "scratch/", Disposition: "rejected", Reason: "gitignored"})
	add(6, func(i int) auditpush.Row {
		return auditpush.Row{Path: fmt.Sprintf("tests/test_%d.py", i), Lang: "python", Disposition: "rejected", Reason: "is-test"}
	}, auditpush.Row{Lang: "python", Disposition: "rejected", Reason: "is-test"})
	add(5, func(i int) auditpush.Row {
		return auditpush.Row{Path: fmt.Sprintf("pkg/x%d_test.go", i), Lang: "go", Disposition: "rejected", Reason: "is-test"}
	}, auditpush.Row{Lang: "go", Disposition: "rejected", Reason: "is-test"})
	return rows
}

func walkView(everyFile bool) ScanView {
	e := warpLikeScan("h", false)
	e.Bundle.Files = walkRows(everyFile)
	e.Bundle.Mutants = nil
	return buildScanView(e, ChainView{}, nil)
}

func TestRecordViewCountsFilesBehindCollapsedRows(t *testing.T) {
	collapsed, every := walkView(false), walkView(true)
	want := []ReasonCount{{"skipped-dir", 50}, {"is-test", 11}, {"gitignored", 3}}
	if !reflect.DeepEqual(every.NotAudited, want) {
		t.Fatalf("every-file entry: NotAudited = %v, want %v", every.NotAudited, want)
	}
	if !reflect.DeepEqual(collapsed.NotAudited, every.NotAudited) {
		t.Errorf("collapsed entry counts rows, not files: NotAudited = %v, want %v", collapsed.NotAudited, every.NotAudited)
	}
	if len(collapsed.Audited) != len(every.Audited) {
		t.Errorf("audited files differ: %d vs %d", len(collapsed.Audited), len(every.Audited))
	}
}

// An entry written before the Files column (no count at all), a collapsed one
// and an every-file one, read together: each is right on its own.
func TestRecordViewHandlesOldCollapsedAndEveryFileEntriesTogether(t *testing.T) {
	old := warpLikeScan("old", false) // Cargo.toml no-language, one is-test row, one audited
	views := []ScanView{buildScanView(old, ChainView{}, nil), walkView(false), walkView(true)}
	if got := views[0].NotAudited; !reflect.DeepEqual(got, []ReasonCount{{"is-test", 1}, {"no-language", 1}}) {
		t.Errorf("an entry with no Files counts one per row: %v", got)
	}
	if !reflect.DeepEqual(views[1].NotAudited, views[2].NotAudited) {
		t.Errorf("collapsed %v != every-file %v", views[1].NotAudited, views[2].NotAudited)
	}
}
