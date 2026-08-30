// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

func f64(v float64) *float64 { return &v }
func ms(v int64) *int64      { return &v }
func ip(v int) *int          { return &v }

// openWarehouse opens the pushed file for reading. A push closes its own
// handle, so a reader can take DuckDB's single-writer lock afterwards.
func openWarehouse(t *testing.T, target string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open warehouse: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func sampleBundle() Bundle {
	return Bundle{
		Scan: ScanRow{
			Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Commit: "abc",
			CorralVersion: "v9.9.9", Substrate: "workspace", Host: "box-1",
			Cores: 24, TreesRequested: 6, DiffBase: "main",
			Candidates: 2, Audited: 1, Passed: true,
			TotalMillis: ms(61000), InputTokens: 1200, OutputTokens: 340, ModelCalls: 7,
		},
		Files: []Row{
			{
				Repo: "o/r", Commit: "abc", RunURL: "https://ci/1", ScanID: 7,
				Path: "pkg/a.go", Lang: "go", Disposition: "audited",
				KillRate: f64(0.5), Survivors: 2, ProvenMissed: 1,
				ParentSHA256: "aaaa", Evidence: "proven", Status: "certified",
				MutantsGraded: 8, MutantsInvalid: 1,
				GoalsDerived: 3, AuthoredTest: "def test_x(): pass",
				VerdictJSON: `{"dev_kill_rate":0.5}`,
			},
			{
				Repo: "o/r", Commit: "abc", RunURL: "https://ci/1", ScanID: 7,
				Path: "pkg/b.go", Lang: "go", Disposition: "rejected",
				Reason: "no-paired-test", Evidence: "paired",
			},
		},
		Mutants: []MutantRow{
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go",
				MutantID: "m1", ParentSHA256: "aaaa", Outcome: "killed",
				TestsRun: 4, DurationMillis: ms(120), KilledBy: "TestFoo",
				Code: "return !ok"},
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go",
				MutantID: "m2", ParentSHA256: "aaaa", Outcome: "survived",
				Proven: true, ProvenByAuthoredAlone: true, Code: "x := 1"},
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go",
				MutantID: "m3", ParentSHA256: "aaaa", Outcome: "invalid",
				InvalidReason: "did-not-compile", Code: "??"},
		},
		Calls: []ModelCallRow{
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go",
				Role: "mutant-generator", Model: "m-1", Calls: 3, Retries: ip(1),
				InputTokens: 900, OutputTokens: 210, WallMillis: 4100},
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go",
				Role: "test-writer", Model: "w-1", Calls: 4,
				InputTokens: 300, OutputTokens: 130, WallMillis: 2200},
		},
		Events: []EventRow{
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go", Seq: 1, Kind: "phase-start", Actor: "driver", Subject: "generation"},
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go", Seq: 2, Kind: "model-call", Actor: "mutant-generator", Model: "m-1", DurationMillis: ms(1500), Detail: `{"retries":0}`},
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go", Seq: 3, Kind: "phase-end", Actor: "driver", Subject: "generation", DurationMillis: ms(3000)},
			{Repo: "o/r", RunURL: "https://ci/1", ScanID: 7, Path: "pkg/a.go", Seq: 4, Kind: "dev-pass", Actor: "driver", DurationMillis: ms(9000)},
		},
	}
}

// TestPushBundleWritesFiveTablesInOneTransaction is the whole contract: five
// grains land together or not at all, and the seal view exists on the
// operator's own file so "what is the repo's current state" is a SELECT, not
// a re-audit.
func TestPushBundleWritesFiveTablesInOneTransaction(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")

	got, err := PushBundle(target, sampleBundle())
	if err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	want := Counts{Scans: 1, Files: 2, Mutants: 3, Calls: 2, Events: 4}
	if got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}

	db := openWarehouse(t, target)
	for table, n := range map[string]int{
		"corral_scans": 1, "corral_audits": 2, "corral_mutants": 3,
		"corral_model_calls": 2, "corral_events": 4,
	} {
		if c := countRows(t, db, table); c != n {
			t.Errorf("%s has %d rows, want %d", table, c, n)
		}
	}

	// Every row carries schema_version 2 — the marker that says this row has
	// the timing and usage columns at all.
	for _, table := range []string{"corral_scans", "corral_audits", "corral_mutants", "corral_model_calls", "corral_events"} {
		var bad int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE schema_version IS DISTINCT FROM 2`).Scan(&bad); err != nil {
			t.Fatalf("schema_version on %s: %v", table, err)
		}
		if bad != 0 {
			t.Errorf("%s has %d row(s) not at schema_version 2", table, bad)
		}
	}

	// Nothing produces a mutant span or a timed-out count yet, and a 0 in
	// either column is a positive claim: line 0 does not exist, and "0
	// mutants timed out" is a measurement nobody made.
	for _, probe := range []struct{ table, col string }{
		{"corral_mutants", "span_start"}, {"corral_mutants", "span_end"},
		{"corral_audits", "mutants_timed_out"},
	} {
		var notNull int
		if err := db.QueryRow(`SELECT count(*) FROM ` + probe.table + ` WHERE ` + probe.col + ` IS NOT NULL`).Scan(&notNull); err != nil {
			t.Fatalf("probe %s.%s: %v", probe.table, probe.col, err)
		}
		if notNull != 0 {
			t.Errorf("%s.%s has %d non-NULL row(s), but nothing produces that value", probe.table, probe.col, notNull)
		}
	}

	// The seal: the latest kill-rate-bearing row per (repo, path). The
	// rejected file has no kill rate and must not appear — a seal that lists
	// files nothing graded is a coverage claim nobody earned.
	var sealPath string
	if err := db.QueryRow(`SELECT path FROM corral_seal`).Scan(&sealPath); err != nil {
		t.Fatalf("corral_seal: %v", err)
	}
	if sealPath != "pkg/a.go" {
		t.Errorf("corral_seal names %q, want the audited file", sealPath)
	}

	// Rollback: a bundle whose mutant outcome is outside the CHECK must
	// leave ALL FIVE tables exactly where they were. A push that half-lands
	// is worse than one that fails, because the next reader sees a scan row
	// with no mutants and reports it as a scan that produced none.
	bad := sampleBundle()
	bad.Scan.ScanID = 8
	bad.Mutants[1].Outcome = "eaten"
	if _, err := PushBundle(target, bad); err == nil {
		t.Fatal("PushBundle must refuse an outcome outside the CHECK")
	}
	db2 := openWarehouse(t, target)
	for table, n := range map[string]int{
		"corral_scans": 1, "corral_audits": 2, "corral_mutants": 3,
		"corral_model_calls": 2, "corral_events": 4,
	} {
		if c := countRows(t, db2, table); c != n {
			t.Errorf("after the failed push %s has %d rows, want %d — the transaction did not roll back", table, c, n)
		}
	}
}

// TestModelCallRetriesIsNullNotZero pins the NULL-not-zero rule for the one
// nullable column on the money grain: sampleBundle's test-writer row never
// sets Retries (nil — the value every real producer sets today, since
// agentbackend has no retry loop to observe), and its mutant-generator row
// sets a MEASURED one. The warehouse column must tell them apart: NULL for
// the first, 1 for the second — never a stored 0 that a later query would
// read as "measured: zero retries".
func TestModelCallRetriesIsNullNotZero(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := PushBundle(target, sampleBundle()); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	db := openWarehouse(t, target)

	var nullCount int
	if err := db.QueryRow(`SELECT count(*) FROM corral_model_calls WHERE role = 'test-writer' AND retries IS NULL`).Scan(&nullCount); err != nil {
		t.Fatalf("query test-writer retries: %v", err)
	}
	if nullCount != 1 {
		t.Errorf("test-writer's unmeasured retries: %d row(s) read NULL, want 1", nullCount)
	}

	var measured sql.NullInt64
	if err := db.QueryRow(`SELECT retries FROM corral_model_calls WHERE role = 'mutant-generator'`).Scan(&measured); err != nil {
		t.Fatalf("query mutant-generator retries: %v", err)
	}
	if !measured.Valid || measured.Int64 != 1 {
		t.Errorf("mutant-generator retries = %+v, want a measured 1", measured)
	}
}

// The three shapes corral_audits has actually had in the wild, copied
// verbatim from this package's own history (f0ed714, 7e5af3e, f29cb3c). A
// warehouse an earlier corral created keeps its column set forever —
// CREATE TABLE IF NOT EXISTS is a no-op on it — so the ONLY thing standing
// between an operator's durable record and a hard INSERT failure on upgrade
// is that these migrate.
const warehouseV1DDL = `CREATE TABLE corral_audits (
  ts TIMESTAMPTZ NOT NULL, repo VARCHAR NOT NULL, commit_sha VARCHAR NOT NULL,
  path VARCHAR NOT NULL, lang VARCHAR, kill_rate DOUBLE, survivors INTEGER,
  proven_missed INTEGER, timed_out BOOLEAN, test_writer_failed BOOLEAN,
  pool_test_unsound BOOLEAN, audited INTEGER, candidates INTEGER,
  mutants_planted INTEGER, models_by_role VARCHAR, min_kill_rate DOUBLE,
  max_proven_missed INTEGER, passed BOOLEAN, statement_sha256 VARCHAR,
  run_url VARCHAR
);`

const warehouseV2SelectionDDL = warehouseV1DDL + `
ALTER TABLE corral_audits ADD COLUMN test_selection VARCHAR;
ALTER TABLE corral_audits ADD COLUMN selected_tests INTEGER;
ALTER TABLE corral_audits ADD COLUMN suite_tests INTEGER;
ALTER TABLE corral_audits ADD COLUMN selection_fallback VARCHAR;
ALTER TABLE corral_audits ADD COLUMN uncovered BOOLEAN;`

const warehouseV3PerMutantDDL = warehouseV2SelectionDDL + `
ALTER TABLE corral_audits ADD COLUMN per_mutant BOOLEAN;
ALTER TABLE corral_audits ADD COLUMN tests_per_mutant_min INTEGER;
ALTER TABLE corral_audits ADD COLUMN tests_per_mutant_median INTEGER;
ALTER TABLE corral_audits ADD COLUMN tests_per_mutant_max INTEGER;`

// The two shapes an operator running YESTERDAY's corral actually has on
// disk: 32 columns after the concurrency disclosure (37420d1) and 33 after
// Task 1's scan_id (0d95056). They matter more than the older three,
// because they are the ones a real upgrade will meet.
const warehouseV4ConcurrencyDDL = warehouseV3PerMutantDDL + `
ALTER TABLE corral_audits ADD COLUMN trees INTEGER;
ALTER TABLE corral_audits ADD COLUMN concurrency_note VARCHAR;
ALTER TABLE corral_audits ADD COLUMN shared_dirs VARCHAR;`

const warehouseV5ScanIDDDL = warehouseV4ConcurrencyDDL + `
ALTER TABLE corral_audits ADD COLUMN scan_id BIGINT;`

func TestPushBundleMigratesEveryPriorWarehouseShape(t *testing.T) {
	for name, ddl := range map[string]string{
		"original":    warehouseV1DDL,
		"selection":   warehouseV2SelectionDDL,
		"per-mutant":  warehouseV3PerMutantDDL,
		"concurrency": warehouseV4ConcurrencyDDL,
		"scan-id":     warehouseV5ScanIDDDL,
	} {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "legacy.duckdb")
			seed, err := sql.Open("duckdb", target)
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			if _, err := seed.Exec(ddl); err != nil {
				t.Fatalf("build the legacy warehouse: %v", err)
			}
			if _, err := seed.Exec(`INSERT INTO corral_audits (ts, repo, commit_sha, path, kill_rate)
				VALUES (now(), 'o/old', 'old', 'pkg/old.go', 0.9)`); err != nil {
				t.Fatalf("seed a legacy row: %v", err)
			}
			if err := seed.Close(); err != nil {
				t.Fatalf("close the legacy warehouse: %v", err)
			}

			if _, err := PushBundle(target, sampleBundle()); err != nil {
				t.Fatalf("PushBundle onto the %s warehouse: %v", name, err)
			}

			db := openWarehouse(t, target)
			// The old row survives, still readable, with the new columns NULL.
			var oldRate float64
			var oldSchema sql.NullInt64
			if err := db.QueryRow(`SELECT kill_rate, schema_version FROM corral_audits WHERE path = 'pkg/old.go'`).Scan(&oldRate, &oldSchema); err != nil {
				t.Fatalf("read the legacy row back: %v", err)
			}
			if oldRate != 0.9 {
				t.Errorf("the legacy row's kill_rate changed: %v", oldRate)
			}
			if oldSchema.Valid {
				t.Errorf("a pre-migration row must have schema_version NULL, got %d", oldSchema.Int64)
			}
			// And the full current column set is present.
			for _, col := range []string{
				"scan_id", "disposition", "parent_sha256", "mutants_graded",
				"selection_ms", "total_ms", "challenger_kappa", "verdict_json", "schema_version",
			} {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM duckdb_columns() WHERE table_name = 'corral_audits' AND column_name = ?`, col).Scan(&n); err != nil {
					t.Fatalf("probe corral_audits.%s: %v", col, err)
				}
				if n != 1 {
					t.Errorf("corral_audits.%s missing after the migration", col)
				}
			}
		})
	}
}

// TestSourceStaysHomeUnlessAsked is the custody rule, enforced by the writer
// rather than by the caller remembering to blank three fields: mutant code,
// the authored test and the verdict blob ARE the audited source, and they
// leave the box only when the operator says so.
func TestSourceStaysHomeUnlessAsked(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sourcePushed bool
	}{
		{"withheld", false},
		{"opted in", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "w.duckdb")
			b := sampleBundle()
			b.SourcePushed = tc.sourcePushed
			b.Scan.SourcePushed = tc.sourcePushed
			if _, err := PushBundle(target, b); err != nil {
				t.Fatalf("PushBundle: %v", err)
			}
			db := openWarehouse(t, target)

			var nulls int
			if err := db.QueryRow(`SELECT count(*) FROM corral_audits WHERE authored_test IS NOT NULL OR verdict_json IS NOT NULL`).Scan(&nulls); err != nil {
				t.Fatalf("probe corral_audits source columns: %v", err)
			}
			var code int
			if err := db.QueryRow(`SELECT count(*) FROM corral_mutants WHERE code IS NOT NULL`).Scan(&code); err != nil {
				t.Fatalf("probe corral_mutants.code: %v", err)
			}
			var pushed bool
			if err := db.QueryRow(`SELECT source_pushed FROM corral_scans`).Scan(&pushed); err != nil {
				t.Fatalf("probe corral_scans.source_pushed: %v", err)
			}
			if pushed != tc.sourcePushed {
				t.Errorf("corral_scans.source_pushed = %v, want %v", pushed, tc.sourcePushed)
			}
			if tc.sourcePushed {
				if nulls == 0 {
					t.Error("--push-source was given: the authored test and the verdict must be present")
				}
				if code != 3 {
					t.Errorf("--push-source was given: %d mutant(s) carry code, want 3", code)
				}
				return
			}
			if nulls != 0 {
				t.Errorf("%d audit row(s) carry source bytes without --push-source", nulls)
			}
			if code != 0 {
				t.Errorf("%d mutant row(s) carry code without --push-source", code)
			}
		})
	}
}

// TestPushRefusesMoreThanOneLink: `link` is variadic only so the pre-Link
// call shape keeps compiling. Two links is a caller bug — the second was
// silently dropped — and a silently dropped link is a row that claims a
// traceability it does not have.
func TestPushRefusesMoreThanOneLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := Push(target, []Row{{Repo: "o/r", Commit: "c", Path: "a.py"}},
		Link{ScanID: 1}, Link{ScanID: 2}); err == nil {
		t.Fatal("Push must refuse more than one Link rather than silently using the first")
	}
}
