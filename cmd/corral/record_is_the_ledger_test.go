// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// legacyScanWriters are the scanstore.Store methods that wrote the retired
// DuckDB scan record. A file calls one when it imports scanstore, names
// the store's type, and calls a method by one of these names.
var (
	legacyScanWriters = regexp.MustCompile(`\.(Record|RecordMutants|RecordModelCalls|RecordEvents|SetStatementSHA256|SetSourcePushed|SetRekorReceipt)\(`)
	scanStoreTyped    = regexp.MustCompile(`\*?scanstore\.Store\b`)
)

func callsLegacyScanWriter(src string) bool {
	return strings.Contains(src, "internal/scanstore") && scanStoreTyped.MatchString(src) && legacyScanWriters.MatchString(src)
}

// TestNothingInTheProductWritesTheLegacyScanRecord: the DuckDB scan record
// (scanstore's scans / scan_files / scan_mutants / scan_model_calls /
// scan_events) was retired when the ledger directory became THE record.
// Its writer methods still exist — the store's own tests build fixtures
// with them, and `corral scans --db` reads what older corrals wrote — so
// the thing keeping a second copy of the record from quietly coming back
// is this walk: no non-test Go file in the product, outside the store's
// own package, may call one of them.
//
// A property, not an enumeration of callers: the walk covers every
// package, so a new call site anywhere fails here rather than reintroducing
// the two-schemas-for-one-record drift the retirement closed.
func TestNothingInTheProductWritesTheLegacyScanRecord(t *testing.T) {
	// The negative control first: a file that DOES call a writer is caught.
	if !callsLegacyScanWriter(`import "github.com/pdbethke/corralai/internal/scanstore"
func f(st *scanstore.Store) { _, _ = st.Record(ctx, scan, files) }`) {
		t.Fatal("the detector missed a file that calls scanstore.Store.Record — the walk below would prove nothing")
	}
	// And the other stores' Record methods are not the scan record.
	if callsLegacyScanWriter(`import "github.com/pdbethke/corralai/internal/bugcatch"
func f(s *bugcatch.Store) { _ = s.Record(ctx, rows) }`) {
		t.Fatal("the detector flags a store that is not scanstore")
	}

	root := filepath.Join("..", "..")
	var offenders []string
	walked := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if (strings.HasPrefix(name, ".") && path != root) || name == "node_modules" || name == "site" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(filepath.ToSlash(rel), "internal/scanstore/") {
			return nil
		}
		body, rerr := os.ReadFile(path) // #nosec G304 -- this repository's own source
		if rerr != nil {
			return rerr
		}
		walked++
		if callsLegacyScanWriter(string(body)) {
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked < 50 {
		t.Fatalf("walked only %d Go files — the walk is not covering the product", walked)
	}
	if len(offenders) > 0 {
		t.Errorf("these non-test files call a scan-record writer — the record is the ledger directory, not a DuckDB table:\n  %s", strings.Join(offenders, "\n  "))
	}
}
