// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The verdict generation names the grading rules that produced an entry. The
// scan record is copied field by field in both directions, and a converter
// like that fails open on the field nobody added, so this follows the value
// from the scan into the bundle and back out through the scans reader.
func TestBundleCarriesVerdictGeneration(t *testing.T) {
	scan := scanstore.Scan{Owner: "o", Repo: "r", Commit: "c", VerdictGeneration: reposcan.VerdictGeneration}
	b := buildBundle(scan, 0, nil, nil, nil, nil, auditpush.Link{}, false, "r", "c", "", bundleMeta{})
	if b.Scan.VerdictGeneration != reposcan.VerdictGeneration {
		t.Fatalf("buildBundle dropped the verdict generation: got %q, want %q", b.Scan.VerdictGeneration, reposcan.VerdictGeneration)
	}
	e := auditpush.LedgerEntry{}
	e.Bundle = b
	if got := scanRowFromEntry(e, 1).VerdictGeneration; got != reposcan.VerdictGeneration {
		t.Fatalf("the scans reader dropped the verdict generation: got %q, want %q", got, reposcan.VerdictGeneration)
	}
}
