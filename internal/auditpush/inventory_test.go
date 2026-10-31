// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"path/filepath"
	"testing"
)

// The walk's inventory, the shadow draw, the token-cap outcome and the
// per-file mutant and writer counts survive the warehouse, a measured zero
// as 0 and an unmeasured count as nil.
func TestInventoryDrawAndCountsReachTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	b := sampleBundle()
	yes := true
	b.Scan.Inventory = `{"excluded":3}`
	b.Scan.ShadowDraw = `[{"seed":"0x000000000000002a"}]`
	b.Scan.MaxTokensReached = &yes
	zero, two := 0, 2
	b.Files[0].MutantsUnplaced, b.Files[0].MutantsUnmeasured, b.Files[0].MutantsDuplicate = &two, &zero, &two
	b.Files[0].WriterAttempts = `{"min":1,"median":2,"max":3,"seats_ungraded":0}`
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	db := openWarehouse(t, target)
	got, ok, err := readScanRow(db, "repo = ?", "o/r")
	if err != nil || !ok {
		t.Fatalf("readScanRow: ok=%v err=%v", ok, err)
	}
	if got.Inventory != b.Scan.Inventory || got.ShadowDraw != b.Scan.ShadowDraw || got.MaxTokensReached == nil || !*got.MaxTokensReached {
		t.Errorf("scan round trip = %q %q %v", got.Inventory, got.ShadowDraw, got.MaxTokensReached)
	}
	rows, err := readFileRows(db, "repo = ?", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	var a Row
	for _, r := range rows {
		if r.Path == "pkg/a.go" {
			a = r
		}
	}
	if a.MutantsUnplaced == nil || *a.MutantsUnplaced != 2 || a.MutantsUnmeasured == nil || *a.MutantsUnmeasured != 0 ||
		a.MutantsDuplicate == nil || *a.MutantsDuplicate != 2 || a.WriterAttempts != b.Files[0].WriterAttempts {
		t.Errorf("row round trip = %v %v %v %q", a.MutantsUnplaced, a.MutantsUnmeasured, a.MutantsDuplicate, a.WriterAttempts)
	}
}

// A row pushed without them reads back with them absent: no cap is not a cap
// that was not reached, and no count is not a count of zero.
func TestUnmeasuredInventoryAndCountsStayNullInTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := PushBundle(target, sampleBundle()); err != nil {
		t.Fatal(err)
	}
	db := openWarehouse(t, target)
	got, _, err := readScanRow(db, "repo = ?", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if got.Inventory != "" || got.ShadowDraw != "" || got.MaxTokensReached != nil {
		t.Errorf("an unrecorded scan field was stored as a value: %q %q %v", got.Inventory, got.ShadowDraw, got.MaxTokensReached)
	}
	rows, err := readFileRows(db, "repo = ?", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.MutantsUnplaced != nil || r.MutantsUnmeasured != nil || r.MutantsDuplicate != nil || r.WriterAttempts != "" {
			t.Errorf("%s: an unrecorded count was stored as a value: %+v", r.Path, r)
		}
	}
}
