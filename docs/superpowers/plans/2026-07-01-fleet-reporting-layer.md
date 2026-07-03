# Fleet Reporting Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `internal/fleet` from syncing one table (`coord.audit`) to a curated, metadata-only reporting set (missions, tasks, telemetry) — `brainID`-tagged, incremental — into the shared MotherDuck, so reports span all swarms.

**Architecture:** Generalize `fleet.Sync` from a single hardcoded INSERT into a list of curated table-sync specs, each with an explicit column allowlist + a cursor. The in-memory-DuckDB bridge `ATTACH`es the metadata stores read-only (coord/mission/queue SQLite + telemetry DuckDB) and the remote, then runs each spec's incremental `INSERT … SELECT '<brainID>', <curated cols> … WHERE <cursor> > max_for_brain`. The classification boundary is the spec list itself — content stores are never attached.

**Tech Stack:** Go 1.26; `github.com/marcboeker/go-duckdb/v2` (CGO) with the `sqlite` + `motherduck` DuckDB extensions; the existing `internal/fleet` seed.

## Global Constraints

- **Metadata-only, classification enforced by construction.** The table-spec list is the ONLY thing that can cross. Content stores (`memory`/`reference`/`repoindex`) are NEVER in the attach set. Each spec's column list is an explicit allowlist — never `SELECT *`, never `instruction`/`result`/`verify`/`detail`-payload bodies. A test asserts (a) no content store is attached, (b) each spec's columns ⊆ a pinned allowlist.
- **`brainID`-tagged + incremental + idempotent.** Every remote row carries `brain` first; each spec syncs rows past `max(<cursor>)` for this brain; re-running inserts 0 rows for unchanged sources; two brains federate into one remote without clobbering.
- **Reuse the existing bridge pattern** (`INSTALL sqlite`/`motherduck`, in-mem DuckDB, ATTACH read-only) — do not invent new plumbing. The existing `fleet_actions` sync becomes one spec among several, behavior unchanged.
- **Graceful:** a source store with an empty configured path is skipped (its specs don't run); a remote/store attach failure returns a clear error; the ticker (unchanged) retries next cycle.
- `go build ./...` and `go test ./...` stay green.

## Scope note (a spec reconciliation)

The spec listed a `fleet_phases` table. The `phases` table has **no timestamp column** (only `position`), so its mutating `status` cannot be incrementally synced to reflect current state without a schema change — an id-cursor would freeze every phase at its insert-time status. **`fleet_phases` is therefore deferred** to a follow-up (add an `updated_ts` to `phases` first); phase-level reporting is covered in the interim by `fleet_tasks` (task titles carry the phase name) + `fleet_missions`. This is the one deliberate deviation from the spec's table list.

---

## File Structure

- `internal/fleet/sync.go` (modify) — `SyncConfig`, the `[]tableSync` specs, the generalized `Sync`.
- `internal/fleet/sync_test.go` (modify) — round-trip curated rows, incremental no-dup, federation, partial-config, classification guard.
- `cmd/corral/main.go` (modify) — build `SyncConfig` from the existing DB paths; call the widened `Sync`.

---

## Task 1: generalize `fleet.Sync` to a curated table-spec list

**Files:** Modify `internal/fleet/sync.go`; Modify `internal/fleet/sync_test.go`

**Interfaces:**
- Consumes (existing): the in-mem DuckDB bridge pattern; `esc()`.
- Produces:
  - `type SyncConfig struct{ Coord, Mission, Queue, Telemetry string }`
  - `func Sync(cfg SyncConfig, remoteAttach, brainID string) (int, error)` — SIGNATURE CHANGE from `(coordDBPath, remoteAttach, brainID)`. Returns total rows synced across all specs.
  - internal `tableSpecs []tableSync` (the curated allowlist).

- [ ] **Step 1: Write the failing test**

```go
// internal/fleet/sync_test.go — REPLACE the existing test body's Sync call shape.
// Uses a local .duckdb file as the "remote" (no MotherDuck / no network).
package fleet

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// seedSQLite makes a sqlite file with a table + rows via the duckdb sqlite writer
// (keeps the test dependency-light; modernc isn't needed here).
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
	// telemetry (duckdb)
	telem := filepath.Join(dir, "telem.duckdb")
	mustDuckDB(t, telem, `CREATE TABLE events(id BIGINT PRIMARY KEY, ts DOUBLE, mission_id BIGINT, kind VARCHAR,
		actor VARCHAR, subject VARCHAR, detail VARCHAR);
		INSERT INTO events VALUES (100, 3.0, 5, 'exec', 'hawk', 'go build', 'SECRET-DETAIL');`)
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
	// classification: the content payload columns must NOT be present in the remote
	assertNoColumnValue(t, remote, "fleet_tasks", "SECRET-INSTRUCTION")   // instruction excluded
	assertNoColumnValue(t, remote, "fleet_telemetry", "SECRET-DETAIL")    // detail excluded
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

func TestClassificationNoContentStore(t *testing.T) {
	// the spec list must never reference memory/reference/repoindex, and never a
	// content payload column.
	banned := []string{"memory", "reference", "repoindex", "instruction", "result", "verify", ".body"}
	for _, ts := range tableSpecs {
		src := ts.src + " " + ts.cols
		for _, b := range banned {
			if containsWord(src, b) {
				t.Fatalf("spec %s references banned/content token %q: %s", ts.remote, b, src)
			}
		}
	}
}
```

> NOTE: implement the small test helpers `mustSQLite`, `mustDuckDB` (create a DB file + run DDL — `mustSQLite` can shell `sqlite3` OR, simpler, open a duckdb in-mem, `INSTALL sqlite`, `ATTACH '<path>' AS s (TYPE sqlite)`, and run the DDL against `s`), `readRemote` (open the remote .duckdb, `SELECT count(*)` per `fleet_*` table into a map — tolerate a missing table as 0), `assertNoColumnValue` (scan every text column of a remote table, fail if the banned string appears), `distinctBrains` (`SELECT count(DISTINCT brain)`), `containsWord`. Keep them at the bottom of the test file. If `mustSQLite` via the duckdb-sqlite-writer is awkward, shelling `sqlite3` is fine (it's available); the existing `sync_test.go` already builds a sqlite fixture — reuse its helper if present.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/fleet/ -run TestSync`
Expected: FAIL — `Sync` signature mismatch / `SyncConfig`/`tableSpecs` undefined.

- [ ] **Step 3: Implement `internal/fleet/sync.go`**

```go
package fleet

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
)

func esc(s string) string { return strings.ReplaceAll(s, "'", "''") }

// SyncConfig holds the source DB paths. An empty path means that store is skipped
// (its table specs don't run) — a brain missing a store still syncs the rest.
type SyncConfig struct {
	Coord     string // coordination SQLite (audit)
	Mission   string // mission SQLite (missions)
	Queue     string // queue SQLite (tasks)
	Telemetry string // telemetry DuckDB (events)
}

// tableSync is one curated, metadata-only replication spec. `cols` is an explicit
// allowlist (never SELECT *, never a content payload column). `needs` names the
// SyncConfig store that must be configured for this spec to run.
type tableSync struct {
	remote    string // remote table name (fleet_*)
	createDDL string // CREATE TABLE IF NOT EXISTS remote.<remote> (...)
	src       string // attached-alias.table, e.g. "mission.missions"
	cols      string // curated columns after the brain tag
	cursor    string // incremental cursor column (must be in createDDL)
	filter    string // extra WHERE predicate ANDed in, or ""
	needs     string // "coord" | "mission" | "queue" | "telem"
}

// tableSpecs is the classification allowlist — the ONLY tables/columns that cross.
// Content stores (memory/reference/repoindex) are deliberately absent.
var tableSpecs = []tableSync{
	{
		remote:    "fleet_actions",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_actions (brain VARCHAR, id BIGINT, ts DOUBLE, agent VARCHAR, action VARCHAR, detail VARCHAR)`,
		src:       "coord.audit",
		cols:      "id, ts, agent_name, action, detail",
		cursor:    "id",
		needs:     "coord",
	},
	{
		remote:    "fleet_missions",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_missions (brain VARCHAR, id BIGINT, directive VARCHAR, status VARCHAR, repo VARCHAR, branch VARCHAR, pr_url VARCHAR, review_rounds BIGINT, created_ts DOUBLE, updated_ts DOUBLE)`,
		src:       "mission.missions",
		cols:      "id, directive, status, repo, branch, pr_url, review_rounds, created_ts, updated_ts",
		cursor:    "updated_ts", // mutable rows sync as temporal versions; query max(updated_ts) per (brain,id) for current state
		needs:     "mission",
	},
	{
		remote:    "fleet_tasks",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_tasks (brain VARCHAR, id BIGINT, mission_id BIGINT, key VARCHAR, role VARCHAR, title VARCHAR, status VARCHAR, claimed_by VARCHAR, created_ts DOUBLE, done_ts DOUBLE)`,
		src:       "queue.tasks",
		cols:      "id, mission_id, key, role, title, status, claimed_by, created_ts, done_ts",
		cursor:    "done_ts", // stream of COMPLETED tasks (done_ts set once)
		filter:    "done_ts IS NOT NULL",
		needs:     "queue",
	},
	{
		remote:    "fleet_telemetry",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_telemetry (brain VARCHAR, id BIGINT, ts DOUBLE, mission_id BIGINT, kind VARCHAR, actor VARCHAR, subject VARCHAR)`,
		src:       "telem.events",
		cols:      "id, ts, mission_id, kind, actor, subject", // detail (free-form payload) deliberately excluded
		cursor:    "id",
		needs:     "telem",
	},
}

// Sync replicates the curated metadata reporting set from the configured stores to
// the remote (a local .duckdb in tests, or `md:<db>` for MotherDuck; the brain must
// set the motherduck_token env), tagging every row with brainID. Incremental per
// (brain, table): only rows past the remote's max cursor for this brain are copied.
func Sync(cfg SyncConfig, remoteAttach, brainID string) (int, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return 0, err
	}
	defer db.Close()

	if _, err := db.Exec("INSTALL sqlite; LOAD sqlite;"); err != nil {
		return 0, fmt.Errorf("load sqlite extension: %w", err)
	}
	if strings.HasPrefix(remoteAttach, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			return 0, fmt.Errorf("load motherduck extension: %w", err)
		}
	}

	attached := map[string]bool{}
	attach := func(alias, path string, sqlite bool) error {
		if path == "" {
			return nil
		}
		typ := " (READ_ONLY)"
		if sqlite {
			typ = " (TYPE sqlite, READ_ONLY)"
		}
		if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS %s%s", esc(path), alias, typ)); err != nil {
			return fmt.Errorf("attach %s: %w", alias, err)
		}
		attached[alias] = true
		return nil
	}
	if err := attach("coord", cfg.Coord, true); err != nil {
		return 0, err
	}
	if err := attach("mission", cfg.Mission, true); err != nil {
		return 0, err
	}
	if err := attach("queue", cfg.Queue, true); err != nil {
		return 0, err
	}
	if err := attach("telem", cfg.Telemetry, false); err != nil {
		return 0, err
	}
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS remote", esc(remoteAttach))); err != nil {
		return 0, fmt.Errorf("attach remote: %w", err)
	}

	total := 0
	for _, ts := range tableSpecs {
		if !attached[ts.needs] {
			continue // source store not configured — skip this spec
		}
		if _, err := db.Exec(ts.createDDL); err != nil {
			return total, fmt.Errorf("create %s: %w", ts.remote, err)
		}
		var last sql.NullFloat64
		if err := db.QueryRow(fmt.Sprintf("SELECT max(%s) FROM remote.%s WHERE brain = ?", ts.cursor, ts.remote), brainID).Scan(&last); err != nil {
			return total, fmt.Errorf("cursor %s: %w", ts.remote, err)
		}
		// format the cursor floor as a plain decimal (never scientific notation —
		// updated_ts/done_ts are large Unix floats that %v/%g would render as 1.7e+09,
		// which DuckDB won't parse in an inline literal).
		floor := strconv.FormatFloat(cursorFloor(last), 'f', -1, 64)
		where := fmt.Sprintf("%s > %s", ts.cursor, floor)
		if ts.filter != "" {
			where = ts.filter + " AND " + where
		}
		q := fmt.Sprintf("INSERT INTO remote.%s SELECT '%s', %s FROM %s WHERE %s ORDER BY %s",
			ts.remote, esc(brainID), ts.cols, ts.src, where, ts.cursor)
		res, err := db.Exec(q)
		if err != nil {
			return total, fmt.Errorf("insert %s: %w", ts.remote, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// cursorFloor is the low-water mark for the incremental WHERE. When the remote holds
// no rows for this brain yet, NullFloat64 is invalid → return -1, which is below any
// real cursor (all cursors — id, updated_ts, done_ts, ts — are non-negative), so the
// first sync copies everything.
func cursorFloor(last sql.NullFloat64) float64 {
	if !last.Valid {
		return -1
	}
	return last.Float64
}
```

> NOTE on cursors: `id` columns are integers but `max(id)` scans fine into `NullFloat64` in DuckDB, and `id > -1e18` / `id > 2` compares correctly (DuckDB coerces). `done_ts`/`updated_ts` are already floats. Using one `NullFloat64` cursor type across all specs keeps the loop uniform. Verify the two integer-cursor specs (actions, telemetry) still sync all rows on the first run and 0 on the second.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fleet/`
Expected: PASS — curated rows synced + brain-tagged; content columns absent; second sync 0 rows; two brains federate; partial config skips telemetry; classification spec-list guard passes.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/sync.go internal/fleet/sync_test.go
git commit -m "feat(fleet): curated metadata reporting sync (missions/tasks/telemetry) — brainID-tagged, incremental"
```

---

## Task 2: cmd/corral — wire the source DB paths into the widened `Sync`

**Files:** Modify `cmd/corral/main.go`

**Interfaces:**
- Consumes: `fleet.SyncConfig`, `fleet.Sync(cfg, target, brainID)` (Task 1).

- [ ] **Step 1: Update `startFleetSync`**

`startFleetSync` currently takes only `coordDBPath` and calls `fleet.Sync(coordDBPath, target, brainID)`. Widen it to receive all the metadata DB paths and build a `SyncConfig`. Change the signature + the call site (where `startFleetSync(coordDBPath)` is invoked in `main`, pass the already-constructed `missionDB`, `queueDB`, and the telemetry DB path):

```go
func startFleetSync(cfg fleet.SyncConfig) {
	target := os.Getenv("CORRALAI_MOTHERDUCK")
	if target == "" {
		log.Printf("fleet: MotherDuck sync disabled (set CORRALAI_MOTHERDUCK to enable)")
		return
	}
	if tok := os.Getenv("CORRALAI_MOTHERDUCK_TOKEN"); tok != "" {
		os.Setenv("motherduck_token", tok)
	}
	host, _ := os.Hostname()
	brainID := env("CORRALAI_BRAIN_ID", host)
	interval := 30 * time.Second
	if s := os.Getenv("CORRALAI_SYNC_INTERVAL"); s != "" {
		if d, err := strconv.Atoi(s); err == nil && d > 0 {
			interval = time.Duration(d) * time.Second
		}
	}
	log.Printf("fleet: syncing curated reporting set to %s every %s (brain=%s)", target, interval, brainID)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if n, err := fleet.Sync(cfg, target, brainID); err != nil {
				log.Printf("fleet: sync error: %v", err)
			} else if n > 0 {
				log.Printf("fleet: synced %d reporting rows to %s", n, target)
			}
		}
	}()
}
```

At the call site in `main`, build the config from the DB paths already constructed there (the telemetry DB path — find where `telemetry.Open(...)` is called and reuse that path variable; if the telemetry path isn't in a variable, add one mirroring the others, e.g. `telemetryDB := env("CORRALAI_TELEMETRY_DB", filepath.Join(home, ".claude", "corralai_telemetry.duckdb"))`):
```go
	startFleetSync(fleet.SyncConfig{
		Coord:     dbPath,        // the coordination SQLite path (existing var)
		Mission:   missionDB,     // existing
		Queue:     queueDB,       // existing
		Telemetry: telemetryDB,   // the telemetry DuckDB path
	})
```
Confirm the real variable names for each DB path in `main` (the earlier `env(...)` lines) and use them; don't hardcode paths.

- [ ] **Step 2: Build + commit**

Run: `go build ./... && go test ./...`
Expected: build OK; all tests PASS.

```bash
git add cmd/corral/main.go
git commit -m "feat(corral): sync the curated reporting set (mission/queue/telemetry) to MotherDuck"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `go test ./...` — all PASS
- [ ] Classification: `TestClassificationNoContentStore` passes (no content store / payload column in any spec); the attach set is coord/mission/queue/telem only — grep confirms `memory`/`reference`/`repoindex` appear nowhere in `sync.go`.
- [ ] Federation + incremental: a second `Sync` is a no-op; two brainIDs coexist in the remote.
- [ ] `fleet_phases` is intentionally absent (deferred — `phases` lacks an update timestamp); noted in the scope section.
