// SPDX-License-Identifier: Elastic-2.0

package fleet

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Sentinel timestamps used across all retention tests.
// now = Unix 1_000_000_000; TTLDays = 30 → cutoff ≈ Unix 997_400_000.
// old  = 100.0  (far below cutoff — eligible for TTL)
// recent = 2_000_000_000.0 (far above cutoff — never eligible)
const (
	tsOld    = 100.0
	tsRecent = 2_000_000_000.0
)

var retentionNow = time.Unix(1_000_000_000, 0)

// ── DDL helpers ──────────────────────────────────────────────────────────────

// createRemoteTables creates all four fleet_* tables in an existing DuckDB file.
func createRemoteTables(t *testing.T, path string) {
	t.Helper()
	mustDuckDB(t, path, `
		CREATE TABLE fleet_missions  (brain VARCHAR, id BIGINT, directive VARCHAR, status VARCHAR, repo VARCHAR, branch VARCHAR, pr_url VARCHAR, review_rounds BIGINT, created_ts DOUBLE, updated_ts DOUBLE);
		CREATE TABLE fleet_actions   (brain VARCHAR, id BIGINT, ts DOUBLE, agent VARCHAR, action VARCHAR);
		CREATE TABLE fleet_tasks     (brain VARCHAR, id BIGINT, mission_id BIGINT, key VARCHAR, role VARCHAR, title VARCHAR, status VARCHAR, claimed_by VARCHAR, created_ts DOUBLE, done_ts DOUBLE);
		CREATE TABLE fleet_telemetry (brain VARCHAR, id BIGINT, ts DOUBLE, mission_id BIGINT, kind VARCHAR, actor VARCHAR, subject VARCHAR)`)
}

// remoteDB opens the remote .duckdb file and returns the *sql.DB.  Caller must Close.
func remoteDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	return db
}

// maxCursor queries max(col) for rows where brain = brainID. Returns -1 when no rows.
func maxCursor(t *testing.T, db *sql.DB, table, col, brainID string) float64 {
	t.Helper()
	var v sql.NullFloat64
	if err := db.QueryRow(
		"SELECT max("+col+") FROM "+table+" WHERE brain = ?", brainID,
	).Scan(&v); err != nil {
		t.Fatalf("maxCursor %s.%s: %v", table, col, err)
	}
	if !v.Valid {
		return -1
	}
	return v.Float64
}

// rowCountFor returns the number of rows in table for the given brain.
func rowCountFor(t *testing.T, db *sql.DB, table, brainID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT count(*) FROM "+table+" WHERE brain = ?", brainID,
	).Scan(&n); err != nil {
		t.Fatalf("rowCountFor %s brain=%s: %v", table, brainID, err)
	}
	return n
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestCompactMissionsKeepsLatestPerMission verifies mission compaction:
//   - For each (brain, id) only the row with the highest updated_ts survives.
//   - The "other" brain's rows are never touched.
//   - max(updated_ts) for "self" is unchanged (cursor safety).
func TestCompactMissionsKeepsLatestPerMission(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.duckdb")
	createRemoteTables(t, remote)

	// Seed: mission id=1 has three versions (ts 1,2,3); id=2 has two (ts 5,6).
	// Also a row for "other" that must survive untouched.
	mustDuckDB(t, remote, `
		INSERT INTO fleet_missions VALUES
		  ('self',  1, 'task A', 'open', '', '', '', 0, 0.0, 1.0),
		  ('self',  1, 'task A', 'open', '', '', '', 0, 0.0, 2.0),
		  ('self',  1, 'task A', 'done', '', '', '', 0, 0.0, 3.0),
		  ('self',  2, 'task B', 'open', '', '', '', 0, 0.0, 5.0),
		  ('self',  2, 'task B', 'done', '', '', '', 0, 0.0, 6.0),
		  ('other', 1, 'task C', 'open', '', '', '', 0, 0.0, 10.0)`)

	cfg := RetentionConfig{TTLDays: 0} // TTL off; compaction only
	deleted, err := Compact(cfg, remote, "self", retentionNow)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	db := remoteDB(t, remote)
	defer db.Close()

	// self: exactly 2 rows remain (one per mission id, latest version).
	selfRows := rowCountFor(t, db, "fleet_missions", "self")
	if selfRows != 2 {
		t.Fatalf("expected 2 self rows after compaction, got %d", selfRows)
	}

	// The surviving rows must be the latest updated_ts per id.
	var maxTS1, maxTS2 float64
	db.QueryRow("SELECT max(updated_ts) FROM fleet_missions WHERE brain='self' AND id=1").Scan(&maxTS1)
	db.QueryRow("SELECT max(updated_ts) FROM fleet_missions WHERE brain='self' AND id=2").Scan(&maxTS2)
	if maxTS1 != 3.0 {
		t.Errorf("mission id=1: expected updated_ts=3.0, got %v", maxTS1)
	}
	if maxTS2 != 6.0 {
		t.Errorf("mission id=2: expected updated_ts=6.0, got %v", maxTS2)
	}

	// CURSOR SAFETY: max(updated_ts) for self must still be 6.0.
	if c := maxCursor(t, db, "fleet_missions", "updated_ts", "self"); c != 6.0 {
		t.Errorf("cursor safety: max(updated_ts) changed to %v (want 6.0)", c)
	}

	// Per-brain isolation: "other" row is untouched.
	if otherRows := rowCountFor(t, db, "fleet_missions", "other"); otherRows != 1 {
		t.Errorf("other brain: expected 1 row, got %d", otherRows)
	}

	// Deleted count reflects the 3 stale rows removed (id=1 @ ts1, id=1 @ ts2, id=2 @ ts5).
	if n := deleted["fleet_missions"]; n != 3 {
		t.Errorf("deleted[fleet_missions]: expected 3, got %d", n)
	}
}

// TestTTLDropsOldKeepsWatermark verifies per-append-table TTL with the critical
// OLD-max-cursor survival case:
//   - fleet_telemetry / fleet_actions: id is the cursor. The MAX-id row (id=50) is
//     deliberately old (ts=old) but must survive because it carries the sync watermark.
//   - fleet_tasks: done_ts is both cursor and TTL column. The row with max(done_ts)
//     survives even though it is old.
//   - The "other" brain's rows are untouched in all tables.
func TestTTLDropsOldKeepsWatermark(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.duckdb")
	createRemoteTables(t, remote)

	// fleet_telemetry: id=10 (old, not max) → deleted; id=50 (old, MAX id) → survives;
	// id=30 (recent) → survives.  "other" row also old → untouched.
	mustDuckDB(t, remote, `
		INSERT INTO fleet_telemetry VALUES
		  ('self',  10, `+fmtF(tsOld)+`, 1, 'exec', 'hawk', 'go build'),
		  ('self',  50, `+fmtF(tsOld)+`, 1, 'exec', 'hawk', 'go test'),
		  ('self',  30, `+fmtF(tsRecent)+`, 1, 'exec', 'hawk', 'go vet'),
		  ('other', 99, `+fmtF(tsOld)+`, 1, 'exec', 'owl',  'go build')`)

	// fleet_actions: id=1 (old, not max) → deleted; id=5 (old, MAX id) → survives;
	// id=3 (recent) → survives.
	mustDuckDB(t, remote, `
		INSERT INTO fleet_actions VALUES
		  ('self',  1, `+fmtF(tsOld)+`, 'hawk', 'claim'),
		  ('self',  5, `+fmtF(tsOld)+`, 'hawk', 'done'),
		  ('self',  3, `+fmtF(tsRecent)+`, 'hawk', 'exec'),
		  ('other', 9, `+fmtF(tsOld)+`, 'owl',  'claim')`)

	// fleet_tasks: done_ts=100 (old, not max) → deleted; done_ts=200 (old, MAX done_ts) → survives.
	mustDuckDB(t, remote, `
		INSERT INTO fleet_tasks VALUES
		  ('self',  1, 1, 'k1', 'worker', 'Build', 'done', 'hawk', 0.0, 100.0),
		  ('self',  2, 1, 'k2', 'worker', 'Test',  'done', 'hawk', 0.0, 200.0),
		  ('other', 3, 1, 'k3', 'worker', 'Build', 'done', 'owl',  0.0, 50.0)`)

	cfg := RetentionConfig{TTLDays: 30}
	_, err := Compact(cfg, remote, "self", retentionNow)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	db := remoteDB(t, remote)
	defer db.Close()

	// ── fleet_telemetry ──
	// id=10 must be gone; id=50 and id=30 survive.
	var count10 int
	db.QueryRow("SELECT count(*) FROM fleet_telemetry WHERE brain='self' AND id=10").Scan(&count10)
	if count10 != 0 {
		t.Errorf("telemetry id=10 (old, not max-id): expected deleted, still present")
	}
	var count50 int
	db.QueryRow("SELECT count(*) FROM fleet_telemetry WHERE brain='self' AND id=50").Scan(&count50)
	if count50 != 1 {
		t.Errorf("telemetry id=50 (old, MAX id): expected to survive (watermark guard), got count=%d", count50)
	}
	// CURSOR SAFETY: max(id) for self must still be 50.
	if c := maxCursor(t, db, "fleet_telemetry", "id", "self"); c != 50 {
		t.Errorf("telemetry cursor safety: max(id) changed to %v (want 50)", c)
	}
	// Per-brain isolation: "other" row untouched.
	if n := rowCountFor(t, db, "fleet_telemetry", "other"); n != 1 {
		t.Errorf("telemetry: other brain row count: expected 1, got %d", n)
	}

	// ── fleet_actions ──
	var countA1 int
	db.QueryRow("SELECT count(*) FROM fleet_actions WHERE brain='self' AND id=1").Scan(&countA1)
	if countA1 != 0 {
		t.Errorf("actions id=1 (old, not max-id): expected deleted, still present")
	}
	var countA5 int
	db.QueryRow("SELECT count(*) FROM fleet_actions WHERE brain='self' AND id=5").Scan(&countA5)
	if countA5 != 1 {
		t.Errorf("actions id=5 (old, MAX id): expected to survive (watermark guard), got count=%d", countA5)
	}
	if c := maxCursor(t, db, "fleet_actions", "id", "self"); c != 5 {
		t.Errorf("actions cursor safety: max(id) changed to %v (want 5)", c)
	}
	if n := rowCountFor(t, db, "fleet_actions", "other"); n != 1 {
		t.Errorf("actions: other brain row count: expected 1, got %d", n)
	}

	// ── fleet_tasks ──
	// done_ts=100 (old, not max) → deleted; done_ts=200 (old, MAX done_ts) → survives.
	var countT100 int
	db.QueryRow("SELECT count(*) FROM fleet_tasks WHERE brain='self' AND done_ts=100.0").Scan(&countT100)
	if countT100 != 0 {
		t.Errorf("tasks done_ts=100 (old, not max-done_ts): expected deleted, still present")
	}
	var countT200 int
	db.QueryRow("SELECT count(*) FROM fleet_tasks WHERE brain='self' AND done_ts=200.0").Scan(&countT200)
	if countT200 != 1 {
		t.Errorf("tasks done_ts=200 (old, MAX done_ts): expected to survive (watermark guard), got count=%d", countT200)
	}
	// CURSOR SAFETY: max(done_ts) for self must still be 200.
	if c := maxCursor(t, db, "fleet_tasks", "done_ts", "self"); c != 200.0 {
		t.Errorf("tasks cursor safety: max(done_ts) changed to %v (want 200.0)", c)
	}
	if n := rowCountFor(t, db, "fleet_tasks", "other"); n != 1 {
		t.Errorf("tasks: other brain row count: expected 1, got %d", n)
	}
}

// TestCursorSafetyEndToEnd records max(cursor) for ALL four tables before Compact
// and asserts each is unchanged afterward — proving no re-sync would be triggered.
func TestCursorSafetyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.duckdb")
	createRemoteTables(t, remote)

	// Seed: each table has stale rows and at least one old-but-max-cursor row for "self".
	mustDuckDB(t, remote, `
		INSERT INTO fleet_missions VALUES
		  ('self', 1, 'dir', 'open', '', '', '', 0, 0.0, 1.0),
		  ('self', 1, 'dir', 'done', '', '', '', 0, 0.0, 7.0),
		  ('self', 2, 'dir', 'open', '', '', '', 0, 0.0, 5.0)`)
	mustDuckDB(t, remote, `
		INSERT INTO fleet_actions VALUES
		  ('self', 1, `+fmtF(tsOld)+`, 'hawk', 'claim'),
		  ('self', 9, `+fmtF(tsOld)+`, 'hawk', 'done')`)
	mustDuckDB(t, remote, `
		INSERT INTO fleet_tasks VALUES
		  ('self', 1, 1, 'k1', 'w', 'T', 'done', 'hawk', 0.0, 100.0),
		  ('self', 2, 1, 'k2', 'w', 'T', 'done', 'hawk', 0.0, 150.0)`)
	mustDuckDB(t, remote, `
		INSERT INTO fleet_telemetry VALUES
		  ('self', 10, `+fmtF(tsOld)+`, 1, 'exec', 'hawk', 's'),
		  ('self', 20, `+fmtF(tsOld)+`, 1, 'exec', 'hawk', 's')`)

	db := remoteDB(t, remote)
	defer db.Close()

	// Record pre-Compact max cursors.
	preM := maxCursor(t, db, "fleet_missions", "updated_ts", "self")
	preA := maxCursor(t, db, "fleet_actions", "id", "self")
	preTsk := maxCursor(t, db, "fleet_tasks", "done_ts", "self")
	preTel := maxCursor(t, db, "fleet_telemetry", "id", "self")

	cfg := RetentionConfig{TTLDays: 30}
	if _, err := Compact(cfg, remote, "self", retentionNow); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Assert max cursors are unchanged after Compact.
	if c := maxCursor(t, db, "fleet_missions", "updated_ts", "self"); c != preM {
		t.Errorf("fleet_missions cursor: pre=%v post=%v (CHANGED — sync watermark broken)", preM, c)
	}
	if c := maxCursor(t, db, "fleet_actions", "id", "self"); c != preA {
		t.Errorf("fleet_actions cursor: pre=%v post=%v (CHANGED — sync watermark broken)", preA, c)
	}
	if c := maxCursor(t, db, "fleet_tasks", "done_ts", "self"); c != preTsk {
		t.Errorf("fleet_tasks cursor: pre=%v post=%v (CHANGED — sync watermark broken)", preTsk, c)
	}
	if c := maxCursor(t, db, "fleet_telemetry", "id", "self"); c != preTel {
		t.Errorf("fleet_telemetry cursor: pre=%v post=%v (CHANGED — sync watermark broken)", preTel, c)
	}
}

// TestRetentionDisabledNoDeletes verifies Disabled=true is a strict no-op:
// no rows are deleted and the returned map is nil.
func TestRetentionDisabledNoDeletes(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.duckdb")
	createRemoteTables(t, remote)

	mustDuckDB(t, remote, `
		INSERT INTO fleet_missions VALUES
		  ('self', 1, 'dir', 'open', '', '', '', 0, 0.0, 1.0),
		  ('self', 1, 'dir', 'done', '', '', '', 0, 0.0, 2.0)`)
	mustDuckDB(t, remote, `
		INSERT INTO fleet_actions VALUES ('self', 1, `+fmtF(tsOld)+`, 'hawk', 'claim')`)

	cfg := RetentionConfig{Disabled: true, TTLDays: 30}
	deleted, err := Compact(cfg, remote, "self", retentionNow)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if deleted != nil {
		t.Errorf("Disabled: expected nil map, got %v", deleted)
	}

	db := remoteDB(t, remote)
	defer db.Close()

	// All rows must still be present.
	if n := rowCountFor(t, db, "fleet_missions", "self"); n != 2 {
		t.Errorf("missions: expected 2 rows (no-op), got %d", n)
	}
	if n := rowCountFor(t, db, "fleet_actions", "self"); n != 1 {
		t.Errorf("actions: expected 1 row (no-op), got %d", n)
	}
}

// TestTTLZeroSkipsTTLButCompacts verifies that TTLDays=0 suppresses the append-stream
// TTL while mission compaction still runs.
func TestTTLZeroSkipsTTLButCompacts(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.duckdb")
	createRemoteTables(t, remote)

	// fleet_missions: two stale versions for id=1 → compaction should remove older one.
	mustDuckDB(t, remote, `
		INSERT INTO fleet_missions VALUES
		  ('self', 1, 'dir', 'open', '', '', '', 0, 0.0, 1.0),
		  ('self', 1, 'dir', 'done', '', '', '', 0, 0.0, 2.0)`)
	// fleet_actions: old rows that would be TTL'd if TTLDays>0 — must survive.
	mustDuckDB(t, remote, `
		INSERT INTO fleet_actions VALUES
		  ('self', 1, `+fmtF(tsOld)+`, 'hawk', 'claim'),
		  ('self', 2, `+fmtF(tsOld)+`, 'hawk', 'done')`)

	cfg := RetentionConfig{TTLDays: 0}
	deleted, err := Compact(cfg, remote, "self", retentionNow)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	db := remoteDB(t, remote)
	defer db.Close()

	// Mission compaction ran: 1 stale version removed.
	if n := deleted["fleet_missions"]; n != 1 {
		t.Errorf("TTL=0: missions compaction should delete 1 stale row, got %d", n)
	}
	if n := rowCountFor(t, db, "fleet_missions", "self"); n != 1 {
		t.Errorf("TTL=0: expected 1 mission row after compaction, got %d", n)
	}

	// TTL was skipped: append-stream rows untouched.
	if _, present := deleted["fleet_actions"]; present {
		t.Errorf("TTL=0: fleet_actions should not appear in deleted map (TTL off), got %v", deleted)
	}
	if n := rowCountFor(t, db, "fleet_actions", "self"); n != 2 {
		t.Errorf("TTL=0: expected 2 action rows (no TTL), got %d", n)
	}
}

// TestBestEffortOneTableFailure verifies that a DELETE failure on one table is
// logged and skipped — other tables continue processing and no error is returned.
func TestBestEffortOneTableFailure(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.duckdb")
	createRemoteTables(t, remote)

	// Seed missions (multiple versions for id=1) and old actions (will be TTL'd).
	mustDuckDB(t, remote, `
		INSERT INTO fleet_missions VALUES
		  ('self', 1, 'dir', 'open', '', '', '', 0, 0.0, 1.0),
		  ('self', 1, 'dir', 'done', '', '', '', 0, 0.0, 2.0)`)
	mustDuckDB(t, remote, `
		INSERT INTO fleet_actions VALUES
		  ('self', 1, `+fmtF(tsOld)+`, 'hawk', 'claim'),
		  ('self', 5, `+fmtF(tsOld)+`, 'hawk', 'done')`)

	// Drop fleet_missions to force a failure on the first operation.
	{
		db, err := sql.Open("duckdb", remote)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DROP TABLE fleet_missions"); err != nil {
			db.Close()
			t.Fatalf("drop fleet_missions: %v", err)
		}
		db.Close()
	}

	cfg := RetentionConfig{TTLDays: 30}
	deleted, err := Compact(cfg, remote, "self", retentionNow)

	// Best-effort: no fatal error returned.
	if err != nil {
		t.Fatalf("Compact must not return a fatal error on a single table failure, got: %v", err)
	}

	// Other tables (fleet_actions TTL) still ran despite the missions failure.
	// id=1 (old, not max-id) must have been deleted; id=5 (old, max-id) survives.
	db := remoteDB(t, remote)
	defer db.Close()

	var countA1 int
	db.QueryRow("SELECT count(*) FROM fleet_actions WHERE brain='self' AND id=1").Scan(&countA1)
	if countA1 != 0 {
		t.Errorf("best-effort: actions id=1 should be TTL'd even after missions failure, still present")
	}
	if n := deleted["fleet_actions"]; n != 1 {
		t.Errorf("best-effort: expected 1 action row deleted, got %d", n)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// fmtF formats a float64 constant for safe embedding in a DuckDB INSERT literal.
func fmtF(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64)
}
