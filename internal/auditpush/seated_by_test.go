// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/review"
)

// How a seat was chosen survives the warehouse on both grains: the file row's
// SeatedBy and SeatEvidence are pushed and read back beside the neighbouring
// SeatProviders (so the INSERT's placeholder count and the read's Scan order
// both line up), and a review's land in corral_reviews as JSON text and text.
// A row that recorded none writes NULL on both.
func TestSeatedByReachesTheWarehouse(t *testing.T) {
	const seatedBy = `{"mutant-generator":"operator","test-writer":"evidence"}`
	const evidence = "the machine ledger (31 scans)"
	const providers = `{"test-writer":"openai"}`
	b := sampleBundle()
	b.Files[0].SeatProviders = providers
	b.Files[0].SeatedBy = seatedBy
	b.Files[0].SeatEvidence = evidence
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	db := openWarehouse(t, target)
	back, err := ReadBundle(db, b.Scan.Repo, b.Scan.ScanID)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	f := back.Files[0]
	if f.SeatedBy != seatedBy || f.SeatEvidence != evidence || f.SeatProviders != providers {
		t.Errorf("read back seated_by %q seat_evidence %q seat_providers %q", f.SeatedBy, f.SeatEvidence, f.SeatProviders)
	}

	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	with := LedgerEntry{Hash: "aa11", Kind: KindReview, Pushed: at,
		Review: &review.Review{Repo: "o/r", Commit: "deadbeef", Scope: "pkg", ReviewerModel: "m", VerifierModel: "v",
			SeatedBy:     map[string]string{"reviewer": "evidence", "verifier": "order"},
			SeatEvidence: evidence}}
	without := LedgerEntry{Hash: "bb22", Kind: KindReview, Pushed: at,
		Review: &review.Review{Repo: "o/r", Commit: "deadbeef", Scope: "pkg", ReviewerModel: "m"}}
	rt := filepath.Join(t.TempDir(), "r.duckdb")
	for _, e := range []LedgerEntry{with, without} {
		if _, err := PushReviewEntry(rt, e, true); err != nil {
			t.Fatalf("PushReviewEntry %s: %v", e.Hash, err)
		}
	}
	rows, err := openWarehouse(t, rt).Query(`SELECT review_uid, seated_by, seat_evidence FROM corral_reviews ORDER BY review_uid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]sql.NullString{}
	for rows.Next() {
		var uid string
		var s, e sql.NullString
		if err := rows.Scan(&uid, &s, &e); err != nil {
			t.Fatal(err)
		}
		got[uid] = [2]sql.NullString{s, e}
	}
	if w := got["aa11"]; w[0].String != `{"reviewer":"evidence","verifier":"order"}` || w[1].String != evidence {
		t.Errorf("review seated_by/seat_evidence = %+v, want the recorded values", w)
	}
	if w := got["bb22"]; w[0].Valid || w[1].Valid {
		t.Errorf("a review that recorded none wrote %+v, want NULL on both", w)
	}
}
