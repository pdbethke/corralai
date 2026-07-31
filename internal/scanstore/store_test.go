// SPDX-License-Identifier: Elastic-2.0

package scanstore_test

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/scanstore"
)

func ptr(f float64) *float64 { return &f }

func TestRecordRoundTripsEveryDisposition(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	scan := scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "abc123",
		Substrate: "workspace", EngineVersion: "test-engine", ModelSet: "test-models",
		TotalFiles: 4, Candidates: 2, Audited: 1, KillRate: ptr(0.5),
		StartedAt: time.Unix(1700000000, 0).UTC(), FinishedAt: time.Unix(1700000060, 0).UTC(),
	}
	files := []scanstore.File{
		{Path: "a.go", Lang: "go", Disposition: "audited", KillRate: ptr(0.5), Survivors: 1, Gradable: true, Evidence: "proven"},
		{Path: "b.go", Lang: "go", Disposition: "rejected", Reason: "no-paired-test", Evidence: "paired"},
		{Path: "c.md", Disposition: "rejected", Reason: "no-language", Evidence: "paired"},
		{Path: "d.go", Lang: "go", Disposition: "rejected", Reason: "not-selected", PreflightState: "not-executed", Evidence: "coverage"},
		{Path: "e.py", Lang: "python", Disposition: "rejected", Reason: "executor-error", Evidence: "proven",
			Detail: "python toolchain unavailable — refusing to grade: pytest not importable"},
		{Path: "f.py", Lang: "python", Disposition: "audited", KillRate: ptr(0.46), Survivors: 13, Gradable: true,
			Evidence: "proven", TimedOut: true},
		{Path: "g.py", Lang: "python", Disposition: "audited", KillRate: ptr(0.457), Survivors: 19, Gradable: true,
			Evidence: "proven", TestWriterFailed: true},
		{Path: "h.py", Lang: "python", Disposition: "audited", KillRate: ptr(0.467), Survivors: 16, Gradable: true,
			Evidence: "proven", ProvenMissed: 7},
		{Path: "i.py", Lang: "python", Disposition: "audited", KillRate: ptr(0.5), Survivors: 4, Gradable: true,
			Evidence: "proven", PoolTestUnsound: true},
	}

	id, err := st.Record(context.Background(), scan, files)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id == 0 {
		t.Fatal("Record returned scan id 0; the header row must be addressable")
	}

	// Every file must come back, keyed to this scan, with its evidence intact.
	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != len(files) {
		t.Fatalf("read back %d files, wrote %d", len(got), len(files))
	}
	byPath := map[string]scanstore.File{}
	for _, f := range got {
		byPath[f.Path] = f
	}
	if d := byPath["d.go"]; d.PreflightState != "not-executed" || d.Evidence != "coverage" {
		t.Fatalf("d.go round-tripped as %+v; preflight state and evidence must survive", d)
	}
	if a := byPath["a.go"]; a.KillRate == nil || *a.KillRate != 0.5 {
		t.Fatalf("a.go kill rate round-tripped as %v, want 0.5", a.KillRate)
	}
	if b := byPath["b.go"]; b.KillRate != nil {
		t.Fatalf("b.go has a kill rate (%v); a rejected file was never scored and must read back NULL, not 0", *b.KillRate)
	}
	if e := byPath["e.py"]; e.Detail != "python toolchain unavailable — refusing to grade: pytest not importable" {
		t.Fatalf("e.py Detail round-tripped as %q; the executor's diagnosis must survive into the ledger, not just the reason code", e.Detail)
	}
	if f := byPath["f.py"]; !f.TimedOut {
		t.Fatalf("f.py TimedOut round-tripped as false, want true — a banked-but-unconverged score must be distinguishable in the ledger, not just by its reason column")
	}
	if a := byPath["a.go"]; a.TimedOut {
		t.Fatalf("a.go TimedOut round-tripped as true, want false — a cleanly-converged row must not carry the marker")
	}
	if g := byPath["g.py"]; !g.TestWriterFailed {
		t.Fatalf("g.py TestWriterFailed round-tripped as false, want true — a converged-but-unproven score (19 survivors, proven_missed unset) must be distinguishable in the ledger")
	}
	if a := byPath["a.go"]; a.TestWriterFailed {
		t.Fatalf("a.go TestWriterFailed round-tripped as true, want false — a cleanly-authored killing test must not carry the marker")
	}
	if h := byPath["h.py"]; h.ProvenMissed != 7 {
		t.Fatalf("h.py ProvenMissed round-tripped as %d, want 7 — corral's strongest claim (an execution-proven, catchable gap) must survive into the ledger", h.ProvenMissed)
	}
	if a := byPath["a.go"]; a.ProvenMissed != 0 {
		t.Fatalf("a.go ProvenMissed round-tripped as %d, want 0", a.ProvenMissed)
	}
	if i := byPath["i.py"]; !i.PoolTestUnsound {
		t.Fatalf("i.py PoolTestUnsound round-tripped as false, want true — a compiling-but-ungraded authored test must be distinguishable in the ledger, distinctly from TestWriterFailed")
	}
	if i := byPath["i.py"]; i.TestWriterFailed {
		t.Fatalf("i.py TestWriterFailed round-tripped as true, want false — its test DID compile, this is a different diagnosis")
	}
	if a := byPath["a.go"]; a.PoolTestUnsound {
		t.Fatalf("a.go PoolTestUnsound round-tripped as true, want false — a cleanly-graded row must not carry the marker")
	}
}

// TestZeroAuditScanKillRateStoresNullNotNaN pins the Task 1 review defect:
// DuckDB sorts NaN as larger than any other DOUBLE value, so a scan.KillRate
// of math.NaN() (reposcan.RepoReport.KillRate's deliberate value when
// Audited == 0) stored as a bare float64 would make a scan that measured
// NOTHING look like the ledger's BEST-scoring scan under MAX() or
// `ORDER BY kill_rate DESC LIMIT 1`. This proves the *float64 fix: a NaN
// scan reads back with a NULL kill_rate (Go nil), and a real score
// (0.9) still wins both aggregate forms with the NaN scan present in the
// same table.
func TestZeroAuditScanKillRateStoresNullNotNaN(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	nan := math.NaN()
	zeroAuditID, err := st.Record(context.Background(), scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "nothing-audited",
		TotalFiles: 1, Candidates: 1, Audited: 0, KillRate: &nan,
	}, nil)
	if err != nil {
		t.Fatalf("Record (zero-audit, NaN kill rate): %v", err)
	}

	good := 0.9
	goodID, err := st.Record(context.Background(), scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "real-score",
		TotalFiles: 1, Candidates: 1, Audited: 1, KillRate: &good,
	}, nil)
	if err != nil {
		t.Fatalf("Record (real score): %v", err)
	}

	// Read the NaN scan's row back directly: kill_rate must be SQL NULL
	// (Go nil), never a float that happens to be NaN — sql.Scan into
	// *float64 would itself error on a NULL if the column round-tripped
	// NaN as a literal value instead.
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var gotNull sql.NullFloat64
	if err := db.QueryRow(`SELECT kill_rate FROM scans WHERE id = ?`, zeroAuditID).Scan(&gotNull); err != nil {
		t.Fatalf("select zero-audit kill_rate: %v", err)
	}
	if gotNull.Valid {
		t.Fatalf("zero-audit scan's kill_rate = %v (valid), want SQL NULL", gotNull.Float64)
	}

	// The inversion this test exists to catch: with the NaN scan sitting in
	// the same table, MAX(kill_rate) must still surface the REAL score, not
	// NaN displacing it.
	var maxRate sql.NullFloat64
	if err := db.QueryRow(`SELECT MAX(kill_rate) FROM scans WHERE id IN (?, ?)`, zeroAuditID, goodID).Scan(&maxRate); err != nil {
		t.Fatalf("select MAX(kill_rate): %v", err)
	}
	if !maxRate.Valid || maxRate.Float64 != good {
		t.Fatalf("MAX(kill_rate) = %v, want the real score %v — a NaN row must not displace it", maxRate, good)
	}

	// And ORDER BY kill_rate DESC LIMIT 1 must surface the real-score scan
	// FIRST, never the never-measured one.
	var topCommit string
	if err := db.QueryRow(`SELECT commit FROM scans WHERE id IN (?, ?) ORDER BY kill_rate DESC LIMIT 1`, zeroAuditID, goodID).Scan(&topCommit); err != nil {
		t.Fatalf("select ORDER BY kill_rate DESC LIMIT 1: %v", err)
	}
	if topCommit != "real-score" {
		t.Fatalf("ORDER BY kill_rate DESC LIMIT 1 surfaced commit %q, want %q (the never-measured scan must not rank first)", topCommit, "real-score")
	}
}

// TestOpenMigrationIsIdempotent mirrors migrateBugcatchObservations'
// contract: opening an already-current store must run zero ALTERs and must
// not error, and opening a store that predates a later column must add
// exactly that column. A migration that silently swallowed every ALTER
// error would make a genuinely broken migration indistinguishable from an
// already-applied one — this test is what would catch that.
func TestOpenMigrationIsIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening an already-current store must be a clean no-op.
	st2, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (2nd, already current): %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A store created before a later column existed: build the original
	// table shape by hand, then confirm Open adds the missing column
	// without erroring.
	dsn2 := filepath.Join(t.TempDir(), "legacy.duckdb")
	db, err := sql.Open("duckdb", dsn2)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE scan_files (
		scan_id BIGINT, path VARCHAR, lang VARCHAR,
		disposition VARCHAR, reason VARCHAR,
		kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	st3, err := scanstore.Open(dsn2)
	if err != nil {
		t.Fatalf("Open (legacy, missing columns): %v", err)
	}
	defer st3.Close()

	scan := scanstore.Scan{Owner: "local", Repo: "demo", Commit: "abc123"}
	files := []scanstore.File{
		{Path: "a.go", Lang: "go", Disposition: "audited", KillRate: ptr(1.0), Evidence: "proven", PreflightState: "ran"},
	}
	id, err := st3.Record(context.Background(), scan, files)
	if err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	got, err := st3.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan after migration: %v", err)
	}
	if len(got) != 1 || got[0].PreflightState != "ran" || got[0].Evidence != "proven" {
		t.Fatalf("migrated columns did not round-trip: %+v", got)
	}
}

// TestProvenEvidenceRoundTrips pins that the evidence behind ProvenMissed
// actually survives a write/read cycle — including through the legacy-store
// migration path, since an existing ledger must gain the columns rather than
// silently dropping the evidence written into it.
//
// This exists because the reader is where this class of bug hides: the INSERT
// can be perfectly correct while SELECT never asks for the column, so the data
// is written, unreadable, and nobody notices until someone needs it. That is
// the same "computed, then discarded before reaching anyone" shape that has
// bitten this codebase repeatedly — here it would have discarded the evidence
// for corral's strongest claim.
func TestProvenEvidenceRoundTrips(t *testing.T) {
	// A legacy store: built with the ORIGINAL bare column set, so Open must
	// migrate it before any of this can round-trip.
	dsn := filepath.Join(t.TempDir(), "legacy-evidence.duckdb")
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE scan_files (
		scan_id BIGINT, path VARCHAR, lang VARCHAR,
		disposition VARCHAR, reason VARCHAR,
		kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (legacy): %v", err)
	}
	defer st.Close()

	const authored = "def test_corral_kills_it():\n    assert False\n"
	id, err := st.Record(context.Background(), scanstore.Scan{Owner: "local", Repo: "flask"}, []scanstore.File{
		// A PROVEN row: three survivors, two of them killed by name.
		{
			Path: "src/flask/cli.py", Lang: "python", Disposition: "audited",
			KillRate: ptr(0.48), Survivors: 3, Evidence: "proven",
			ProvenMissed: 2, ProvenMutantIDs: "m1,m3", AuthoredTest: authored,
		},
		// A TRIED-AND-MISSED row: a real authored test, zero kills. The
		// authored source must still be retained — that is the whole point.
		{
			Path: "src/flask/app.py", Lang: "python", Disposition: "audited",
			KillRate: ptr(0.66), Survivors: 10, Evidence: "proven",
			ProvenMissed: 0, ProvenMutantIDs: "", AuthoredTest: authored,
		},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].ProvenMutantIDs != "m1,m3" {
		t.Errorf("proven row ids = %q, want %q", got[0].ProvenMutantIDs, "m1,m3")
	}
	if got[0].AuthoredTest != authored {
		t.Errorf("proven row authored test did not round-trip: %q", got[0].AuthoredTest)
	}
	// The case that motivated the columns: 0 proven, but the attempt is still
	// on the record and inspectable without paying for another audit.
	if got[1].ProvenMissed != 0 || got[1].ProvenMutantIDs != "" {
		t.Errorf("tried-and-missed row = (%d, %q), want (0, \"\")", got[1].ProvenMissed, got[1].ProvenMutantIDs)
	}
	if got[1].AuthoredTest != authored {
		t.Errorf("tried-and-missed row MUST retain the authored test — that is the whole point; got %q", got[1].AuthoredTest)
	}
}

// TestScansListsHeadersNewestFirst pins the read side of the ledger. Until
// this existed the store was WRITE-ONLY in practice: `certify --repo --record`
// wrote scans and per-file dispositions, and nothing could read them back
// without a duckdb CLI (absent from the production host) or a hand-rolled Go
// program. A record nobody can query is not a record — it is a cost.
func TestScansListsHeadersNewestFirst(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	for _, s := range []scanstore.Scan{
		{Owner: "local", Repo: "flask", Commit: "aaa", Substrate: "workspace", Audited: 1, KillRate: ptr(0.48)},
		{Owner: "local", Repo: "gin", Commit: "bbb", Substrate: "jail", Audited: 2, KillRate: ptr(0.9)},
	} {
		if _, err := st.Record(context.Background(), s, nil); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, err := st.Scans(context.Background(), 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d scans, want 2", len(got))
	}
	// Newest first: the most recent scan is what an operator asks about.
	if got[0].Repo != "gin" || got[1].Repo != "flask" {
		t.Fatalf("order = [%s %s], want [gin flask] (newest first)", got[0].Repo, got[1].Repo)
	}
	if got[0].ID == 0 || got[0].ID == got[1].ID {
		t.Fatalf("scan ids must be real and distinct, got %d and %d", got[0].ID, got[1].ID)
	}
	if got[0].Substrate != "jail" || got[0].Audited != 2 {
		t.Fatalf("provenance did not round-trip: %+v", got[0])
	}
	// A never-measured scan must read back as NULL, never 0.0 — the same
	// NaN/NULL discipline Scan.KillRate's own doc exists for.
	if got[0].KillRate == nil || *got[0].KillRate != 0.9 {
		t.Fatalf("kill rate did not round-trip: %+v", got[0].KillRate)
	}

	// limit is honoured, so an operator can ask for just the last scan.
	one, err := st.Scans(context.Background(), 1)
	if err != nil {
		t.Fatalf("Scans(limit=1): %v", err)
	}
	if len(one) != 1 || one[0].Repo != "gin" {
		t.Fatalf("limit=1 returned %+v, want just the newest (gin)", one)
	}
}

// TestScansNullKillRateReadsBackNil is the NaN/NULL trap this store already
// documents on the write side, now asserted on the read side: a scan that
// audited nothing must come back nil, not 0.0 — a stored 0.0 would later read
// as "this scan scored terribly" about a scan that never graded anything.
func TestScansNullKillRateReadsBackNil(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if _, err := st.Record(context.Background(), scanstore.Scan{Owner: "local", Repo: "empty", Audited: 0}, nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := st.Scans(context.Background(), 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d scans, want 1", len(got))
	}
	if got[0].KillRate != nil {
		t.Fatalf("a scan that audited nothing must read back NULL, got %v", *got[0].KillRate)
	}
}
