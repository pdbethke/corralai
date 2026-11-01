// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"path/filepath"
	"testing"
)

func TestScanToolchainsReachTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	b := sampleBundle()
	b.Scan.Toolchains = `{"typescript":{"node":"v24.22.1","vitest":"vitest/5.0.3 linux-x64 node-v24.22.1"}}`
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	got, ok, err := readScanRow(openWarehouse(t, target), "repo = ?", "o/r")
	if err != nil || !ok {
		t.Fatalf("readScanRow: ok=%v err=%v", ok, err)
	}
	if got.Toolchains != b.Scan.Toolchains {
		t.Errorf("toolchains round trip = %q, want %q", got.Toolchains, b.Scan.Toolchains)
	}
}
