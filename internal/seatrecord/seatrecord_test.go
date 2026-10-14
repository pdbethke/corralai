// SPDX-License-Identifier: Elastic-2.0

package seatrecord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The public warehouse extract (scripts/build-warehouse-parquet.py) is a
// per-model reader too, written in Python, so it cannot import this constant.
// It carries the same text instead, and this test is what keeps it the same:
// the public scorecard and `corral models rank` must not disagree about which
// rows count.
func TestTheWarehouseExtractUsesTheSamePredicate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build-warehouse-parquet.py"))
	if err != nil {
		t.Fatal(err)
	}
	want := `RESOLVED_ONLY = "` + ResolvedOnly + `"`
	if !strings.Contains(string(b), want) {
		t.Fatalf("scripts/build-warehouse-parquet.py must define %s — the same predicate the Go stores apply", want)
	}
}
