# Fleet Data-Lifecycle: Compaction + Retention — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** D (fleet_* retention/compaction, #19 follow-up)

## Problem

The #19 fleet reporting layer syncs curated metadata from every brain into a shared MotherDuck,
into four `fleet_*` tables that only grow:
- **`fleet_missions`** is **temporal-versioned** — a mission accrues a NEW row on every status /
  branch / PR / review-round update (5–7 rows over its life). The #20 oracle's
  `fleet_missions_current` view (`row_number() OVER (PARTITION BY brain,id ORDER BY updated_ts
  DESC)=1`) re-scans *all* those versions on *every* query, so its hottest scan grows with total
  historical versions, not live missions.
- **`fleet_actions` / `fleet_tasks` / `fleet_telemetry`** are pure **append streams** (one row per
  audit event / completed task / telemetry event, forever). `fleet_telemetry` is the fastest
  grower (~3–5× the other tables per mission).

There is **no retention anywhere** today. At current single-brain scale this is trivially small,
but a fleet-reporting plane with no data lifecycle is an obvious gap for a system meant to run
many brains over time — and the `fleet_missions` version bloat degrades the oracle *now*.

**Goal:** a per-brain, cursor-safe data-lifecycle step that (a) **compacts** the versioned
`fleet_missions` to its latest row per mission, and (b) applies **time-based TTL retention** to
the append streams — running on the sync cadence, touching only the running brain's own rows,
and structurally unable to corrupt the incremental-sync cursor.

## First principles

1. **Cursor safety is the load-bearing invariant.** #19's incremental sync reads
   `max(<cursor>) FROM remote.fleet_* WHERE brain = <self>` as its watermark and copies
   `<cursor> > floor`. If a compaction/retention DELETE ever removes the brain's **global
   max-cursor row** for a table, `max(<cursor>)` drops (or goes NULL → floor −1) and the NEXT
   sync **re-copies everything**, duplicating rows (no uniqueness constraint on any `fleet_*`
   table). So every DELETE must **preserve the row holding `max(<cursor>)` for that brain** — by
   construction, verified by test.
2. **Per-brain, no coordination.** Each brain owns its `brainID`-tagged rows; every DELETE is
   scoped `WHERE brain = <self>`. No cross-brain locking or coordinator.
3. **Off is a first-class state.** Retention is configurable and can be fully disabled; disabled
   ⇒ no DELETE ever runs (today's behavior, unchanged).

## Per-table policy

| Table | Cursor | Growth | Policy |
|---|---|---|---|
| `fleet_missions` | `updated_ts` | versioned | **Compact:** keep the latest `updated_ts` per `(brain,id)`; delete older versions. |
| `fleet_actions` | `id` | append | **TTL:** delete rows older than the window (by `ts`), except the `max(id)` row. |
| `fleet_tasks` | `done_ts` | append | **TTL:** delete rows older than the window (by `done_ts`), except the `max(done_ts)` row. |
| `fleet_telemetry` | `id` | append | **TTL:** delete rows older than the window (by `ts`), except the `max(id)` row. |

- **Mission compaction is cursor-safe by construction:** keeping the latest version of *every*
  mission preserves the globally-latest `updated_ts` (it belongs to the most-recently-updated
  mission, whose latest row is kept). No special-case needed beyond "keep latest per id."
- **TTL preserves the max-cursor row explicitly:** the append streams' cursor (`id`/`done_ts`) is
  not the TTL time column, so a brain that went quiet could have its newest-cursor row also be
  "old." Each TTL DELETE therefore excludes `WHERE <cursor> = (SELECT max(<cursor>) FROM t WHERE
  brain=<self>)`. This guarantees the watermark row always survives regardless of the window.

## Architecture

A new step in `internal/fleet` — `Compact(cfg, remoteAttach, brainID)` (or `Retain`) — mirroring
`Sync`'s existing recipe: open a fresh in-memory DuckDB, `INSTALL/LOAD motherduck`, `ATTACH 'md:'
AS remote`, run the four scoped DELETEs, discard the in-mem DB. It is invoked from the same place
as `Sync` (`cmd/corral` fleet ticker), either right after each `Sync` or on a slower multiple of
the sync interval (retention doesn't need 30s granularity).

**Compaction DELETE (fleet_missions):**
```sql
DELETE FROM remote.fleet_missions m
WHERE m.brain = ?
  AND m.updated_ts < (SELECT max(m2.updated_ts) FROM remote.fleet_missions m2
                      WHERE m2.brain = m.brain AND m2.id = m.id)
```
(keeps exactly the latest row per `(brain,id)`; the global max survives).

**TTL DELETE (per append table, e.g. fleet_telemetry):**
```sql
DELETE FROM remote.fleet_telemetry t
WHERE t.brain = ?
  AND t.ts < ?                                   -- cutoff = now − window
  AND t.id  < (SELECT max(t2.id) FROM remote.fleet_telemetry t2 WHERE t2.brain = t.brain)
```
(the `id <` max-guard keeps the watermark row even if it's older than the cutoff; `ts` cutoff and
`now` are passed in — the codebase forbids `Date.now()`-style calls in some contexts, so `now` is
computed in `cmd/corral` and passed down, consistent with #19's timestamp handling).

## Config

- `CORRALAI_FLEET_RETENTION_DAYS` — TTL window for the append streams (default **90**; `0` =
  disable TTL). Compaction of `fleet_missions` is independently useful and cheap; gate it behind
  the same "retention enabled" switch or a `CORRALAI_FLEET_COMPACT=1` (default on) — decide in the
  plan, but a single `CORRALAI_FLEET_RETENTION_DISABLE=1` master-off is required so an operator can
  turn the whole lifecycle step off.
- `CORRALAI_FLEET_RETENTION_INTERVAL` (optional) — how often the step runs relative to sync
  (default: every sync, or every Nth — plan decides). Keep simple.

## Error handling / edge cases

- **Retention disabled / no MotherDuck configured** → the step is a no-op (no ATTACH, no DELETE).
- **A DELETE fails** (transient MotherDuck error) → log + skip that table this cycle; NEVER abort
  the daemon or the sync loop. Retention is best-effort; the next cycle retries. Sync correctness
  does not depend on retention succeeding.
- **Empty / single-row table for the brain** → the max-cursor guard means the DELETE affects 0
  rows; harmless.
- **Concurrent with Sync** — retention runs from the same fleet goroutine as Sync (serialized),
  not a second concurrent writer, so it can't race the sync INSERTs. (If run on its own ticker,
  it must share the fleet goroutine / be serialized with Sync — the plan ensures this.)
- **Other brains' rows** — every DELETE is `WHERE brain = <self>`; another brain's rows are never
  touched. A cross-brain test asserts this.
- **The oracle** — reads are unaffected; compaction only removes rows the `fleet_missions_current`
  view already discards, so oracle answers are identical, just faster.

## Testing

- **Mission compaction:** seed a `fleet_missions` (local DuckDB standing in for `remote`) with
  several `updated_ts` versions of two missions for this brain (+ rows for ANOTHER brain);
  compact; assert **only the latest version of each of this brain's missions survives**, the other
  brain's rows are **untouched**, and the **global `max(updated_ts)` for this brain is unchanged**
  (the cursor-safety property — the exact thing that prevents a re-sync).
- **TTL retention (each append table):** seed old + recent rows (+ the max-cursor row deliberately
  made "old"); apply TTL; assert old rows are gone, recent rows remain, and **the `max(id)` /
  `max(done_ts)` row for the brain SURVIVES even though it's older than the cutoff** (the
  re-sync-safety guard). Cross-brain rows untouched.
- **Cursor-safety end-to-end:** after compaction+TTL, compute `max(<cursor>)` per table for the
  brain and assert it equals the pre-retention max (so a subsequent #19 sync sees the same
  watermark and does NOT re-copy).
- **Disabled:** `CORRALAI_FLEET_RETENTION_DISABLE=1` (or days=0 for TTL) → no rows deleted.
- **Best-effort:** a DELETE error on one table doesn't prevent the others / doesn't error the loop.

## Out of scope (follow-ups)

- **Cross-brain / global compaction** (a coordinator that compacts on behalf of departed brains) —
  per-brain is sufficient; a brain that never returns leaves its rows, which is acceptable.
- **Archival to cold storage** before delete — straight delete for v1.
- **Local (SQLite/DuckDB source) store retention** — the source stores (coord.audit, missions,
  queue, telemetry) are a separate lifecycle concern; D is scoped to the shared `fleet_*` plane.
- **Compaction of `fleet_missions` on the LOCAL source** — the source `missions` table is one row
  per mission already (not versioned); only the *synced* fleet copy is versioned.
