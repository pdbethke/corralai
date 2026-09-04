// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestReadBundleRoundTripsPushedRows is ReadBundle's own correctness proof:
// push sampleBundle() (the same rich fixture TestPushBundleMigratesEvery...
// and the custody tests use — every grain populated, several nullable
// fields exercised both ways), read it back, and confirm the CANONICAL
// bytes match — not a field-by-field struct comparison (time.Time's
// monotonic reading and DuckDB's own TIMESTAMPTZ precision make
// reflect.DeepEqual the wrong tool here), but json.Marshal equality, which
// is what warehouseRowsSHA256 actually hashes.
func TestReadBundleRoundTripsPushedRows(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	original := sampleBundle()
	if _, err := PushBundle(target, original); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}

	db := openWarehouse(t, target)
	got, err := ReadBundle(db, original.Scan.Repo, original.Scan.ScanID)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}

	if len(got.Files) != len(original.Files) {
		t.Fatalf("got %d file row(s), want %d", len(got.Files), len(original.Files))
	}
	if len(got.Mutants) != len(original.Mutants) {
		t.Fatalf("got %d mutant row(s), want %d", len(got.Mutants), len(original.Mutants))
	}
	if len(got.Calls) != len(original.Calls) {
		t.Fatalf("got %d call row(s), want %d", len(got.Calls), len(original.Calls))
	}
	if len(got.Events) != len(original.Events) {
		t.Fatalf("got %d event row(s), want %d", len(got.Events), len(original.Events))
	}

	// stampLink is what PushBundle actually applies before writing — the
	// ORIGINAL bundle's own hash (what the statement signs) is computed
	// AFTER stampLink runs (certify_repo.go builds the bundle, THEN calls
	// PushBundle, which stamps ScanID/StatementSHA256 from Link onto every
	// row) — but sampleBundle() already sets ScanID/StatementSHA256
	// directly on every row and passes an empty Link, so stampLink is a
	// no-op here and `original` is already what got hashed in production.
	// SourcePushed also needs to match what a real caller compares against.
	got.SourcePushed = original.SourcePushed
	got.Link = original.Link

	// BlankUnpushedSource is what warehouseRowsSHA256 itself applies before
	// hashing (and what PushBundle applies before writing) — sampleBundle()
	// does not set SourcePushed, so the source columns (AuthoredTest,
	// VerdictJSON, Mutants[].Code) are withheld at the WRITE, and comparing
	// against the un-blanked original here would fault ReadBundle for
	// faithfully reporting what the warehouse actually holds.
	want := original
	BlankUnpushedSource(&want)
	// scan_uid is minted by the PUSH (from its own timestamp), never
	// carried in by the caller, so the read-back is the first place it
	// exists. It must be non-empty — a reader that could not see the
	// push's identity could not tell two pushes of one scan apart — and
	// it is the one field the original cannot have had.
	if got.Scan.ScanUID == "" {
		t.Fatalf("read-back scan carries no scan_uid; the reader cannot identify the push it read")
	}
	got.Scan.ScanUID = ""

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal read-back: %v", err)
	}
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("read-back bundle is not byte-identical to the pushed one.\npushed: %s\nread:   %s", wantJSON, gotJSON)
	}
}
