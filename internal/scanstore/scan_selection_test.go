// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"path/filepath"
	"testing"
)

// TestScanRecordsTheSelectionRunOnce is the grain correction: the
// instrumented coverage run happens ONCE for a scan, not once per file.
// Recording it only on every file's row made `sum(total_ms)` over a scan
// count it once per file — inventing time nobody spent — so the scan header
// is where it belongs, and a scan that instrumented nothing stores NULL.
func TestScanRecordsTheSelectionRunOnce(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	ms := int64(92000)
	timed, err := st.Record(ctx, Scan{Repo: "o/r", Commit: "c1", SelectionMillis: &ms}, nil)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	whole, err := st.Record(ctx, Scan{Repo: "o/r", Commit: "c2"}, nil)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, ok, err := st.ScanByID(ctx, timed)
	if err != nil || !ok {
		t.Fatalf("ScanByID(%d) = ok %v, err %v", timed, ok, err)
	}
	if got.SelectionMillis == nil || *got.SelectionMillis != 92000 {
		t.Errorf("selection_ms = %v, want 92000", got.SelectionMillis)
	}

	got, ok, err = st.ScanByID(ctx, whole)
	if err != nil || !ok {
		t.Fatalf("ScanByID(%d) = ok %v, err %v", whole, ok, err)
	}
	if got.SelectionMillis != nil {
		t.Errorf("a scan that instrumented nothing stored %d; NULL is the only honest value", *got.SelectionMillis)
	}

	if _, ok, err := st.ScanByID(ctx, 9999); err != nil || ok {
		t.Errorf("ScanByID on an unknown id = ok %v, err %v; want (false, nil)", ok, err)
	}

	// The list reader sees the same column, so `scans list` and `scans show`
	// cannot disagree about what a scan cost.
	rows, err := st.Scans(ctx, 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	for _, r := range rows {
		if r.ID == timed && (r.SelectionMillis == nil || *r.SelectionMillis != 92000) {
			t.Errorf("Scans() lost selection_ms: %v", r.SelectionMillis)
		}
	}
}
