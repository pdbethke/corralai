# Fleet Reporting Layer (brains → MotherDuck) — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** #19

## Where this fits

`internal/fleet` already replicates one thing — the coordination **audit/action stream**
(`coord.audit`) — to a remote DuckDB / MotherDuck database, tagging each row with a
`brainID` so multiple brains federate into one place. This sub-project **extends that
seed** from a single hardcoded table to a **curated operational-metadata reporting set**:
missions, phases, tasks, and execution telemetry, still `brainID`-tagged, still
incremental. The result is that every brain reports a rich, uniform observability picture
up to a shared MotherDuck, where reports and dashboards span **all swarms in the fleet**.

It is the **data plane** of the fleet-observability vision. Two layers build on it later:
the free-form **oracle** (#20) that answers natural-language questions by querying this
data (local per-swarm, or `md:` fleet-wide), and — much further out, deliberately not here
— governed **cross-swarm coordination**. This spec ships only the reporting *up*; reading
back the fleet view is #20, and it is safe precisely because what is synced is metadata.

## First principle: metadata-only, classification enforced by construction

The single non-negotiable: **only operational metadata leaves the brain.** No source code
(the `repoindex` code chunks), no memory bodies, no reference text, no raw command output,
no secrets, no live coordination locks. This is not enforced by a prompt or a reviewer — it
is enforced by **construction**: the sync's fixed list of table specs is the *only* thing
that can ever cross the boundary, the content stores (`memory`/`reference`/`repoindex`) are
**never** in the attach set, and each spec selects an explicit allowlist of columns. What a
remote (and therefore any other brain, and MotherDuck's UI) can see is exactly this list —
nothing more can leak because nothing more is read.

This is also what makes cross-swarm *reading* (#20) safe to hand out freely: a brain seeing
"swarm B opened PR #7 on repo X, phase 2 of 4 done" reveals operations, never content.

## Architecture

Reuse the existing in-memory-DuckDB bridge unchanged in shape; generalize the payload from
one INSERT to a **list of curated table-sync specs**.

```
[brain ticker, CORRALAI_MOTHERDUCK set]  fleet.Sync(cfg, remoteAttach, brainID):
  in-mem duckdb: INSTALL sqlite  (+ INSTALL/LOAD motherduck if remoteAttach starts "md:")
  ATTACH coord, mission, queue    (TYPE sqlite, READ_ONLY)
  ATTACH telemetry                (duckdb file, READ_ONLY)     ← metadata store only
  ATTACH remoteAttach AS remote   (local .duckdb in tests, md:<db> in prod)
  for each spec in tableSpecs:
     CREATE TABLE IF NOT EXISTS remote.<spec.remote> (...)
     lastCursor := SELECT max(<spec.cursor>) FROM remote.<spec.remote> WHERE brain = brainID
     INSERT INTO remote.<spec.remote>
        SELECT '<brainID>', <spec.curatedCols> FROM <spec.src>
        WHERE <spec.cursor> > lastCursor ORDER BY <spec.cursor>
→ MotherDuck accumulates brainID-tagged operational metadata across every swarm
→ cross-swarm dashboards in MotherDuck's own UI immediately; #20 oracle queries it later
```

The content stores (`memory`, `reference`, `repoindex`) are conspicuously **absent** from
the attach set — that absence is the classification boundary.

## Components

### 1. `internal/fleet/sync.go` — generalize `Sync`

- `type SyncConfig struct { Coord, Mission, Queue, Telemetry string }` — the source DB
  paths (already constructed in `cmd/corral`). Empty paths are skipped (their specs are
  simply not run), so a brain without, say, a telemetry DB still syncs the rest.
- `func Sync(cfg SyncConfig, remoteAttach, brainID string) (int, error)` — the signature
  grows from `(coordDBPath, remoteAttach, brainID)` to take the config struct. Returns the
  total rows synced across all specs.
- `type tableSync struct { remote, createDDL, srcAlias, src, curatedCols, cursor string }`
  — an internal, package-level list. Each entry names its remote table, its DDL, the
  attached-alias.table it reads, the **explicit curated column list**, and the incremental
  cursor column. The existing `fleet_actions` (from `coord.audit`) becomes one entry.
- `Sync` attaches each configured store read-only, ensures every remote table, and runs the
  per-spec incremental INSERT (max-cursor-per-brain, ordered). A spec whose source store is
  unconfigured/unattachable is logged and skipped; the rest proceed.

### 2. The curated specs (metadata-only allowlist)

Exact columns pinned to what the stores expose (verified against the schemas during
implementation); the intent:

- `fleet_actions` ← `coord.audit` — **existing**: `id, ts, agent_name, action, detail`.
- `fleet_missions` ← `mission.missions` — `id, directive, status, repo, branch, pr_url,
  review_rounds, review_parked, created_ts, updated_ts`. (No secret columns exist on this
  table; the allowlist is explicit regardless.)
- `fleet_phases` ← `mission.phases` — `mission_id, name, status, created_ts, updated_ts`.
- `fleet_tasks` ← `queue.tasks` — `id, mission_id, status, agent_name, created_ts`
  (assignment/status/timing — no payload bodies).
- `fleet_telemetry` ← `telemetry.<exec table>` — `id, mission_id, agent, command_summary,
  exit_code, duration_ms, ok, ts` (redacted summary + result only — **no raw stdout/stderr
  bodies**).

Cursor is `id` where monotonic, else `updated_ts`/`ts`. Each remote row carries the
`brain` tag first.

### 3. `cmd/corral` — pass the extra paths

The MotherDuck ticker already exists (`CORRALAI_MOTHERDUCK`, `CORRALAI_MOTHERDUCK_TOKEN`,
`CORRALAI_BRAIN_ID` default hostname, interval). Only change: build the `fleet.SyncConfig`
from the already-constructed `coordDBPath`/`missionDB`/`queueDB`/telemetry paths and pass it
to the widened `fleet.Sync`. Ticker, brainID, token export, and the disabled-when-unset
behavior are unchanged.

## Data classification enforcement (the pillar)

- The `tableSync` list is the **allowlist** — the only tables/columns that can ever cross.
- Content stores (`memory`/`reference`/`repoindex`) are **never attached** — a code chunk
  or memory body has no path to the remote.
- Each `curatedCols` is an explicit column list, never `SELECT *`.
- A test asserts (a) the attach set contains no content store, and (b) each spec's
  `curatedCols` is a subset of a pinned allowlist (so a future edit that adds a sensitive
  column fails the test).
- Secrets never live in these tables to begin with (the token lives only in
  `cmd/corral`/`repo.Engine`), so there is no secret column to exclude — but the allowlist
  makes the guarantee structural rather than incidental.

## Error handling / edge cases

- **A source store missing/unattachable** → log, skip that spec, continue the others
  (partial sync is fine and self-heals next cycle).
- **Remote unreachable / bad MotherDuck token** → log, skip this cycle; the per-(brain,
  table) cursor means nothing is lost and nothing is duplicated on retry.
- **`md:` target without `CORRALAI_MOTHERDUCK_TOKEN`** → clear error, sync stays off.
- **Idempotent / incremental** → re-running `Sync` inserts only rows past each remote table's
  max cursor for this brain; safe to run on any cadence.
- **Two brains, same remote** → federate: each brain's rows are tagged and its cursor is
  computed `WHERE brain = brainID`, so brains never clobber or skip each other's rows.
- **Schema drift on a store** → the curated select names columns explicitly; a renamed
  source column surfaces as a clear error on that spec (logged, skipped), not silent
  corruption.

## Testing

Extend `internal/fleet/sync_test.go`, using a **local `.duckdb` file as the remote** (no
network / no MotherDuck needed):

- Seed coord/mission/queue SQLite + a telemetry DuckDB with a few rows; run `Sync`; assert
  every `fleet_*` table holds the curated rows, `brain`-tagged, with exactly the allowlisted
  columns and correct values.
- **Incremental:** run `Sync` twice → the second run inserts 0 rows (no duplicates).
- **Federation:** run `Sync` with `brainID="A"` then `brainID="B"` (different seeded rows) →
  the remote holds both brains' rows, distinguishable by `brain`, cursors independent.
- **Partial config:** an empty `Telemetry` path → the other tables still sync, no error.
- **Classification test (security-critical):** assert the attach set / spec list contains no
  `memory`/`reference`/`repoindex` source, and that each spec's `curatedCols` ⊆ the pinned
  allowlist — so adding a content store or a non-allowlisted column fails CI.

## Out of scope (follow-ups)

- **The oracle (#20)** — free-form NL → text-to-SQL over the local unified view and/or `md:`.
- **Content sync** (memory bodies, reference text, repoindex code) — a separate, opt-in
  classification expansion; not metadata.
- **A per-table classification *policy engine*** — the allowlist is hardcoded here; a
  configurable policy is a later refinement.
- **Cross-swarm *coordination*** (brains acting on each other's state) — a future
  sub-project needing its own authority + dedup + cross-swarm DoS governance.
- **The dashboards themselves** — built MotherDuck-side by the human; this spec only lands
  the data.
