// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"path/filepath"
	"testing"
)

func TestScanHostConditionsReachTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	b := sampleBundle()
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }
	b.Scan.LoadStart, b.Scan.LoadEnd, b.Scan.LoadPeak = f(13.5), f(38.25), f(40)
	b.Scan.MemAvailableStartBytes, b.Scan.PeakRSSBytes, b.Scan.CPUMillis = i(1<<33), i(1<<28), i(0)
	b.Scan.HostNotMeasured = "example"
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	got, ok, err := readScanRow(openWarehouse(t, target), "repo = ?", "o/r")
	if err != nil || !ok {
		t.Fatalf("readScanRow: ok=%v err=%v", ok, err)
	}
	if got.LoadStart == nil || *got.LoadStart != 13.5 || *got.LoadEnd != 38.25 || *got.LoadPeak != 40 ||
		*got.MemAvailableStartBytes != 1<<33 || *got.PeakRSSBytes != 1<<28 || got.CPUMillis == nil || *got.CPUMillis != 0 ||
		got.HostNotMeasured != "example" {
		t.Errorf("host conditions round trip = %+v", got)
	}
}

// A figure that was not measured must come back nil, not 0.
func TestUnmeasuredHostConditionsStayNullInTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	b := sampleBundle()
	b.Scan.HostNotMeasured = "not measured on plan9"
	if _, err := PushBundle(target, b); err != nil {
		t.Fatal(err)
	}
	got, _, err := readScanRow(openWarehouse(t, target), "repo = ?", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if got.LoadStart != nil || got.LoadPeak != nil || got.CPUMillis != nil || got.PeakRSSBytes != nil || got.MemAvailableStartBytes != nil {
		t.Errorf("an unmeasured figure was stored as a value: %+v", got)
	}
}
