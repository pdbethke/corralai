// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// A row stands for one file unless it says it stands for more. 0 must mean
// one, so every row written before the field existed still counts as one.
func TestFileCountIsOneUnlessTheRowStandsForMore(t *testing.T) {
	for _, c := range []struct{ files, want int }{{0, 1}, {1, 1}, {50, 50}} {
		if got := (ledger.Row{Files: c.files}).FileCount(); got != c.want {
			t.Errorf("ledger.Row{Files: %d}.FileCount() = %d, want %d", c.files, got, c.want)
		}
		if got := (scanstore.File{Files: c.files}).FileCount(); got != c.want {
			t.Errorf("scanstore.File{Files: %d}.FileCount() = %d, want %d", c.files, got, c.want)
		}
	}
	if got := (ledger.Row{}).FileCount(); got != 1 {
		t.Errorf("zero Row FileCount() = %d, want 1", got)
	}
}

// An entry written without the field has FileCount() 1, and a one-file row
// marshals to the bytes it always did (no "Files" key), so an old entry's
// hash does not move.
func TestAnEntryWrittenBeforeFilesCountsEachRowAsOneFile(t *testing.T) {
	var r ledger.Row
	if err := json.Unmarshal([]byte(`{"Repo":"r","Path":"a.go"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Files != 0 || r.FileCount() != 1 {
		t.Errorf("old-shape row: Files = %d, FileCount() = %d, want 0 and 1", r.Files, r.FileCount())
	}
	b, err := json.Marshal(ledger.Row{Repo: "r", Path: "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["Files"]; has {
		t.Errorf("a one-file row must not serialize a Files key (it would change every old entry's bytes): %s", b)
	}
	b, _ = json.Marshal(ledger.Row{Files: 50})
	if err := json.Unmarshal(b, &m); err != nil || m["Files"] != float64(50) {
		t.Errorf("a collapsed row must serialize Files: %s", b)
	}
}
