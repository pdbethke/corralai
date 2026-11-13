// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// seatProvidersBundle is a one-file scan bundle. Without providers it is
// exactly the bundle preProviderRowsSHA256 was computed from.
func seatProvidersBundle(providers string) auditpush.Bundle {
	kr := 0.5
	return auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "o/r", ScanID: 7, Commit: "deadbeef", Audited: 1, Candidates: 1, ModelSet: "critic=off,mutant-generator=qwen3:8b,shadow=off,test-writer=gpt-x"},
		Files: []auditpush.Row{
			{Repo: "o/r", ScanID: 7, Path: "pkg/a.go", Commit: "deadbeef", KillRate: &kr, Survivors: 2,
				Disposition: "audited", Evidence: "proven", ModelsByRole: `{"mutant-generator":"qwen3:8b","test-writer":"gpt-x"}`,
				SeatProviders: providers},
		},
	}
}

// preProviderRowsSHA256 is warehouseRowsSHA256 of seatProvidersBundle("")
// as the binary at origin/main 07df580a computed it, before Row had a
// SeatProviders field or corral_audits a seat_providers column (computed
// 2026-11-12 by running that tree's warehouseRowsSHA256 on this bundle).
// A statement signed by that binary names this value.
const preProviderRowsSHA256 = "151a1c7c501e89d5b6edb684a8eb661ab1be755ca11f624538e6f68e52b1ef91"

func openTestWarehouse(t *testing.T, target string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open %s: %v", target, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The file row's providers survive a push to the warehouse and the read
// back, and the columns around the new one keep their values: the INSERT's
// placeholder count and the read's Scan order both line up.
func TestSeatProvidersSurviveTheWarehouse(t *testing.T) {
	const providers = `{"mutant-generator":"ollama","test-writer":"openai"}`
	b := seatProvidersBundle(providers)
	b.Files[0].SeatTools = `{"test-writer":"codex-cli 1"}`
	cu := 3
	b.Files[0].CoveringUnits = &cu
	b.Files[0].BaselineFlaky = "pkg TestFlaky"
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	back, err := auditpush.ReadBundle(openTestWarehouse(t, target), "o/r", 7)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if len(back.Files) != 1 {
		t.Fatalf("read back %d file rows, want 1", len(back.Files))
	}
	f := back.Files[0]
	if f.SeatProviders != providers {
		t.Errorf("SeatProviders = %q, want %q", f.SeatProviders, providers)
	}
	if f.SeatTools != b.Files[0].SeatTools || f.CoveringUnits == nil || *f.CoveringUnits != 3 || f.BaselineFlaky != "pkg TestFlaky" {
		t.Errorf("a neighbouring column moved: seat_tools %q covering_units %v baseline_flaky %q", f.SeatTools, f.CoveringUnits, f.BaselineFlaky)
	}
	// And the rows hash a statement signs is reproduced from what was read.
	signed, err := warehouseRowsSHA256(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := warehouseRowsSHA256(back); err != nil || got != signed {
		t.Errorf("rows hash read back %q (%v), signed %q", got, err, signed)
	}
}

// A row with no providers hashes exactly as it did before the field existed,
// so a statement an older binary signed still verifies against a warehouse
// this binary pushed (its seat_providers column NULL), end to end through
// corral verify --attest --db.
func TestAPreProviderStatementStillVerifies(t *testing.T) {
	b := seatProvidersBundle("")
	if got, err := warehouseRowsSHA256(b); err != nil || got != preProviderRowsSHA256 {
		t.Fatalf("a row with no providers hashes %q (%v), want the pre-change %q", got, err, preProviderRowsSHA256)
	}
	stmtPath := signedFixtureStatement(t, "o/r", 7, preProviderRowsSHA256)
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, b); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	var null sql.NullString
	if err := openTestWarehouse(t, target).QueryRow(`SELECT seat_providers FROM corral_audits`).Scan(&null); err != nil || null.Valid {
		t.Fatalf("seat_providers = %+v (%v), want NULL for a row that recorded none", null, err)
	}
	_, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if !strings.Contains(out, "✓ warehouse rows:") {
		t.Errorf("the pre-change statement no longer verifies:\n%s%s", out, errb)
	}
}

// A review's providers reach corral_reviews as JSON, beside the tools, and a
// review that recorded none writes NULL. There is no typed review read-back
// in auditpush (models rank reads corral_reviews by SQL), so this reads the
// columns the same way.
func TestReviewSeatProvidersReachTheWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	with := auditpush.LedgerEntry{Hash: "aa11", Kind: auditpush.KindReview, Pushed: at,
		Review: &review.Review{Repo: "o/r", Commit: "deadbeef", Scope: "pkg", ReviewerModel: "gpt-x", ReviewerTool: "r-cli 1",
			VerifierModel: "qwen3:8b", VerifierTool: "v-cli 2", GeneratedWith: "gw",
			SeatProviders: map[string]string{"reviewer": "openai", "verifier": "ollama"}}}
	without := auditpush.LedgerEntry{Hash: "bb22", Kind: auditpush.KindReview, Pushed: at,
		Review: &review.Review{Repo: "o/r", Commit: "deadbeef", Scope: "pkg", ReviewerModel: "m"}}
	for _, e := range []auditpush.LedgerEntry{with, without} {
		if _, err := auditpush.PushReviewEntry(target, e, true); err != nil {
			t.Fatalf("PushReviewEntry %s: %v", e.Hash, err)
		}
	}
	rows, err := openTestWarehouse(t, target).Query(`SELECT review_uid, reviewer_tool, verifier_tool, generated_with, seat_providers FROM corral_reviews ORDER BY review_uid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][4]sql.NullString{}
	for rows.Next() {
		var uid string
		var c [4]sql.NullString
		if err := rows.Scan(&uid, &c[0], &c[1], &c[2], &c[3]); err != nil {
			t.Fatal(err)
		}
		got[uid] = c
	}
	w := got["aa11"]
	if w[0].String != "r-cli 1" || w[1].String != "v-cli 2" || w[2].String != "gw" {
		t.Errorf("a neighbouring column moved: %+v", w)
	}
	if w[3].String != `{"reviewer":"openai","verifier":"ollama"}` {
		t.Errorf("seat_providers = %q, want the review's providers", w[3].String)
	}
	if n := got["bb22"][3]; n.Valid {
		t.Errorf("a review with no providers wrote %q, want NULL", n.String)
	}
}
