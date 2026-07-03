# Fleet Data-Lifecycle (Compaction + Retention) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A per-brain, cursor-safe fleet-data-lifecycle step — compact the versioned `fleet_missions` to its latest row per mission, TTL the `fleet_actions`/`fleet_tasks`/`fleet_telemetry` append streams — that runs on the sync cadence and can never corrupt the #19 incremental-sync watermark.

**Architecture:** T1 builds the retention core in `internal/fleet` (the four scoped DELETEs + config + comprehensive cursor-safety tests, using a local DuckDB standing in for the `md:` remote). T2 wires it into the `cmd/corral` fleet goroutine on a coarse timer, serialized with `Sync`, best-effort.

**Tech Stack:** Go 1.26; DuckDB (CGO) + MotherDuck (`md:`); `internal/fleet`, `cmd/corral`.

## Global Constraints (bind every task)

- **CURSOR SAFETY IS LOAD-BEARING.** #19's sync watermark is `max(<cursor>) … WHERE brain=<self>` per table. A DELETE must NEVER remove the brain's global max-cursor row (would drop the watermark → NULL/lower floor → full re-sync → duplicate rows, since no `fleet_*` table has a uniqueness constraint). Mission compaction preserves it by construction (keep latest per id); TTL preserves it with an explicit `<cursor> < (SELECT max(<cursor>) …)` guard. A test asserts `max(<cursor>)` is unchanged after retention for every table.
- **Per-brain:** every DELETE is `WHERE brain = ?` (the running brain's `brainID`). Never touch another brain's rows (cross-brain test).
- **Best-effort:** a DELETE failure logs + skips that table for the cycle; NEVER aborts the fleet goroutine or affects `Sync`. Sync correctness does not depend on retention.
- **Fully disable-able:** `CORRALAI_FLEET_RETENTION_DISABLE=1` → the whole step is a no-op. `CORRALAI_FLEET_RETENTION_DAYS` (default 90; `0` = TTL off, compaction still runs).
- **`now` is injected** (computed in `cmd/corral`, passed down) — do not call wall-clock inside `internal/fleet` logic that must stay testable/deterministic (consistent with #19's timestamp handling).
- Retention runs from the SAME goroutine as `Sync` (serialized) — never a second concurrent writer.
- `go build ./...` + `go test ./...` stay green each task. CGO.

## Per-table policy (from the spec)

| Table | Cursor | Action | DELETE guard |
|---|---|---|---|
| `fleet_missions` | `updated_ts` | compact to latest per `(brain,id)` | `updated_ts < max(updated_ts) per (brain,id)` |
| `fleet_actions` | `id` | TTL by `ts` | `ts < cutoff AND id < max(id) per brain` |
| `fleet_tasks` | `done_ts` | TTL by `done_ts` | `done_ts < cutoff AND done_ts < max(done_ts) per brain` |
| `fleet_telemetry` | `id` | TTL by `ts` | `ts < cutoff AND id < max(id) per brain` |

---

## File Structure

- `internal/fleet/retention.go` (create) — `RetentionConfig`, `RetentionConfigFromEnv`, `Compact(...)`.
- `internal/fleet/retention_test.go` (create).
- `cmd/corral/main.go` (modify) — invoke `Compact` on a coarse timer in the fleet goroutine.

---

## Task 1: `internal/fleet` retention core (the four DELETEs + config + cursor-safety tests)

**Files:** Create `internal/fleet/retention.go`, `internal/fleet/retention_test.go`

**Interfaces produced:**
- `type RetentionConfig struct{ Disabled bool; TTLDays int }` + `func RetentionConfigFromEnv() RetentionConfig`
- `func Compact(cfg RetentionConfig, remoteAttach, brainID string, now time.Time) (deleted map[string]int, err error)` — opens an in-mem DuckDB, `INSTALL/LOAD motherduck`, `ATTACH '<remoteAttach>' AS remote`, runs the four scoped DELETEs best-effort, returns per-table deleted counts. `Disabled` → no-op (nil map, nil err). Mirror `Sync`'s connection recipe.

- [ ] **Step 1: Study `internal/fleet/sync.go`** — the in-mem-DuckDB open + `INSTALL motherduck; LOAD motherduck; ATTACH … AS remote`, how `remoteAttach`/`brainID` are passed, the `tableSpecs` (confirm the real column names: `updated_ts`, `id`, `ts`, `done_ts`, `brain`), and how `Sync` is called from `cmd/corral`. Match its idiom exactly.

- [ ] **Step 2: Write failing tests** (local DuckDB stands in for `remote` — create the four `fleet_*` tables with the #19 DDL, seed rows, and `ATTACH` a `:memory:`/temp DuckDB as `remote`, OR test the DELETE SQL against a plain table and inject the `remote.` alias via a test seam — mirror how `internal/fleet` sync tests stand in for the remote).

```go
// internal/fleet/retention_test.go (sketch — implement against the real test harness)
//
// setup: a DuckDB with remote.fleet_missions / fleet_actions / fleet_tasks / fleet_telemetry
// seeded for brainID "self" AND "other".
//
// TestCompactMissionsKeepsLatestPerMission:
//   seed mission id=1 with updated_ts {1,2,3}, id=2 with {5,6}; a row for brain "other".
//   Compact(cfg, remote, "self", now) → self's fleet_missions has exactly id=1@3 and id=2@6;
//   the "other" brain's rows are untouched; and max(updated_ts) for "self" == 6 (unchanged) —
//   the CURSOR-SAFETY assertion.
//
// TestTTLDropsOldKeepsWatermark (per append table):
//   seed fleet_telemetry for "self": rows with ts {old, old, recent} and ids {10, 50, 30}
//   where the MAX-id row (id=50) is deliberately OLD (ts=old). TTLDays makes cutoff exclude
//   the old ts. Compact → the two ts<cutoff rows are gone EXCEPT id=50 survives (max-id guard);
//   recent row remains; max(id) for "self" == 50 (unchanged) — cursor safety. "other" untouched.
//   Repeat the shape for fleet_actions (id/ts) and fleet_tasks (done_ts/done_ts).
//
// TestCursorSafetyEndToEnd: for ALL four tables, record max(<cursor>) per table for "self"
//   before Compact; after Compact assert each is unchanged (proves no re-sync would trigger).
//
// TestRetentionDisabledNoDeletes: cfg.Disabled → 0 rows deleted anywhere.
// TestTTLZeroSkipsTTLButCompacts: TTLDays=0 → append tables untouched, missions still compacted.
// TestBestEffortOneTableFailure: (if feasible) a malformed table / forced error on one DELETE
//   doesn't prevent the others and doesn't return a fatal error — logged, continue.
```

- [ ] **Step 3: Run red.**

- [ ] **Step 4: Implement `retention.go`**

```go
package fleet

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

type RetentionConfig struct {
	Disabled bool
	TTLDays  int // 0 => TTL off (compaction still runs)
}

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

// Compact runs the per-brain fleet data-lifecycle: compact fleet_missions to latest-per-mission
// and TTL the append streams. Best-effort per table; a single table's failure is logged and
// skipped. Cursor-safe: never deletes the brain's max-cursor row for any table.
func Compact(cfg RetentionConfig, remoteAttach, brainID string, now time.Time) (map[string]int, error) {
	if cfg.Disabled {
		return nil, nil
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	for _, s := range []string{"INSTALL motherduck", "LOAD motherduck",
		fmt.Sprintf("ATTACH '%s' AS remote (READ_ONLY FALSE)", remoteAttach)} { // match Sync's ATTACH (writable)
		if _, err := db.Exec(s); err != nil {
			return nil, fmt.Errorf("fleet retention attach: %w", err)
		}
	}
	deleted := map[string]int{}
	// 1. mission compaction (cursor-safe by construction)
	exec(db, deleted, "fleet_missions", `
		DELETE FROM remote.fleet_missions m
		WHERE m.brain = ?
		  AND m.updated_ts < (SELECT max(m2.updated_ts) FROM remote.fleet_missions m2
		                      WHERE m2.brain = m.brain AND m2.id = m.id)`, brainID)
	// 2. TTL append streams (only when TTLDays > 0), each preserving its max-cursor row
	if cfg.TTLDays > 0 {
		cutoff := float64(now.AddDate(0, 0, -cfg.TTLDays).Unix())
		exec(db, deleted, "fleet_actions", `
			DELETE FROM remote.fleet_actions t WHERE t.brain = ? AND t.ts < ?
			  AND t.id < (SELECT max(t2.id) FROM remote.fleet_actions t2 WHERE t2.brain = t.brain)`,
			brainID, cutoff)
		exec(db, deleted, "fleet_telemetry", `
			DELETE FROM remote.fleet_telemetry t WHERE t.brain = ? AND t.ts < ?
			  AND t.id < (SELECT max(t2.id) FROM remote.fleet_telemetry t2 WHERE t2.brain = t.brain)`,
			brainID, cutoff)
		exec(db, deleted, "fleet_tasks", `
			DELETE FROM remote.fleet_tasks t WHERE t.brain = ? AND t.done_ts < ?
			  AND t.done_ts < (SELECT max(t2.done_ts) FROM remote.fleet_tasks t2 WHERE t2.brain = t.brain)`,
			brainID, cutoff)
	}
	return deleted, nil
}

// exec runs one retention DELETE best-effort: on error, log + continue (never abort the cycle).
func exec(db *sql.DB, deleted map[string]int, table, query string, args ...any) {
	res, err := db.Exec(query, args...)
	if err != nil {
		log.Printf("fleet retention: %s delete failed (skipped this cycle): %v", table, err)
		return
	}
	if n, err := res.RowsAffected(); err == nil {
		deleted[table] = int(n)
	}
}
```
> IMPLEMENTER: confirm the real `ATTACH` form `Sync` uses (read-write, since retention DELETEs) and the `motherduck_token` handling (env, already set by `cmd/corral` for Sync — retention reuses it). Confirm the column names against `tableSpecs`. If the DuckDB `DELETE … USING a correlated subquery` form differs, adapt but keep the max-cursor guard exact. For tests, if attaching a second in-mem DuckDB as `remote` is awkward, create the `remote` schema/tables directly in the one in-mem DB (`CREATE SCHEMA remote; CREATE TABLE remote.fleet_missions …`) and call the same DELETEs — the SQL is identical.

- [ ] **Step 5: Run green + build** — `go test ./internal/fleet/ -v`; `go build ./...`; full `go test ./...`.
- [ ] **Step 6: Commit** — `git commit -m "feat(fleet): cursor-safe per-brain retention — fleet_missions compaction + append-stream TTL"`

---

## Task 2: wire retention into the `cmd/corral` fleet goroutine

**Files:** Modify `cmd/corral/main.go`

**Interfaces consumed:** `fleet.Compact`, `fleet.RetentionConfigFromEnv`.

- [ ] **Step 1: Study `startFleetSync`** (`cmd/corral/main.go` ~217-246) — the fleet goroutine + ticker, how `Sync` is called, `brainID`, the `remoteAttach`/`md:` target, `motherduck_token`.

- [ ] **Step 2: Implement** — in the SAME fleet goroutine (serialized with `Sync`), run `Compact` on a COARSE cadence (retention doesn't need the sync's 30s granularity). Simplest: a counter — run `Compact` every Nth sync tick (e.g. `retentionEvery := max(1, retentionIntervalSec/syncIntervalSec)`, default retention interval ~1h), OR a second `time.Ticker` selected in the same goroutine's loop. Either way, `Compact` and `Sync` never run concurrently (same goroutine). Pass `time.Now()` as `now`. Log the returned per-table deleted counts at a low frequency. Read `fleet.RetentionConfigFromEnv()` once at startup; if `CORRALAI_FLEET_RETENTION_DISABLE=1`, skip scheduling it entirely.
  ```go
  // sketch inside the fleet goroutine loop:
  retCfg := fleet.RetentionConfigFromEnv()
  // ... on the coarse cadence, serialized after a Sync:
  if !retCfg.Disabled {
      if del, err := fleet.Compact(retCfg, mdTarget, brainID, time.Now()); err != nil {
          log.Printf("fleet retention cycle error: %v", err)
      } else if len(del) > 0 {
          log.Printf("fleet retention: %v", del)
      }
  }
  ```

- [ ] **Step 3: Test** — a light wiring test if feasible (the core logic is fully tested in T1); at minimum confirm the goroutine compiles + `Compact` is reachable + disabled-config skips it. If a full integration test needs MotherDuck, assert the scheduling/gating logic in isolation instead and note it.

- [ ] **Step 4: Run green + build.**
- [ ] **Step 5: Commit** — `git commit -m "feat(corral): run fleet retention on a coarse cadence in the fleet goroutine (serialized with sync)"`

---

## Final verification

- [ ] `go build ./...` clean; `go test ./...` all PASS.
- [ ] **Cursor safety (the load-bearing check):** for ALL four tables, `max(<cursor>)` per brain is unchanged after `Compact` (the end-to-end test) — so a subsequent #19 sync sees the same watermark and does NOT re-copy. The TTL max-cursor guard is proven by the "max-id row is old but survives" test.
- [ ] Mission compaction keeps exactly the latest version per `(brain,id)`; append-stream TTL drops old rows within the window; disabled/TTL=0 honored; best-effort (one table's failure doesn't abort).
- [ ] Per-brain: another brain's rows are never touched (cross-brain test).
- [ ] Retention runs serialized with `Sync` (same goroutine), best-effort, on a coarse cadence.
