# Threshold-Gated ANN Vector Index Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An HNSW (DuckDB `vss`) accelerator for `reference` + `memory` semantic search that activates ONLY above a per-store row threshold, imposes zero cost/constraint below it, and never changes search correctness (exact re-rank behind ANN candidate generation). `repoindex` is excluded.

**Architecture:** T1 builds a standalone `internal/annindex` helper (load vss, threshold-gated conditional migration + HNSW create/rebuild, staleness) validated against the real DuckDB `vss`. T2 wires the cleanest store (`reference` — no filters) to prove the query path. T3 wires `memory` (filters applied in the exact re-rank) + config + a `repoindex`-untouched guard.

**Tech Stack:** Go 1.26; DuckDB 1.4.1 `vss` extension (HNSW), CGO; `internal/{annindex,reference,memory}`.

## Global Constraints (bind every task)

- **Correctness never depends on the index.** HNSW only generates candidates; the store re-ranks with the EXISTING exact `list_cosine_similarity` and applies all filters, so ANN results equal brute-force results. An "ANN top-k == brute-force top-k" agreement test is the proof.
- **Graceful fallback everywhere — search NEVER breaks.** `vss` unavailable, below threshold, mixed-dimension rows, a failed migration, or a query/column dim mismatch → the existing brute-force path. Every ANN failure degrades, never errors out of search.
- **`repoindex` is untouched** (permanently `mission_id`-bounded; a guard test asserts no HNSW there).
- **Threshold-gated:** `CORRALAI_ANN_THRESHOLD` (default 20000) rows before HNSW activates per store; `CORRALAI_ANN_DISABLE=1` forces brute-force always.
- **HNSW-on-file-backed-DB gate:** set `SET hnsw_enable_experimental_persistence = true` after `LOAD vss` (else `CREATE INDEX … USING HNSW` errors on a persistent DB); treat the index as rebuildable (call `Ensure` at `Open()` too).
- **PROBE the real DuckDB 1.4.1 `vss`** (like the tree-sitter task probed grammars): verify `INSTALL vss; LOAD vss;`, the persistence flag, `CREATE INDEX … USING HNSW (col) WITH (metric='cosine')`, the distance function name (`array_cosine_distance` vs `array_distance` with cosine metric), whether the index is actually used (`EXPLAIN` shows an HNSW scan), and the `FLOAT[] → FLOAT[N]` migration (`ALTER COLUMN … TYPE FLOAT[N]`; if unsupported, add-column+copy+swap). Adapt + fall back on any failure.
- `go build ./...` + `go test ./...` stay green each task. CGO.

---

## File Structure

- `internal/annindex/annindex.go` (create) — `Loaded`, `Ensure`, `Stale`, `Config`.
- `internal/annindex/annindex_test.go` (create).
- `internal/reference/store.go` (modify) — load vss at Open; `Ensure` after Replace/Remove + at Open; HNSW query path in `Search`.
- `internal/memory/store.go` (modify) — load vss at Open; `Ensure` after Build + at Open; HNSW query path in `searchSemantic` with filters in re-rank.
- config read where the two stores are constructed (their `Open`/constructor reads `CORRALAI_ANN_THRESHOLD`/`_DISABLE`, or `cmd/corral` passes it).

---

## Task 1: `internal/annindex` — the core helper (probe DuckDB vss; threshold-gated migration + HNSW)

**Files:** Create `internal/annindex/annindex.go`, `internal/annindex/annindex_test.go`

**Interfaces produced:**
- `type Config struct{ Threshold int; Disabled bool }` + `func ConfigFromEnv() Config`
- `func Loaded(db *sql.DB) bool` — `INSTALL vss; LOAD vss; SET hnsw_enable_experimental_persistence=true;` once; false on any failure.
- `func Ensure(db *sql.DB, table, col, idxName string, cfg Config) (active bool, dim int, err error)` — threshold check → dim consistency → conditional `FLOAT[N]` migration → create/rebuild HNSW, or drop-if-below-threshold. Idempotent. Returns `active=false` (brute-force) on any non-fatal issue.
- `func Rebuild(db, table, col, idxName string, dim int) error` — drop + recreate the HNSW index (for staleness/deletes).

- [ ] **Step 1: PROBE** — write a throwaway `main`/test that opens an in-file DuckDB, `INSTALL vss; LOAD vss;`, sets the persistence flag, creates a table with `FLOAT[3]`, inserts a few rows, `CREATE INDEX … USING HNSW (v) WITH (metric='cosine')`, runs `EXPLAIN SELECT … ORDER BY array_cosine_distance(v, [..]::FLOAT[3]) LIMIT 2`, and confirms an HNSW index scan appears. Record the EXACT working SQL + distance-function name. Also probe `ALTER TABLE t ALTER COLUMN v TYPE FLOAT[3] USING v::FLOAT[3]` on a `FLOAT[]` column; if it errors, use add-column+copy+drop+rename. Use the WORKING forms below.

- [ ] **Step 2: Write failing tests**

```go
// internal/annindex/annindex_test.go
// Helper: open an in-file DuckDB (t.TempDir), create `docs(id BIGINT, embedding FLOAT[])`,
// seed N rows of a fixed dim D (small, e.g. 8) with random-ish but deterministic vectors.
//
// TestBelowThresholdNoIndex: seed 10 rows, Config{Threshold:100}. Ensure → active=false, no
//   HNSW index exists (query duckdb_indexes / catalog), column still FLOAT[] (LIST).
// TestAboveThresholdCreatesHNSW: seed 200 rows dim 8, Config{Threshold:100}. Loaded(db)==true
//   (skip test if vss can't load in CI — but it should). Ensure → active=true, dim=8, an HNSW
//   index named idxName exists, column migrated to FLOAT[8]. Calling Ensure again is idempotent
//   (no error, still active).
// TestMixedDimSkips: seed rows of dim 8 AND dim 4, above threshold. Ensure → active=false
//   (mixed dim), no migration, no index, no error.
// TestDisabledForcesBruteforce: Config{Disabled:true}, above threshold → Ensure active=false.
// TestRebuild: above threshold + index built; delete some rows; Rebuild → index recreated, no error.
```
> IMPLEMENTER: if `vss` genuinely cannot load in the test environment, the load-dependent tests may `t.Skip` WITH a logged reason — but confirm it loads (it's core in 1.4.1). Do NOT skip silently.

- [ ] **Step 3: Run red.**

- [ ] **Step 4: Implement `annindex.go`** (adapt SQL to the probe results)

```go
package annindex

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Threshold int
	Disabled  bool
}

func ConfigFromEnv() Config {
	c := Config{Threshold: 20000}
	if v, err := strconv.Atoi(os.Getenv("CORRALAI_ANN_THRESHOLD")); err == nil && v > 0 {
		c.Threshold = v
	}
	c.Disabled = os.Getenv("CORRALAI_ANN_DISABLE") == "1"
	return c
}

// Loaded installs+loads vss and enables HNSW persistence on a file-backed DB. Returns false
// (→ brute-force forever) on any failure. Call once at store Open().
func Loaded(db *sql.DB) bool {
	for _, s := range []string{
		"INSTALL vss", "LOAD vss",
		"SET hnsw_enable_experimental_persistence = true",
	} {
		if _, err := db.Exec(s); err != nil {
			return false
		}
	}
	return true
}

// Ensure is idempotent maintenance: gate on threshold, verify one consistent dim, migrate
// FLOAT[]→FLOAT[N] if needed, create the HNSW index (or drop it if the store fell below
// threshold). active=false means "use brute-force" for any non-fatal reason.
func Ensure(db *sql.DB, table, col, idxName string, cfg Config) (active bool, dim int, err error) {
	if cfg.Disabled {
		return false, 0, nil
	}
	var n int
	if err = db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL", table, col)).Scan(&n); err != nil {
		return false, 0, nil // count failed → brute-force
	}
	if n < cfg.Threshold {
		_, _ = db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", idxName)) // shrank back under → no index
		return false, 0, nil
	}
	// one consistent dimension across all non-null rows?
	var lo, hi int
	if err = db.QueryRow(fmt.Sprintf("SELECT min(len(%s)), max(len(%s)) FROM %s WHERE %s IS NOT NULL", col, col, table, col)).Scan(&lo, &hi); err != nil || lo != hi || lo == 0 {
		return false, 0, nil // mixed / unknown dim → brute-force
	}
	dim = lo
	// migrate FLOAT[] → FLOAT[dim] if the column is still a LIST (probe-verified form; else the
	// add-column+copy+swap fallback). Wrap in a guard: on failure → brute-force.
	if err = migrateToArray(db, table, col, dim); err != nil {
		return false, 0, nil
	}
	if err = ensureHNSW(db, table, col, idxName); err != nil {
		return false, 0, nil
	}
	return true, dim, nil
}
// migrateToArray + ensureHNSW: use the exact SQL confirmed in Step 1 (ALTER COLUMN … TYPE
// FLOAT[dim], or add/copy/swap; CREATE INDEX IF NOT EXISTS <idxName> ON <table> USING HNSW
// (<col>) WITH (metric='cosine')). migrateToArray is a no-op if the column is already FLOAT[dim].
// Rebuild: DROP INDEX IF EXISTS + ensureHNSW.
```

- [ ] **Step 5: Run green + build** — `go test ./internal/annindex/ -v`; `go build ./...`; full `go test ./...`.
- [ ] **Step 6: Commit** — `git commit -m "feat(annindex): threshold-gated HNSW helper (load vss, conditional FLOAT[N] migration, create/rebuild)"`

---

## Task 2: wire `reference` (the query path — no filters)

**Goal:** reference loads vss, calls `Ensure` at Open + after Replace/Remove, and `Search` uses the HNSW candidate + exact re-rank path when active, else the unchanged brute-force scan. reference has NO filters — the cleanest place to prove the query path.

**Files:** Modify `internal/reference/store.go`; test alongside.

**Interfaces consumed:** `annindex.{Loaded,Ensure,Rebuild,ConfigFromEnv}`.

- [ ] **Step 1: Failing test**
  - `TestReferenceANNAgreesWithBruteforce`: build a store with a low threshold (inject `Config{Threshold: 50}`), seed > threshold consistent-dim chunks with a fake embedder, call the post-write `Ensure`; then a `Search(qv, k)` returns the **same top-k `Source`/`Text` set** as a direct brute-force `list_cosine_similarity` query over the same rows. Assert the HNSW index exists.
  - `TestReferenceBelowThresholdBruteforce`: < threshold → no index, brute-force, correct results.
  - (Use the existing reference test harness + fake embedder; make the threshold injectable for tests — e.g. a `Store` field defaulted from `ConfigFromEnv` but overridable.)

- [ ] **Step 2: Run red / Step 3: Implement**
  - `Open()`: `s.vss = annindex.Loaded(db)`; store `annindex.ConfigFromEnv()` (threshold/disable), injectable for tests.
  - After `Replace`/`Remove`: if `s.vss`, `active, dim, _ := annindex.Ensure(db, "chunks", "embedding", "ref_hnsw", cfg)`; cache `active`/`dim`. On `Remove` (a delete), also consider `annindex.Rebuild` per the staleness heuristic (or just rebuild on Remove — reference deletes are infrequent).
  - `Search`: if `active`, candidate-gen with the HNSW distance order + LIMIT `k*overfetch` (overfetch e.g. 4), then exact re-rank with `list_cosine_similarity` and take k — either as a wrapping SQL (`SELECT … FROM (… ORDER BY array_cosine_distance … LIMIT k*of) ORDER BY list_cosine_similarity DESC LIMIT k`) or fetch candidates + re-rank in Go. Else the unchanged brute-force query. Guard: if the query vector's dim != `dim`, use brute-force.

- [ ] **Step 4: Run green + build.**
- [ ] **Step 5: Commit** — `git commit -m "feat(reference): HNSW-accelerated Search above threshold with exact re-rank (brute-force fallback)"`

---

## Task 3: wire `memory` (filters in re-rank) + config + repoindex guard

**Goal:** memory loads vss, `Ensure` at Open + after Build, and `searchSemantic` uses the HNSW path with the `shared`/`project`/`type` filters applied in the exact re-rank so results equal brute-force-with-filter. Plus the config wiring and a repoindex-untouched guard.

**Files:** Modify `internal/memory/store.go`; test alongside. (Config already in `annindex`.)

- [ ] **Step 1: Failing test**
  - `TestMemoryANNFilteredAgreesWithBruteforce`: low threshold, seed > threshold entries (mix of shared/private, projects, types) with a fake embedder, `Build` (which triggers Ensure); a `Search(query, project, type, k, sharedOnly)` returns the **same results as brute-force-with-the-same-filter**. Assert HNSW exists.
  - `TestMemoryDisableSwitch`: `Config{Disabled:true}` (or `CORRALAI_ANN_DISABLE=1`) above threshold → brute-force, correct results.
  - `TestRepoindexNoHNSW` (in repoindex or a cross-package test): after indexing, assert the repoindex `chunks` table has NO HNSW index and its search is unchanged (repoindex must not import annindex).

- [ ] **Step 2: Run red / Step 3: Implement**
  - `Open()`: `s.vss = annindex.Loaded(db)` + cache config (injectable).
  - After `Build`: `active, dim, _ := annindex.Ensure(db, "mem", "embedding", "mem_hnsw", cfg)`; because `Build` is a full wipe+reinsert, `Ensure` (idempotent, rebuild-capable) recreates the index each Build when above threshold — acceptable; the memory corpus that's above 20k is the case where this matters.
  - `searchSemantic`: if `active`, HNSW candidate-gen (over-fetch generously since filters cut the set — e.g. `k*8`) then exact `list_cosine_similarity` re-rank WITH the `shared`/`project`/`type` `WHERE` clauses applied, take k. Else unchanged brute-force. Dim-mismatch guard → brute-force.
  - Confirm `internal/repoindex` does NOT import `annindex` (the guard test + a grep in final verification).

- [ ] **Step 4: Run green + build.**
- [ ] **Step 5: Commit** — `git commit -m "feat(memory): HNSW-accelerated semantic recall above threshold with filtered exact re-rank; ANN config + repoindex left brute-force"`

---

## Final verification

- [ ] `go build ./...` clean; `go test ./...` all PASS.
- [ ] **Correctness (the load-bearing check):** the ANN-vs-brute-force **agreement** tests pass for reference (unfiltered) AND memory (filtered) — same top-k. HNSW changed speed, not answers.
- [ ] **Graceful:** below threshold → no index/brute-force; mixed-dim → skip; `vss` unavailable → brute-force; `CORRALAI_ANN_DISABLE=1` → brute-force. None error out of search.
- [ ] **repoindex untouched:** grep confirms `internal/repoindex` does not import `annindex`; no HNSW index on its `chunks`; its tests unchanged.
- [ ] The HNSW persistence flag is set (index survives / is rebuilt at Open); threshold default is 20000; `CORRALAI_ANN_THRESHOLD` overrides.
- [ ] Report the probe results (the exact working vss SQL + distance function + migration form used).
