# Threshold-Gated ANN Vector Index — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** C (VSS/ANN vector index)

## Problem

All three vector stores (`internal/memory`, `internal/reference`, `internal/repoindex`) do
semantic search by brute-force cosine — `list_cosine_similarity(embedding, q::FLOAT[])` with a
full `ORDER BY score DESC LIMIT k` scan, no index. At current scale (memory: tens–hundreds;
reference: hundreds–low-thousands; repoindex: ~1k chunks/mission) this is *ample* — the design
docs say so. But the `reference` corpus (user-ingested PDFs/URLs) and the `memory` hive-mind can
grow to tens of thousands of vectors, at which point a full scan per query degrades.

**Goal:** an approximate-nearest-neighbor (HNSW via DuckDB's `vss` extension) accelerator that
**activates only when a store is genuinely large**, imposes **zero cost/constraint below that
threshold**, and **never breaks or changes the correctness of search** (it is a pure candidate-
generation accelerator behind an exact re-rank).

## First principles

1. **Pay only at scale.** Below a per-store row threshold, storage stays `FLOAT[]` + brute-force
   — no fixed-dimension constraint, no migration, no index. Small/dev deployments are untouched.
2. **Correctness never depends on the index.** HNSW only generates *candidates*; the store
   re-ranks them with exact `list_cosine_similarity`, so approximate recall / ghost nodes never
   yield a wrong top-k — worst case a marginally different candidate set. `vss` unavailable,
   below threshold, or mixed-dimension rows → brute-force. Search never breaks.
3. **Apply it where it helps, not reflexively everywhere** (see scope below).

## Scope — which stores get HNSW (the load-bearing call)

- **`reference` — YES.** A *global, unfiltered* corpus that grows with ingestion — the one
  realistic path to tens-of-thousands of vectors where a full scan hurts. HNSW's cleanest case.
- **`memory` — YES.** Global-ish; its light filters (`shared`/`project`/`type`) are handled by
  over-fetching candidates from the index then applying the filter + exact re-rank.
- **`repoindex` — NO, deliberately.** Every repo search is already `WHERE mission_id = ?`-scoped
  to one mission's ~1k chunks *regardless of total missions*, so each query is permanently
  bounded and brute-force is always cheap. A shared HNSW index is also filter-blind, so the
  per-mission predicate would defeat it. Excluding it is correct; repoindex is untouched by C.

## Architecture

A small shared helper (the three stores are independent packages, so a tiny reusable unit,
`internal/annindex`, OR a per-store method following a shared recipe — the plan decides) that
each participating store (`memory`, `reference`) uses at two points:

**(a) Maintenance — after a write batch** (`memory.Build`, `reference.Replace`/`Remove`):
```
ensureANN(store):
  if !vssLoaded: return                       // extension unavailable → brute-force forever
  n := rowCount()
  if n < threshold:                           // below → ensure NO index (drop if shrank under)
      dropHNSWIfPresent(); return
  dim, consistent := embeddingDim()           // one dim across all non-null rows?
  if !consistent: log("mixed-dim, ANN skipped"); return
  if columnIsList():                           // FLOAT[] → FLOAT[N] one-time migration
      ALTER TABLE ... ALTER COLUMN embedding TYPE FLOAT[dim] USING embedding::FLOAT[dim]
  if !hnswIndexExists() || stale():
      CREATE INDEX <name> ON <table> USING HNSW (embedding) WITH (metric = 'cosine')
```

**(b) Query — in `searchSemantic`:**
```
if hnswActive:
    candidates = SELECT ... ORDER BY array_cosine_distance(embedding, q::FLOAT[dim]) LIMIT k*overfetch   // uses HNSW
    // then exact re-rank + filters, in Go or a wrapping SELECT:
    rank candidates by list_cosine_similarity(embedding, q), apply shared/project/type filters, take k
else:
    <the existing brute-force list_cosine_similarity full scan>   // unchanged
```
`array_cosine_distance` (ascending = nearest) is what the HNSW index accelerates; the exact
re-rank uses the existing `list_cosine_similarity` so scores/ordering match brute-force.

## Components / changes

### 1. `internal/annindex` (new, small) OR shared per-store methods
- `func Loaded(db) bool` — `INSTALL vss; LOAD vss;` once at store `Open()`, record success (the
  exact idiom `fts` uses); false → the store stays brute-force forever.
- `func Ensure(db, table, col string, threshold int) (active bool, dim int, err error)` — the
  maintenance step above (count, dim-consistency, conditional migration, create/rebuild HNSW,
  or drop-if-below). Idempotent; safe to call after every write batch.
- `func Stale(...)` heuristic — rebuild when the deleted-fraction estimate exceeds a bound (or
  simply rebuild on `Remove`/large-delete). Below threshold → no index → nothing to maintain.

### 2. `internal/memory/store.go` + `internal/reference/store.go`
- Load `vss` at `Open()` (flag `s.vss`).
- Call `Ensure(...)` at the end of `Build` (memory) and `Replace`/`Remove` (reference).
- `searchSemantic` (memory) / `Search` (reference): when HNSW is active, use the candidate-gen +
  exact-re-rank path; else the unchanged brute-force scan. The filter handling (memory's
  shared/project/type) is applied in the re-rank so results are identical to brute-force.

### 3. Config
- `CORRALAI_ANN_THRESHOLD` (default 20000) — rows before HNSW activates per store.
- `CORRALAI_ANN_DISABLE=1` — hard off (always brute-force), for operators who want it.

### 4. `internal/repoindex` — NO CHANGES (documented exclusion above).

## Error handling / edge cases

- **`vss` unavailable** (older DuckDB / extension blocked) → `Loaded`=false → brute-force; logged
  once, never an error.
- **Below threshold** → no migration, `FLOAT[]` kept, brute-force. The common case; zero cost.
- **Mixed-dimension rows** (post-model-change) → `Ensure` logs + skips migration/index; stays
  brute-force. Never a hard failure.
- **Migration on a large table** → one-time `ALTER COLUMN` cost when first crossing the
  threshold; acceptable (it's the scale where the index pays for itself). If the ALTER fails
  (e.g. a stray bad row), catch → log → brute-force.
- **Deletes / ghost nodes** → exact re-rank keeps top-k correct; staleness rebuild restores
  recall. A deleted row absent from the table can't appear in results.
- **Query dim ≠ column N** (a query embedded with a different model than the indexed rows) →
  detect + fall back to brute-force for that query (or refuse the ANN path); never a cast panic.
- **Concurrency** — index create/rebuild happens inside the store's existing write path (already
  serialized per store); reads use the index read-only.
- **HNSW persistence (a real DuckDB gate).** The stores are file-backed DuckDB databases, but
  DuckDB's HNSW index does not persist across restarts by default — `CREATE INDEX … USING HNSW`
  on a persistent DB **errors** unless `SET hnsw_enable_experimental_persistence = true` is set
  first. So: set that flag right after `LOAD vss` (part of `Loaded`), AND treat the index as
  **rebuildable** — `Ensure()` is called at `Open()` too, recreating the index if it's missing
  but the store is above threshold. If the flag/persistence proves unreliable in this build,
  fall back to "rebuild HNSW on every Open() when above threshold" (a bounded one-time cost at
  startup for a large store) — verify the flag's behavior during implementation and pick the
  robust path. Either way, `Loaded`/`Ensure` degrade to brute-force if HNSW can't be created.

## Testing

- **Below threshold:** seed < threshold rows → `Ensure` creates NO index, column stays `FLOAT[]`,
  search uses brute-force; results correct.
- **Above threshold (the core test):** seed > threshold **consistent-dim** rows → `Ensure`
  migrates to `FLOAT[N]` + creates HNSW; a `searchSemantic` returns the **same top-k** as a
  brute-force reference computation over the same data (ANN-vs-exact **agreement** on the seeded
  set — proves the accelerator doesn't change answers). Assert the HNSW index exists.
- **Filters (memory):** above threshold, a `shared`/`project`/`type`-filtered query returns the
  same results as brute-force-with-the-same-filter (re-rank applies filters correctly).
- **Mixed-dim:** seed rows of two dimensions → `Ensure` skips migration/index, stays brute-force.
- **`vss` unavailable:** `Loaded`=false → brute-force path; no error.
- **Delete/rebuild:** cross threshold, delete a chunk/source, assert the rebuild heuristic fires
  and search stays correct.
- **repoindex untouched:** a test asserting repoindex has no HNSW index and its search path is
  unchanged.
- **Disable switch:** `CORRALAI_ANN_DISABLE=1` → always brute-force even above threshold.

## Out of scope (follow-ups)

- **repoindex ANN** — permanently mission-bounded; excluded by design.
- **Background/async index build** — build inline at the maintenance point (the store's write
  path is already async/fire-and-forget for memory; acceptable).
- **Per-tenant index partitioning; re-embed on model change** — larger efforts.
- **Tuning HNSW params (`ef_construction`/`M`)** — use `vss` defaults for v1; expose later.
