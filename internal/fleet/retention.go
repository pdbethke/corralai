// SPDX-License-Identifier: Elastic-2.0

package fleet

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// RetentionConfig controls per-brain fleet table compaction and TTL behaviour.
type RetentionConfig struct {
	Disabled bool // if true, Compact is a no-op
	TTLDays  int  // 0 => TTL off (mission compaction still runs)
}

// RetentionConfigFromEnv builds a RetentionConfig from well-known env vars.
// Defaults: TTLDays=90. Set CORRALAI_FLEET_RETENTION_DISABLE=1 to disable entirely.
// Set CORRALAI_FLEET_RETENTION_DAYS=N (N >= 0) to override the TTL window; 0 turns
// off the TTL step while leaving mission compaction active.
func RetentionConfigFromEnv() RetentionConfig {
	c := RetentionConfig{TTLDays: 90}
	if os.Getenv("CORRALAI_FLEET_RETENTION_DISABLE") == "1" {
		c.Disabled = true
	}
	if v, err := strconv.Atoi(os.Getenv("CORRALAI_FLEET_RETENTION_DAYS")); err == nil && v >= 0 {
		c.TTLDays = v
	}
	return c
}

// Compact runs the per-brain fleet data-lifecycle against the remote store:
//
//  1. Mission compaction — deduplicate fleet_missions to keep only the latest
//     updated_ts row per (brain, id). Cursor-safe by construction: the max(updated_ts)
//     row per mission is never removed.
//
//  2. Append-stream TTL (only when cfg.TTLDays > 0) — purge rows older than the
//     TTL window from fleet_actions, fleet_telemetry, and fleet_tasks. Each DELETE
//     includes an explicit max-cursor guard so the brain's global max-cursor row is
//     preserved even when it falls outside the TTL window, protecting the #19
//     incremental-sync watermark.
//
// Operations are scoped to brainID — no other brain's rows are ever touched.
// Each DELETE is best-effort: a single failure is logged and the loop continues;
// the function never returns a fatal error for a per-table failure.
// Returns a map of table → rows deleted (nil when Disabled).
func Compact(cfg RetentionConfig, remoteAttach, brainID string, now time.Time) (map[string]int, error) {
	if cfg.Disabled {
		return nil, nil
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("fleet retention: open in-mem db: %w", err)
	}
	defer db.Close()

	// Mirror Sync's connection recipe: only install motherduck when the attach
	// string is an md: URI (tests use plain .duckdb paths).
	if strings.HasPrefix(remoteAttach, "md:") {
		for _, s := range []string{"INSTALL motherduck", "LOAD motherduck"} {
			if _, err := db.Exec(s); err != nil {
				return nil, fmt.Errorf("fleet retention: load motherduck: %w", err)
			}
		}
	}

	// Writable ATTACH — matches what Sync uses for INSERT; default in DuckDB is
	// read-write, so no extra flag needed.
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS remote", esc(remoteAttach))); err != nil {
		return nil, fmt.Errorf("fleet retention: attach remote: %w", err)
	}

	deleted := map[string]int{}

	// ── 1. Mission compaction ──────────────────────────────────────────────────
	// For each (brain, id) pair, keep only the row with the highest updated_ts.
	// The correlated subquery makes it self-referential: a row is only deleted when
	// a later version of the same mission exists.  Cursor safety is structural —
	// the max(updated_ts) row per mission can never satisfy the strict-less-than.
	retentionExec(db, deleted, "fleet_missions", `
		DELETE FROM remote.fleet_missions m
		WHERE  m.brain = ?
		  AND  m.updated_ts < (
		           SELECT max(m2.updated_ts)
		           FROM   remote.fleet_missions m2
		           WHERE  m2.brain = m.brain
		           AND    m2.id    = m.id
		       )`, brainID)

	// ── 1b. fleet_intents expired-claims cleanup ───────────────────────────────
	// Unconditionally purge claims whose expires_ts < now — they are lapsed
	// coordination signals regardless of the TTL window.
	// Cursor column: ts.  Cursor-safe: the max(ts) row per brain_id is preserved
	// even if it has expired (it carries the sync/resume watermark for the table).
	// Note: fleet_intents uses brain_id (not brain) as the per-brain column.
	retentionExec(db, deleted, "fleet_intents", `
		DELETE FROM remote.fleet_intents t
		WHERE  t.brain_id = ?
		  AND  t.expires_ts < ?
		  AND  t.ts < (SELECT max(t2.ts) FROM remote.fleet_intents t2 WHERE t2.brain_id = t.brain_id)`,
		brainID, float64(now.Unix()))

	// ── 2. Append-stream TTL ───────────────────────────────────────────────────
	if cfg.TTLDays > 0 {
		cutoff := float64(now.AddDate(0, 0, -cfg.TTLDays).Unix())

		// fleet_actions: cursor=id, TTL column=ts.
		// Guard: t.id < max(id) ensures the highest-id row (sync watermark) survives
		// even when its ts is older than the cutoff.
		retentionExec(db, deleted, "fleet_actions", `
			DELETE FROM remote.fleet_actions t
			WHERE  t.brain = ? AND t.ts < ?
			  AND  t.id < (SELECT max(t2.id) FROM remote.fleet_actions t2 WHERE t2.brain = t.brain)`,
			brainID, cutoff)

		// fleet_telemetry: cursor=id, TTL column=ts. Same guard shape as fleet_actions.
		retentionExec(db, deleted, "fleet_telemetry", `
			DELETE FROM remote.fleet_telemetry t
			WHERE  t.brain = ? AND t.ts < ?
			  AND  t.id < (SELECT max(t2.id) FROM remote.fleet_telemetry t2 WHERE t2.brain = t.brain)`,
			brainID, cutoff)

		// fleet_tasks: cursor=done_ts, TTL column=done_ts.
		// Guard: t.done_ts < max(done_ts) preserves the row with the highest done_ts
		// (the sync watermark) even when that row's timestamp is past the TTL window.
		retentionExec(db, deleted, "fleet_tasks", `
			DELETE FROM remote.fleet_tasks t
			WHERE  t.brain = ? AND t.done_ts < ?
			  AND  t.done_ts < (SELECT max(t2.done_ts) FROM remote.fleet_tasks t2 WHERE t2.brain = t.brain)`,
			brainID, cutoff)

		// fleet_intents: cursor=ts, TTL column=ts.
		// Removes old-by-age claims past the TTL window, complementing the
		// expired-claims cleanup above (which already ran outside this block).
		// Guard: t.ts < max(ts) preserves the highest-ts row as the watermark.
		// Note: uses brain_id column (not brain).
		retentionExec(db, deleted, "fleet_intents", `
			DELETE FROM remote.fleet_intents t
			WHERE  t.brain_id = ? AND t.ts < ?
			  AND  t.ts < (SELECT max(t2.ts) FROM remote.fleet_intents t2 WHERE t2.brain_id = t.brain_id)`,
			brainID, cutoff)
	}

	return deleted, nil
}

// retentionExec runs a single retention DELETE best-effort.  On error the failure
// is logged and the function returns without updating the deleted map, allowing the
// caller to continue with the remaining tables.
// A missing fleet_* table (brain that never synced that stream) is an expected,
// normal skip — the "skipped this cycle" log line is informational, not an anomaly.
func retentionExec(db *sql.DB, deleted map[string]int, table, query string, args ...any) {
	res, err := db.Exec(query, args...)
	if err != nil {
		log.Printf("fleet retention: %s delete failed (skipped this cycle): %v", table, err)
		return
	}
	if n, err := res.RowsAffected(); err == nil {
		deleted[table] += int(n)
	}
}
