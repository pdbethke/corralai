// SPDX-License-Identifier: Elastic-2.0

package scanstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/scanstore"
)

func i64(v int64) *int64 { return &v }

func iptr(v int) *int   { return &v }
func bptr(v bool) *bool { return &v }

// TestLedgerRecordsEveryGrain is the whole of Task 2's local half in one
// assertion: the ledger holds a scan at FIVE grains (scan, file, mutant,
// model call, event), and everything written at each grain reads back
// unchanged. The columns this test names are created NULL/empty here and
// filled by later tasks — the point of pinning them now is that the SHAPE
// exists before anything measures into it, so a later task adds a
// measurement rather than a column plus a measurement plus a migration.
func TestLedgerRecordsEveryGrain(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "grains.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	scan := scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "abc123",
		Substrate: "workspace", EngineVersion: "v9.9.9", ModelSet: "a=b",
		CorralVersion: "v9.9.9", Host: "box-1", Cores: 24, TreesRequested: 6,
		DiffBase: "main", TotalFiles: 3, Candidates: 2, Audited: 1,
		KillRate: ptr(0.5), TotalMillis: 61000,
		InputTokens: 1200, OutputTokens: 340, ModelCalls: 7,
		SourcePushed: true, StatementSHA256: "deadbeef",
		StartedAt:  time.Unix(1700000000, 0).UTC(),
		FinishedAt: time.Unix(1700000061, 0).UTC(),
	}
	files := []scanstore.File{
		{
			Path: "pkg/a.go", Lang: "go", Disposition: "audited",
			KillRate: ptr(0.5), Survivors: 2, Gradable: true, Evidence: "proven",
			ParentSHA256:    "aaaa",
			MutantsGraded:   8,
			MutantsInvalid:  1,
			MutantsTimedOut: iptr(2),
			SelectionMillis: i64(11), GenerationMillis: i64(22), PoolMillis: i64(33),
			DevPassMillis: i64(44), AuthoredPassMillis: i64(55), CriticMillis: i64(66),
			TotalMillis:        i64(231),
			MutantMillisMedian: i64(7), MutantMillisMax: i64(19),
			ChallengerJaccard: ptr(0.42), ChallengerKappa: ptr(0.13),
			ChallengerSufficient: bptr(true),
			GoalsDerived:         3,
			PerMutant:            true,
			TestsPerMutantMin:    iptr(3),
			TestsPerMutantMedian: iptr(5),
			TestsPerMutantMax:    iptr(41),
		},
		{
			Path: "pkg/b.go", Lang: "go", Disposition: "rejected",
			Reason: "no-paired-test", Evidence: "paired",
		},
	}

	id, err := st.Record(ctx, scan, files)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	gotFiles, err := st.FilesForScan(ctx, id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if !reflect.DeepEqual(gotFiles, files) {
		t.Errorf("scan_files did not round-trip.\n got: %+v\nwant: %+v", gotFiles, files)
	}

	// The scan header's own new columns.
	headers, err := st.Scans(ctx, 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	if len(headers) != 1 {
		t.Fatalf("Scans returned %d headers, want 1", len(headers))
	}
	if !reflect.DeepEqual(headers[0].Scan, scan) {
		t.Errorf("scans header did not round-trip.\n got: %+v\nwant: %+v", headers[0].Scan, scan)
	}

	mutants := []scanstore.Mutant{
		{
			ScanID: id, Path: "pkg/a.go", MutantID: "m1", Outcome: "killed",
			ParentSHA256: "aaaa", TestsRun: 4, SelectionRule: "lines",
			DurationMillis: i64(120), KilledBy: "TestFoo", SpanStart: 41, SpanEnd: 43,
		},
		{
			ScanID: id, Path: "pkg/a.go", MutantID: "m2", Outcome: "survived",
			ParentSHA256: "aaaa", Proven: true, ProvenByAuthoredAlone: true,
			TestsRun: 4, SelectionRule: "lines", DurationMillis: i64(90),
			SpanStart: 7, SpanEnd: 7,
		},
	}
	if err := st.RecordMutants(ctx, mutants); err != nil {
		t.Fatalf("RecordMutants: %v", err)
	}
	gotMutants, err := st.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan: %v", err)
	}
	if !reflect.DeepEqual(gotMutants, mutants) {
		t.Errorf("scan_mutants did not round-trip.\n got: %+v\nwant: %+v", gotMutants, mutants)
	}

	// Retries is nullable: one row here carries a MEASURED count (proving the
	// plumbing round-trips a non-nil value, should a backend ever report
	// one), the other carries nil — the value every producer in this
	// codebase actually sets today, since nothing has a retry loop to
	// observe. A stored 0 would read as "measured: none happened"; nil must
	// read back as SQL NULL, not 0.
	calls := []scanstore.ModelCall{
		{ScanID: id, Path: "pkg/a.go", Role: "mutant-generator", Model: "m-1",
			Calls: 3, Retries: iptr(1), InputTokens: 900, OutputTokens: 210, WallMillis: 4100},
		// CachedInputTokens is the same nullable discipline for a DIFFERENT
		// cause: the writer seat sends a byte-identical prefix on every one
		// of a file's per-survivor calls, so a caching provider reports how
		// much of the prompt it reused. A provider that says nothing has
		// reported NOTHING, not a miss — the mutant-generator row above
		// leaves it nil and must read back as SQL NULL, never 0.
		{ScanID: id, Path: "pkg/a.go", Role: "test-writer", Model: "w-1",
			Calls: 4, Retries: nil, InputTokens: 300, OutputTokens: 130,
			CachedInputTokens: i64(1200), WallMillis: 2200},
	}
	if err := st.RecordModelCalls(ctx, calls); err != nil {
		t.Fatalf("RecordModelCalls: %v", err)
	}
	gotCalls, err := st.ModelCallsForScan(ctx, id)
	if err != nil {
		t.Fatalf("ModelCallsForScan: %v", err)
	}
	if !reflect.DeepEqual(gotCalls, calls) {
		t.Errorf("scan_model_calls did not round-trip.\n got: %+v\nwant: %+v", gotCalls, calls)
	}
	for _, c := range gotCalls {
		if c.Role == "test-writer" && c.Retries != nil {
			t.Errorf("test-writer's unmeasured retries read back as %d, want SQL NULL (nil)", *c.Retries)
		}
		if c.Role == "mutant-generator" && (c.Retries == nil || *c.Retries != 1) {
			t.Errorf("mutant-generator's retries = %v, want a measured 1", c.Retries)
		}
		if c.Role == "mutant-generator" && c.CachedInputTokens != nil {
			t.Errorf("mutant-generator's unreported cached tokens read back as %d, want SQL NULL (nil)", *c.CachedInputTokens)
		}
		if c.Role == "test-writer" && (c.CachedInputTokens == nil || *c.CachedInputTokens != 1200) {
			t.Errorf("test-writer's cached tokens = %v, want a measured 1200", c.CachedInputTokens)
		}
	}

	ts := time.Unix(1700000010, 0).UTC()
	events := []scanstore.Event{
		{ScanID: id, Path: "pkg/a.go", Seq: 1, TS: ts, Kind: "phase-start",
			Actor: "driver", Subject: "generation", Detail: `{"shards":2}`},
		{ScanID: id, Path: "pkg/a.go", Seq: 2, TS: ts.Add(time.Second), Kind: "model-call",
			Actor: "mutant-generator", Subject: "shard-0", Model: "m-1",
			DurationMillis: i64(1500), Detail: `{"retries":0}`},
		{ScanID: id, Path: "pkg/a.go", Seq: 3, TS: ts.Add(2 * time.Second), Kind: "phase-end",
			Actor: "driver", Subject: "generation", DurationMillis: i64(3000)},
	}
	if err := st.RecordEvents(ctx, events); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}
	gotEvents, err := st.EventsForScan(ctx, id)
	if err != nil {
		t.Fatalf("EventsForScan: %v", err)
	}
	if !reflect.DeepEqual(gotEvents, events) {
		t.Errorf("scan_events did not round-trip.\n got: %+v\nwant: %+v", gotEvents, events)
	}
}

// legacySharedDirsDDL is the ledger's shape at HEAD before this change — the
// three CREATE TABLEs verbatim, copied here as a string so the migration path
// is exercised against the real historical shape rather than a paraphrase of
// it. A ledger at ANY prior shape has to open, and this is the newest prior
// shape (the one an operator running yesterday's corral actually has).
const legacySharedDirsDDL = `
CREATE TABLE scans (
	id BIGINT PRIMARY KEY, ts TIMESTAMP,
	owner VARCHAR, repo VARCHAR, commit VARCHAR,
	substrate VARCHAR, engine_version VARCHAR, model_set VARCHAR,
	top INTEGER, all_candidates BOOLEAN, diff_base VARCHAR,
	total_files INTEGER, candidates INTEGER, audited INTEGER,
	kill_rate DOUBLE, cache_hits INTEGER,
	preflight_ran BOOLEAN, preflight_note VARCHAR,
	started_at TIMESTAMP, finished_at TIMESTAMP
);
CREATE TABLE scan_files (
	scan_id BIGINT, path VARCHAR, lang VARCHAR,
	disposition VARCHAR CHECK (disposition IN ('audited', 'rejected')),
	reason VARCHAR,
	kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN,
	preflight_state VARCHAR CHECK (preflight_state IN ('', 'executed', 'not-executed')),
	evidence VARCHAR CHECK (evidence IN ('', 'paired', 'coverage', 'proven')),
	detail VARCHAR,
	timed_out BOOLEAN,
	test_writer_failed BOOLEAN,
	proven_missed INTEGER,
	pool_test_unsound BOOLEAN,
	proven_mutant_ids VARCHAR,
	authored_test VARCHAR,
	cache_key VARCHAR,
	verdict_json VARCHAR,
	computed_at TIMESTAMP,
	models_by_role VARCHAR,
	mutants_total INTEGER,
	regions_total INTEGER,
	regions_probed INTEGER,
	dropped_regions VARCHAR,
	vacuous_findings INTEGER,
	status VARCHAR,
	authored_test_not_collected BOOLEAN,
	baseline_failed BOOLEAN,
	cache_hit BOOLEAN,
	reused_from_scan_id BIGINT,
	suite_baseline_ms BIGINT,
	test_selection VARCHAR,
	selected_tests INTEGER,
	suite_tests INTEGER,
	selection_fallback VARCHAR,
	uncovered BOOLEAN,
	mutants_from VARCHAR,
	trees INTEGER,
	concurrency_note VARCHAR,
	shared_dirs VARCHAR
);
CREATE TABLE scan_mutants (
	scan_id BIGINT, path VARCHAR, mutant_id VARCHAR,
	outcome VARCHAR CHECK (outcome IN ('killed', 'survived')),
	parent_sha256 VARCHAR,
	proven BOOLEAN,
	tests_run INTEGER,
	selection_rule VARCHAR
);
CREATE SEQUENCE scans_id START 1;
`

// TestLedgerMigratesFromTheSharedDirsShape opens a ledger built at the
// pre-plan shape, with a row already in each table, and proves that (a) every
// new column is added rather than the open failing, (b) the two new tables
// come into existence, and (c) the pre-existing row still reads back — with
// the new fields at their honest zero/NULL, never a fabricated value.
func TestLedgerMigratesFromTheSharedDirsShape(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-grains.duckdb")
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(legacySharedDirsDDL); err != nil {
		t.Fatalf("create legacy ledger: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO scans (id, ts, owner, repo, commit, audited)
		VALUES (nextval('scans_id'), now(), 'local', 'demo', 'abc', 1)`); err != nil {
		t.Fatalf("insert legacy scan: %v", err)
	}
	// reason/preflight_state are written as '' rather than left NULL: that is
	// what the store itself writes for an audited row, and FilesForScan has
	// always scanned those two into plain strings.
	if _, err := db.Exec(`INSERT INTO scan_files (scan_id, path, lang, disposition, reason, preflight_state, kill_rate, survivors, gradable, evidence)
		VALUES (1, 'pkg/old.go', 'go', 'audited', '', '', 0.5, 2, true, 'proven')`); err != nil {
		t.Fatalf("insert legacy file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO scan_mutants (scan_id, path, mutant_id, outcome, parent_sha256, proven)
		VALUES (1, 'pkg/old.go', 'm1', 'survived', 'aaaa', false)`); err != nil {
		t.Fatalf("insert legacy mutant: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (legacy): %v", err)
	}
	ctx := context.Background()

	gotFiles, err := st.FilesForScan(ctx, 1)
	if err != nil {
		t.Fatalf("FilesForScan on the migrated ledger: %v", err)
	}
	if len(gotFiles) != 1 {
		t.Fatalf("FilesForScan returned %d rows, want 1", len(gotFiles))
	}
	f := gotFiles[0]
	if f.ParentSHA256 != "" || f.MutantsGraded != 0 || f.GoalsDerived != 0 || f.MutantsTimedOut != nil {
		t.Errorf("a pre-migration row must read back with EMPTY new fields, got %+v", f)
	}
	for name, v := range map[string]*int64{
		"SelectionMillis": f.SelectionMillis, "GenerationMillis": f.GenerationMillis,
		"PoolMillis": f.PoolMillis, "DevPassMillis": f.DevPassMillis,
		"AuthoredPassMillis": f.AuthoredPassMillis, "CriticMillis": f.CriticMillis,
		"TotalMillis": f.TotalMillis, "MutantMillisMedian": f.MutantMillisMedian,
		"MutantMillisMax": f.MutantMillisMax,
	} {
		if v != nil {
			t.Errorf("%s on a pre-migration row must be nil (never measured), got %d", name, *v)
		}
	}
	if f.ChallengerJaccard != nil || f.ChallengerKappa != nil || f.ChallengerSufficient != nil {
		t.Errorf("challenger columns on a pre-migration row must be nil, got %+v", f)
	}

	gotMutants, err := st.MutantsForScan(ctx, 1)
	if err != nil {
		t.Fatalf("MutantsForScan on the migrated ledger: %v", err)
	}
	if len(gotMutants) != 1 || gotMutants[0].DurationMillis != nil || gotMutants[0].KilledBy != "" {
		t.Errorf("a pre-migration mutant row must read back with empty new fields, got %+v", gotMutants)
	}

	// The two new tables must EXIST on a ledger that predates them, and be
	// writable — a migration that only adds columns would leave these absent
	// and every later write would fail against an operator's existing file.
	if err := st.RecordModelCalls(ctx, []scanstore.ModelCall{{ScanID: 1, Path: "pkg/old.go", Role: "test-writer", Model: "w"}}); err != nil {
		t.Fatalf("RecordModelCalls on the migrated ledger: %v", err)
	}
	if err := st.RecordEvents(ctx, []scanstore.Event{{ScanID: 1, Path: "pkg/old.go", Seq: 1, TS: time.Unix(1700000000, 0).UTC(), Kind: "phase-start"}}); err != nil {
		t.Fatalf("RecordEvents on the migrated ledger: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open (verify): %v", err)
	}
	defer db2.Close()
	for _, want := range []struct{ table, column string }{
		{"scans", "corral_version"}, {"scans", "host"}, {"scans", "cores"},
		{"scans", "trees_requested"}, {"scans", "total_ms"}, {"scans", "input_tokens"},
		{"scans", "output_tokens"}, {"scans", "model_calls"}, {"scans", "source_pushed"},
		{"scans", "statement_sha256"},
		{"scan_files", "parent_sha256"}, {"scan_files", "mutants_graded"},
		{"scan_files", "mutants_invalid"}, {"scan_files", "mutants_timed_out"},
		{"scan_files", "selection_ms"}, {"scan_files", "generation_ms"},
		{"scan_files", "pool_ms"}, {"scan_files", "dev_pass_ms"},
		{"scan_files", "authored_pass_ms"}, {"scan_files", "critic_ms"},
		{"scan_files", "total_ms"}, {"scan_files", "mutant_ms_median"},
		{"scan_files", "mutant_ms_max"}, {"scan_files", "challenger_jaccard"},
		{"scan_files", "challenger_kappa"}, {"scan_files", "challenger_sufficient"},
		{"scan_files", "goals_derived"}, {"scan_files", "per_mutant"},
		{"scan_files", "tests_per_mutant_min"}, {"scan_files", "tests_per_mutant_median"},
		{"scan_files", "tests_per_mutant_max"},
		{"scan_mutants", "duration_ms"}, {"scan_mutants", "killed_by"},
		{"scan_mutants", "span_start"}, {"scan_mutants", "span_end"},
		{"scan_mutants", "proven_by_authored_alone"},
		{"scan_model_calls", "wall_ms"}, {"scan_events", "detail"},
	} {
		var n int
		if err := db2.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?`,
			want.table, want.column).Scan(&n); err != nil {
			t.Fatalf("probe %s.%s: %v", want.table, want.column, err)
		}
		if n != 1 {
			t.Errorf("column %s.%s is missing after the migration", want.table, want.column)
		}
	}
}

// TestUnmeasuredMillisAreNull pins the rule the whole design turns on: a
// duration nothing measured is SQL NULL, never 0. A stored zero is a NUMBER
// — a later cost-model query averages it, ranks on it and reports a phase
// that took no time — and the columns exist precisely so the cost model can
// be a query.
func TestUnmeasuredMillisAreNull(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "nullms.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	id, err := st.Record(ctx, scanstore.Scan{Owner: "local", Repo: "demo"},
		[]scanstore.File{{Path: "pkg/a.go", Lang: "go", Disposition: "rejected", Reason: "not-selected"}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := st.RecordMutants(ctx, []scanstore.Mutant{
		{ScanID: id, Path: "pkg/a.go", MutantID: "m1", Outcome: "survived"},
	}); err != nil {
		t.Fatalf("RecordMutants: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	for _, col := range []string{
		"selection_ms", "generation_ms", "pool_ms", "dev_pass_ms",
		"authored_pass_ms", "critic_ms", "total_ms", "mutant_ms_median", "mutant_ms_max",
		"challenger_jaccard", "challenger_kappa", "challenger_sufficient",
		"tests_per_mutant_min", "tests_per_mutant_median", "tests_per_mutant_max",
		// mutants_timed_out has NO producer yet (no verdict field counts
		// timed-out mutants), and a stored 0 would read as the positive claim
		// "none timed out".
		"mutants_timed_out",
	} {
		var isNull bool
		if err := db.QueryRow(`SELECT ` + col + ` IS NULL FROM scan_files WHERE path = 'pkg/a.go'`).Scan(&isNull); err != nil {
			t.Fatalf("probe scan_files.%s: %v", col, err)
		}
		if !isNull {
			t.Errorf("scan_files.%s must be SQL NULL when nothing measured it", col)
		}
	}
	// span_start/span_end have no producer at all today (advpool.MutantRef
	// carries no span), so every row's is unrecorded — and a 0 in a column of
	// 1-based line numbers is a line that does not exist.
	for _, col := range []string{"duration_ms", "span_start", "span_end"} {
		var isNull bool
		if err := db.QueryRow(`SELECT ` + col + ` IS NULL FROM scan_mutants WHERE mutant_id = 'm1'`).Scan(&isNull); err != nil {
			t.Fatalf("probe scan_mutants.%s: %v", col, err)
		}
		if !isNull {
			t.Errorf("scan_mutants.%s must be SQL NULL when nothing recorded it", col)
		}
	}
}

// TestStatementSHAIsStampedOnTheScanRow closes a loop the ledger could not:
// the statement is written AFTER the scan row (it has to name the scan id),
// so scans.statement_sha256 was written empty on every run that used
// --attest and stayed empty forever. The warehouse row carried the hash and
// the local ledger did not, which is precisely the asymmetry the column was
// added to remove.
func TestStatementSHAIsStampedOnTheScanRow(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "stamp.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	id, err := st.Record(ctx, scanstore.Scan{Owner: "local", Repo: "demo"}, nil)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := st.SetStatementSHA256(ctx, id, "deadbeef"); err != nil {
		t.Fatalf("SetStatementSHA256: %v", err)
	}
	headers, err := st.Scans(ctx, 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	if len(headers) != 1 || headers[0].StatementSHA256 != "deadbeef" {
		t.Fatalf("statement_sha256 = %q, want deadbeef", headers[0].StatementSHA256)
	}
}
