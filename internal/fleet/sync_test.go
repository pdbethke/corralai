// SPDX-License-Identifier: Elastic-2.0

package fleet

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
	_ "modernc.org/sqlite"
)

// seedStores creates all four source DB files in dir and returns a populated SyncConfig.
func seedStores(t *testing.T, dir string) SyncConfig {
	t.Helper()
	// coord.audit
	coord := filepath.Join(dir, "coord.sqlite")
	mustSQLite(t, coord, `CREATE TABLE audit(id INTEGER PRIMARY KEY, ts REAL, agent_name TEXT, action TEXT, detail TEXT);
		INSERT INTO audit VALUES (1, 1.0, 'hawk', 'claim', 'task#1'), (2, 2.0, 'owl', 'done', 'task#1');`)
	// missions
	mission := filepath.Join(dir, "mission.sqlite")
	mustSQLite(t, mission, `CREATE TABLE missions(id INTEGER PRIMARY KEY, directive TEXT, status TEXT, sprint INTEGER,
		requires_review INTEGER, created_ts REAL, updated_ts REAL, repo TEXT, base TEXT, branch TEXT, pr_url TEXT,
		review_rounds INTEGER, review_watermark TEXT, review_parked INTEGER);
		INSERT INTO missions(id,directive,status,created_ts,updated_ts,repo,branch,pr_url,review_rounds)
		VALUES (5,'build calc','done',1.0,2.0,'https://github.com/o/r','corralai/m5','https://github.com/o/r/pull/7',0);`)
	// queue tasks
	queue := filepath.Join(dir, "queue.sqlite")
	mustSQLite(t, queue, `CREATE TABLE tasks(id INTEGER PRIMARY KEY, mission_id INTEGER, key TEXT, role TEXT, title TEXT,
		instruction TEXT, status TEXT, claimed_by TEXT, created_ts REAL, claimed_ts REAL, done_ts REAL, verify TEXT);
		INSERT INTO tasks(id,mission_id,key,role,title,instruction,status,claimed_by,created_ts,done_ts,verify)
		VALUES (10,5,'build#1','','build','SECRET-INSTRUCTION','done','hawk',1.0,2.0,'go build ./...');`)
	// telemetry (duckdb) — model and detail are now synced; seed with benign values.
	telem := filepath.Join(dir, "telem.duckdb")
	mustDuckDB(t, telem, `CREATE TABLE events(id BIGINT PRIMARY KEY, ts DOUBLE, mission_id BIGINT, kind VARCHAR,
		actor VARCHAR, subject VARCHAR, model VARCHAR, detail VARCHAR);
		INSERT INTO events VALUES (100, 3.0, 5, 'exec', 'hawk', 'go build', 'exec-model', 'exec-info');`)
	return SyncConfig{Coord: coord, Mission: mission, Queue: queue, Telemetry: telem}
}

func TestSyncCuratedTables(t *testing.T) {
	dir := t.TempDir()
	cfg := seedStores(t, dir)
	remote := filepath.Join(dir, "remote.duckdb")

	n, err := Sync(cfg, remote, "brainA")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected rows synced")
	}
	rows := readRemote(t, remote)
	// curated rows present, brain-tagged
	if rows["fleet_actions"] != 2 || rows["fleet_missions"] != 1 || rows["fleet_tasks"] != 1 || rows["fleet_telemetry"] != 1 {
		t.Fatalf("row counts: %+v", rows)
	}
	// classification: instruction must NOT cross into fleet_tasks
	assertNoColumnValue(t, remote, "fleet_tasks", "SECRET-INSTRUCTION")
	// idempotent: a second sync adds nothing
	n2, _ := Sync(cfg, remote, "brainA")
	if n2 != 0 {
		t.Fatalf("second sync should be a no-op, synced %d", n2)
	}
	// federation: a second brain writes alongside, distinguishable
	Sync(cfg, remote, "brainB")
	if brains := distinctBrains(t, remote, "fleet_missions"); brains != 2 {
		t.Fatalf("expected 2 brains federated, got %d", brains)
	}
}

func TestSyncPartialConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := seedStores(t, dir)
	cfg.Telemetry = "" // telemetry not configured
	remote := filepath.Join(dir, "remote.duckdb")
	if _, err := Sync(cfg, remote, "brainA"); err != nil {
		t.Fatal(err)
	}
	rows := readRemote(t, remote)
	if rows["fleet_missions"] != 1 {
		t.Fatal("other tables should still sync when telemetry is absent")
	}
	if _, ok := rows["fleet_telemetry"]; ok && rows["fleet_telemetry"] != 0 {
		t.Fatalf("telemetry unconfigured → no telemetry rows, got %d", rows["fleet_telemetry"])
	}
}

// set builds a string membership set from its arguments.
func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// allowedCols is the positive, per-spec column allowlist — the security pillar's
// primary guard. Every column a spec crosses MUST be pinned here. A new spec with
// no entry, or a new/renamed column not in its set, fails CI — forcing a reviewed
// allowlist edit rather than silently widening what data leaves the boundary.
var allowedCols = map[string]map[string]bool{
	"fleet_actions":   set("id", "ts", "agent_name", "action"),
	"fleet_missions":  set("id", "directive", "status", "repo", "branch", "pr_url", "review_rounds", "created_ts", "updated_ts"),
	"fleet_tasks":     set("id", "mission_id", "key", "role", "title", "status", "claimed_by", "created_ts", "done_ts"),
	"fleet_telemetry": set("id", "ts", "mission_id", "kind", "actor", "subject", "model", "detail"),
}

func TestClassificationNoContentStore(t *testing.T) {
	// PRIMARY: positive per-spec allowlist. Every column in ts.cols must be a
	// member of allowedCols[ts.remote]; a spec with no allowlist entry fails.
	// This catches content columns (evidence/text/summary/stdout/…) the denylist
	// below would miss.
	for _, ts := range tableSpecs {
		allowed, ok := allowedCols[ts.remote]
		if !ok {
			t.Fatalf("spec %s has no allowlist entry in allowedCols — add one (reviewed) before shipping", ts.remote)
		}
		for _, c := range strings.Split(ts.cols, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if !allowed[c] {
				t.Fatalf("spec %s crosses column %q not in its pinned allowlist", ts.remote, c)
			}
		}
	}

	// BELT-AND-SUSPENDERS: denylist scan of src + cols + createDDL. Never reference
	// a content store (memory/reference/repoindex) or a content payload column.
	banned := []string{"memory", "reference", "repoindex", "instruction", "result", "verify", ".body"}
	for _, ts := range tableSpecs {
		src := ts.src + " " + ts.cols + " " + ts.createDDL
		for _, b := range banned {
			if containsSubstring(src, b) {
				t.Fatalf("spec %s references banned/content token %q: %s", ts.remote, b, src)
			}
		}
	}
}

func TestFleetActionsDetailExcluded(t *testing.T) {
	dir := t.TempDir()

	// Seed coord.audit with a detail containing a plaintext secret.
	secret := "ghp_SECRET123" // gitleaks:allow — deliberately fake, tests the credential scrubber
	coord := filepath.Join(dir, "coord.sqlite")
	mustSQLite(t, coord, `CREATE TABLE audit(id INTEGER PRIMARY KEY, ts REAL, agent_name TEXT, action TEXT, detail TEXT);
		INSERT INTO audit VALUES (1, 1.0, 'hawk', 'claim', '`+secret+`');
		INSERT INTO audit VALUES (2, 2.0, 'owl', 'done', 'another `+secret+` here');`)

	cfg := SyncConfig{Coord: coord}
	remote := filepath.Join(dir, "remote.duckdb")

	if _, err := Sync(cfg, remote, "brainA"); err != nil {
		t.Fatal(err)
	}

	// (a) secret must appear NOWHERE in fleet_actions — detail was never transferred.
	assertNoColumnValue(t, remote, "fleet_actions", secret)

	// (b) metadata rows were still synced correctly (id/ts/agent/action present).
	db, err := sql.Open("duckdb", remote)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow("SELECT count(*) FROM fleet_actions WHERE brain = 'brainA'").Scan(&n); err != nil {
		t.Fatalf("count fleet_actions: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 metadata rows in fleet_actions, got %d", n)
	}

	// (c) detail column must not exist in fleet_actions at all.
	rows, err := db.Query("SELECT * FROM fleet_actions LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for _, c := range cols {
		if c == "detail" {
			t.Fatal("fleet_actions has a 'detail' column — it must be excluded entirely")
		}
	}
}

func TestFleetTelemetryCarriesModelAndDetail(t *testing.T) {
	dir := t.TempDir()
	wantModel := "gemini-2.5-flash"
	// A finding detail with all five whitelisted keys — these are exactly the keys
	// the finding_reported/finding_resolved emitters write (internal/brain/tasks.go).
	findingDetail := `{"type":"security","severity":"high","outcome":"true_positive","finding_id":"F-42","backend":"gemini"}`

	// Seed a telem DuckDB with a finding_reported event that has model + detail.
	telem := filepath.Join(dir, "telem.duckdb")
	mustDuckDB(t, telem, `CREATE TABLE events(id BIGINT PRIMARY KEY, ts DOUBLE, mission_id BIGINT, kind VARCHAR,
		actor VARCHAR, subject VARCHAR, model VARCHAR, detail VARCHAR);
		INSERT INTO events VALUES (1, 1.0, 42, 'finding_reported', 'hawk', 'auth.go', '`+wantModel+`', '`+findingDetail+`');`)

	cfg := SyncConfig{Telemetry: telem}

	// (a) fresh remote: model + detail columns present and finding metadata propagated.
	remote := filepath.Join(dir, "remote.duckdb")
	if _, err := Sync(cfg, remote, "brainA"); err != nil {
		t.Fatal(err)
	}
	assertTelemetryModelDetail(t, remote, wantModel)

	// (b) idempotent ALTER: a pre-existing remote table WITHOUT model/detail columns
	// acquires them on the next sync run (ADD COLUMN IF NOT EXISTS migration).
	remote2 := filepath.Join(dir, "remote2.duckdb")
	mustDuckDB(t, remote2, `CREATE TABLE fleet_telemetry (brain VARCHAR, id BIGINT, ts DOUBLE, mission_id BIGINT, kind VARCHAR, actor VARCHAR, subject VARCHAR)`)
	if _, err := Sync(cfg, remote2, "brainA"); err != nil {
		t.Fatal(err)
	}
	assertTelemetryModelDetail(t, remote2, wantModel)
}

// assertTelemetryModelDetail verifies fleet_telemetry in path has model + detail
// columns, that model propagated, and that the sanitized detail JSON still carries
// the whitelisted finding-metadata keys (the shape T3's report queries).
func assertTelemetryModelDetail(t *testing.T, path, wantModel string) {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// columns present
	rows, err := db.Query("SELECT * FROM fleet_telemetry LIMIT 0")
	if err != nil {
		t.Fatalf("query fleet_telemetry columns: %v", err)
	}
	colNames, _ := rows.Columns()
	rows.Close()
	colSet := make(map[string]bool, len(colNames))
	for _, c := range colNames {
		colSet[c] = true
	}
	if !colSet["model"] {
		t.Error("fleet_telemetry missing column 'model'")
	}
	if !colSet["detail"] {
		t.Error("fleet_telemetry missing column 'detail'")
	}

	// model propagated; sanitized detail still yields the whitelisted finding keys via
	// json_extract_string (the exact query shape T3's model-comparison report uses).
	var gotModel, gotType, gotSeverity, gotOutcome, gotFindingID, gotBackend string
	if err := db.QueryRow(
		`SELECT COALESCE(model,''),
		        COALESCE(json_extract_string(detail,'$.type'),''),
		        COALESCE(json_extract_string(detail,'$.severity'),''),
		        COALESCE(json_extract_string(detail,'$.outcome'),''),
		        COALESCE(json_extract_string(detail,'$.finding_id'),''),
		        COALESCE(json_extract_string(detail,'$.backend'),'')
		 FROM fleet_telemetry LIMIT 1`,
	).Scan(&gotModel, &gotType, &gotSeverity, &gotOutcome, &gotFindingID, &gotBackend); err != nil {
		t.Fatalf("read fleet_telemetry row: %v", err)
	}
	if gotModel != wantModel {
		t.Errorf("model: got %q, want %q", gotModel, wantModel)
	}
	if gotType != "security" || gotSeverity != "high" || gotOutcome != "true_positive" ||
		gotFindingID != "F-42" || gotBackend != "gemini" {
		t.Errorf("sanitized detail lost whitelisted keys: type=%q severity=%q outcome=%q finding_id=%q backend=%q",
			gotType, gotSeverity, gotOutcome, gotFindingID, gotBackend)
	}
}

// TestFleetTelemetryDetailCanary is the value-level content-leak guard (replaces the
// old raw SECRET-DETAIL assertion). telem.events.detail is a GENERIC map — some kinds
// carry content (mission_created → directive, spawn_refused → reason). The sanitized
// json_object projection keeps ONLY whitelisted finding-metadata keys, so no content
// crosses into fleet_telemetry (→ MotherDuck / ask_fleet).
func TestFleetTelemetryDetailCanary(t *testing.T) {
	dir := t.TempDir()
	canaryDirective := "PROPRIETARY-DIRECTIVE-CANARY-7f3"
	canaryReason := "SECRET-SPAWN-REASON-CANARY-9b2"

	telem := filepath.Join(dir, "telem.duckdb")
	mustDuckDB(t, telem, `CREATE TABLE events(id BIGINT PRIMARY KEY, ts DOUBLE, mission_id BIGINT, kind VARCHAR,
		actor VARCHAR, subject VARCHAR, model VARCHAR, detail VARCHAR);
		INSERT INTO events VALUES
			(1, 1.0, 7, 'mission_created', 'operator', '', '', '{"directive":"`+canaryDirective+`","review":true}'),
			(2, 2.0, 7, 'spawn_refused', 'hawk', 'kid', 'gemini', '{"reason":"`+canaryReason+`"}'),
			(3, 3.0, 7, 'finding_reported', 'hawk', 'auth.go', 'gemini', '{"type":"vuln","severity":"critical"}');`)

	cfg := SyncConfig{Telemetry: telem}
	remote := filepath.Join(dir, "remote.duckdb")
	if _, err := Sync(cfg, remote, "brainA"); err != nil {
		t.Fatal(err)
	}

	// All three rows synced (kind/actor/subject metadata is fine to cross)...
	if rows := readRemote(t, remote); rows["fleet_telemetry"] != 3 {
		t.Fatalf("expected 3 telemetry rows, got %d", rows["fleet_telemetry"])
	}
	// ...but NEITHER content value appears in ANY column of ANY fleet_telemetry row.
	assertNoColumnValue(t, remote, "fleet_telemetry", canaryDirective) // mission_created.directive dropped
	assertNoColumnValue(t, remote, "fleet_telemetry", canaryReason)    // spawn_refused.reason dropped
	// The finding metadata (whitelisted) DID survive for the report.
	db, err := sql.Open("duckdb", remote)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sev string
	if err := db.QueryRow(
		`SELECT json_extract_string(detail,'$.severity') FROM fleet_telemetry WHERE kind='finding_reported'`,
	).Scan(&sev); err != nil {
		t.Fatalf("read finding severity: %v", err)
	}
	if sev != "critical" {
		t.Errorf("whitelisted finding severity lost: got %q, want %q", sev, "critical")
	}
}

// ---- test helpers ----

// mustSQLite creates a SQLite file at path and populates it by executing each
// semicolon-separated statement in ddl via the pure-Go modernc sqlite driver.
func mustSQLite(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range strings.Split(ddl, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("mustSQLite exec %s: %v (stmt: %s)", path, err, stmt)
		}
	}
}

// mustDuckDB creates (or opens) a DuckDB file at path and executes each
// semicolon-separated statement in ddl.
func mustDuckDB(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range strings.Split(ddl, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("mustDuckDB exec %s: %v (stmt: %s)", path, err, stmt)
		}
	}
}

// readRemote opens the remote .duckdb and returns a map of fleet_* table name →
// row count. Tables that don't exist yet are omitted from the map (map zero
// value is 0 for missing keys).
func readRemote(t *testing.T, path string) map[string]int {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := map[string]int{}
	for _, tbl := range []string{"fleet_actions", "fleet_missions", "fleet_tasks", "fleet_telemetry"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err == nil {
			m[tbl] = n
		}
		// tolerate missing table — just absent from map
	}
	return m
}

// assertNoColumnValue scans every row and every text column in table (in the
// remote .duckdb at path) and fails if banned appears in any cell value.
func assertNoColumnValue(t *testing.T, path, table, banned string) {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT * FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			switch s := v.(type) {
			case string:
				if strings.Contains(s, banned) {
					t.Fatalf("table %s contains banned value %q", table, banned)
				}
			case []byte:
				if strings.Contains(string(s), banned) {
					t.Fatalf("table %s contains banned value %q ([]byte)", table, banned)
				}
			}
		}
	}
}

// distinctBrains returns the number of distinct brain values in table.
func distinctBrains(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(DISTINCT brain) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("distinctBrains %s: %v", table, err)
	}
	return n
}

// containsSubstring reports whether s contains sub as a substring (case-insensitive).
func containsSubstring(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
