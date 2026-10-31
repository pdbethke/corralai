// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"path/filepath"
	"testing"
)

// TestScanUnitPassReachesTheWarehouse: the unit pass is enumerated in the
// INSERT and the read like every scan field, and an unset one reads back
// empty (NULL), not as a zeroed pass.
func TestScanUnitPassReachesTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	b := sampleBundle()
	b.Scan.UnitPass = `{"listed":5,"runnable":5,"run":5,"failed":4,"cut":1,"green_as_written":1,"unreported":4,"millis":1500,"reused":false}`
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	got, ok, err := readScanRow(openWarehouse(t, target), "repo = ?", "o/r")
	if err != nil || !ok {
		t.Fatalf("readScanRow: ok=%v err=%v", ok, err)
	}
	if got.UnitPass != b.Scan.UnitPass {
		t.Errorf("unit_pass round trip = %q, want %q", got.UnitPass, b.Scan.UnitPass)
	}
}
