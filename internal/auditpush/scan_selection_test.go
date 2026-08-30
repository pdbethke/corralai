// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"path/filepath"
	"testing"
)

// TestScanRowCarriesTheSelectionRunToTheWarehouse. The instrumented coverage
// pass happens once per SCAN, so the warehouse carries it at the scan grain —
// `sum(total_ms)` over a scan's audits is then a sound number, which it could
// not be while a per-file copy of one shared run was the only place it lived.
// A scan that instrumented nothing writes NULL, never 0.
func TestScanRowCarriesTheSelectionRunToTheWarehouse(t *testing.T) {
	b := sampleBundle()
	b.Scan.SelectionMillis = ms(92000)
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	db := openWarehouse(t, target)
	var got *int64
	if err := db.QueryRow(`SELECT selection_ms FROM corral_scans`).Scan(&got); err != nil {
		t.Fatalf("read selection_ms: %v", err)
	}
	if got == nil || *got != 92000 {
		t.Fatalf("corral_scans.selection_ms = %v, want 92000", got)
	}
}

func TestWholeSuiteScanWritesNullSelection(t *testing.T) {
	b := sampleBundle() // no SelectionMillis: the pass never ran
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	db := openWarehouse(t, target)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM corral_scans WHERE selection_ms IS NOT NULL`).Scan(&n); err != nil {
		t.Fatalf("probe selection_ms: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d scan row(s) claim a selection pass that never ran", n)
	}
}
