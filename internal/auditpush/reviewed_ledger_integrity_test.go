// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/review"
)

// The six findings of review ed079ca08965 (a Claude Code reviewer on
// internal/auditpush, Codex verifying, 2026-09-07), all six confirmed on
// adjudication. Three carried reproductions, kept here inverted; the
// other three have the tests they should have had. Every one fails on the
// code as it stood.

// R4 — the entry hash must see a recorded false or zero. Under the sparse
// form, "passed: false, kill_rate 0" (measured) and both absent (never
// measured) hashed identically and one signature verified for both.
func TestEntryHashDistinguishesAMeasuredZeroFromNothingMeasured(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	failed := false
	zero := 0.0
	a := LedgerEntry{
		Format: LedgerFileFormat, Pushed: at, ScanUID: "uid", Prev: "p",
		Bundle: Bundle{
			Scan:  ScanRow{Repo: "o/r", Commit: "deadbeef", Host: "h", Passed: &failed},
			Files: []Row{{Repo: "o/r", Commit: "deadbeef", Path: "a.go", Survivors: 3, KillRate: &zero, Passed: &failed}},
		},
	}
	b := LedgerEntry{
		Format: LedgerFileFormat, Pushed: at, ScanUID: "uid", Prev: "p",
		Bundle: Bundle{
			Scan:  ScanRow{Repo: "o/r", Commit: "deadbeef", Host: "h"},
			Files: []Row{{Repo: "o/r", Commit: "deadbeef", Path: "a.go", Survivors: 3}},
		},
	}
	ha, err := EntryHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := EntryHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Fatalf("two entries making opposite claims hash identically: %s", ha)
	}
	// And a written entry, edited on disk to drop the false, must fail
	// verification — the hash is over the bytes in the file.
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := Ed25519LedgerSigner{KeyID: "k", Key: priv}
	if _, err := AppendLedgerEntry(dir, a, signer); err != nil {
		t.Fatal(err)
	}
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil || len(checks) != 1 || !checks[0].HashOK || !checks[0].SigOK {
		t.Fatalf("the entry as written must verify: %+v %v", checks, err)
	}
	name := filepath.Join(dir, ScansSubdir, checks[0].File)
	raw, err := readMaybeGzip(name)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(raw, []byte(`"Passed": false`), []byte(`"Passed": null`), -1)
	if bytes.Equal(edited, raw) {
		t.Fatalf("fixture: no Passed:false in the written entry:\n%s", raw)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(edited) //nolint:errcheck
	zw.Close()
	if err := os.WriteFile(name, gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	checks, err = VerifyLedgerDir(dir, pub)
	if err != nil || len(checks) != 1 {
		t.Fatal(err)
	}
	if checks[0].HashOK {
		t.Fatalf("an entry edited on disk from measured-false to never-measured still verifies: %+v", checks[0])
	}
}

// R2 — a scan entry's uid derives from its own row and Pushed, so a reader
// holding the entry (or the view over it) reproduces it.
func TestLedgerDirScanUIDIsRecomputableFromTheEntry(t *testing.T) {
	dir := t.TempDir() + "/"
	b := Bundle{Scan: ScanRow{Repo: "o/r", ScanID: 7, Commit: "deadbeef", Host: "h", CorralVersion: "v", Substrate: "workspace"}}
	if _, err := PushBundle(dir, b); err != nil {
		t.Fatalf("push: %v", err)
	}
	entries, err := ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read dir: %d, %v", len(entries), err)
	}
	e := entries[0]
	if got := RecomputeScanUID(e.Bundle.Scan, e.Pushed); got != e.ScanUID || e.Bundle.Scan.ScanUID != e.ScanUID {
		t.Fatalf("entry scan_uid %s; recomputed from its row and Pushed %s; row carries %q", e.ScanUID, got, e.Bundle.Scan.ScanUID)
	}
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var stored string
	var ts time.Time
	if err := db.QueryRow(`SELECT scan_uid, ts FROM corral_scans WHERE repo = 'o/r' AND scan_id = 7`).Scan(&stored, &ts); err != nil {
		t.Fatal(err)
	}
	rb, err := ReadBundle(db, "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if view := RecomputeScanUID(rb.Scan, ts); view != stored {
		t.Fatalf("the view's row does not recompute: stored %s, recomputed %s", stored, view)
	}
	// And verify checks it: an entry whose uid was minted elsewhere is named.
	checks, err := VerifyLedgerDir(dir, nil)
	if err != nil || len(checks) != 1 || checks[0].Problem != "" {
		t.Fatalf("a well-formed entry must pass: %+v %v", checks, err)
	}
}

// R3 — a retraction reaches every kind: a retracted review's rows leave
// the view and the push, as a retracted scan's do.
func TestRetractedReviewLeavesTheViewAndThePush(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteReview(dir, review.Review{
		Repo: "o/r", Commit: "deadbeef", Scope: "internal/x", ReviewerModel: "m",
		Findings: []review.Finding{{ID: "R1", Claim: "c", Declared: "HYPOTHESIS", Tier: "HYPOTHESIS"}},
		Sound:    []string{"a", "b", "c"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sb := Bundle{
		Scan:  ScanRow{Repo: "o/r", ScanID: 9, Commit: "deadbeef", Host: "h", CorralVersion: "v", Substrate: "workspace"},
		Files: []Row{{Repo: "o/r", Commit: "deadbeef", ScanID: 9, Path: "a.go", Disposition: "audited"}},
	}
	if _, err := PushBundle(dir+"/", sb); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	var reviewHash, scanHash string
	for _, e := range entries {
		switch e.Kind {
		case KindReview:
			reviewHash = e.Hash
		case KindScan:
			scanHash = e.Hash
		}
	}
	// An adjudication of the review, which goes with it.
	if _, err := WriteAdjudication(dir, reviewHash+"#R1", VerdictConfirmed, "p", "real", nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{reviewHash, scanHash} {
		if _, err := WriteRetraction(dir, h, "wrong", nil); err != nil {
			t.Fatal(err)
		}
	}
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range []string{"corral_scans", "corral_audits", "corral_reviews", "corral_findings", "corral_adjudications"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s holds %d row(s) of retracted entries in the view", tbl, n)
		}
	}
	// The push: dry run counts them as retracted, not pushable.
	plan, err := PushLedgerDir(dir, filepath.Join(t.TempDir(), "w.duckdb"), false, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Retracted != 3 || plan.Reviews != 0 || plan.Adjudications != 0 || plan.Scans != 0 {
		t.Fatalf("the push must leave every retracted entry out: %+v", plan)
	}
}

// R1 — every timestamp the rows carry is canonicalised, the two added with
// the ledger included; a statement over them must hash what the warehouse
// stores.
func TestCanonicalizeForWarehouseReachesEveryTimestamp(t *testing.T) {
	loc := time.FixedZone("x", 3*3600)
	ts := time.Date(2026, 9, 7, 10, 0, 0, 123456789, loc)
	b := Bundle{
		Scan:  ScanRow{StartedAt: &ts, FinishedAt: &ts},
		Files: []Row{{StartedAt: &ts, ComputedAt: &ts}},
	}
	CanonicalizeForWarehouse(&b)
	want := ts.UTC().Truncate(time.Microsecond)
	for name, got := range map[string]*time.Time{
		"scan.started_at": b.Scan.StartedAt, "scan.finished_at": b.Scan.FinishedAt,
		"file.started_at": b.Files[0].StartedAt, "file.computed_at": b.Files[0].ComputedAt,
	} {
		if got == nil || !got.Equal(want) || got.Location() != time.UTC || got.Nanosecond()%1000 != 0 {
			t.Errorf("%s not canonical: %v", name, got)
		}
	}
}

// R5 — the file name a chain check reports is the file the entry came
// from, whatever the order two entries inside one second sort to.
func TestChainCheckNamesTheEntrysOwnFile(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := WriteReview(dir, review.Review{Repo: "o/r", Commit: "c" + strings.Repeat("0", 11), Scope: "s", ReviewerModel: "m", Sound: []string{"a"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	checks, err := VerifyLedgerDir(dir, nil)
	if err != nil || len(checks) != 3 {
		t.Fatal(err)
	}
	for _, c := range checks {
		e, err := ReadLedgerEntry(filepath.Join(dir, ScansSubdir, c.File))
		if err != nil {
			t.Fatal(err)
		}
		if e.Hash != hashOfCheck(t, dir, c) {
			t.Errorf("check reports file %s, whose entry is not the one checked", c.File)
		}
	}
}

// hashOfCheck finds the entry a check describes by its position in the chain.
func hashOfCheck(t *testing.T, dir string, c ChainCheck) string {
	t.Helper()
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.File == c.File {
			return e.Hash
		}
	}
	return ""
}

// R6 — a review push is one transaction: a review row never lands
// without its findings.
func TestPushReviewEntryIsOneTransaction(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	e := LedgerEntry{Kind: KindReview, Hash: strings.Repeat("a", 64), Pushed: time.Now(), Review: &review.Review{
		Repo: "o/r", Commit: "c", Scope: "s", ReviewerModel: "m",
		Findings: []review.Finding{{ID: "R1", Claim: "ok", Declared: "HYPOTHESIS", Tier: "HYPOTHESIS"}},
	}}
	// Make the findings insert fail: a corral_findings table of the wrong
	// shape (DuckDB has no CHECK constraints to add). The review row's
	// insert still succeeds, so without a transaction it lands alone.
	db, err := openWarehouseForWrite(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE corral_findings; CREATE TABLE corral_findings (review_uid VARCHAR)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := PushReviewEntry(target, e, false); err == nil {
		t.Fatal("fixture: the second finding must fail to insert")
	}
	db, err = openWarehouseForWrite(target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var reviews, findings int
	db.QueryRow(`SELECT count(*) FROM corral_reviews`).Scan(&reviews)   //nolint:errcheck
	db.QueryRow(`SELECT count(*) FROM corral_findings`).Scan(&findings) //nolint:errcheck
	if reviews != 0 || findings != 0 {
		t.Fatalf("a failed push left %d review row(s) and %d finding row(s) — half-landed", reviews, findings)
	}
}

// An entry written under corral-ledger-2 is still read and verified under
// its own rules, and the check says which rules those were.
func TestFormat2EntriesStillVerify(t *testing.T) {
	dir := t.TempDir()
	e := LedgerEntry{Format: ledgerFormat2, Pushed: time.Now().UTC().Truncate(time.Microsecond), ScanUID: "minted-elsewhere",
		Bundle: Bundle{Scan: ScanRow{Repo: "o/r", Commit: "deadbeef", Host: "h", ScanUID: "minted-elsewhere"}}}
	h, err := EntryHash(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Hash = h
	js, _ := json.MarshalIndent(e, "", " ")
	if err := os.MkdirAll(filepath.Join(dir, ScansSubdir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ScansSubdir, "20260901T000000Z-deadbeef-minted.json"), js, 0o600); err != nil {
		t.Fatal(err)
	}
	checks, err := VerifyLedgerDir(dir, nil)
	if err != nil || len(checks) != 1 {
		t.Fatal(err)
	}
	if !checks[0].HashOK || checks[0].Problem != "" || !strings.Contains(checks[0].Note, ledgerFormat2) {
		t.Fatalf("a format-2 entry must verify under its own rules and say so: %+v", checks[0])
	}
}
