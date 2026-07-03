# Repo Code Index (DuckDB semantic/hybrid search) — Design

**Status:** design · **Date:** 2026-06-30 · **Sub-project:** #17

## Where this fits

Repo-work mode gives the brain an authoritative per-mission working copy; distributed
workspace lets remote bees mirror it. A bee can `grep` its local mirror for exact
strings, but it cannot ask "where is auth handled?" or "what already parses repo
URLs?" — semantic questions a substring search misses. This sub-project indexes the
brain's working copy into DuckDB (FTS + embeddings) and serves **hybrid + semantic
code search** over MCP, so bees ground on the codebase by meaning, not just by token.
It is the "uses DuckDB to index the repo" idea, built on the same engine pattern as
the memory and reference stores.

## First principle: reuse the proven engine recipe; degrade gracefully

This is the memory engine's recipe applied to code, not a new search architecture:
DuckDB `INSTALL fts; LOAD fts;` + `create_fts_index` for BM25, native
`list_cosine_similarity` for semantic, max-normalize + union-by-id `mergeHits` for the
blend, the shared `internal/embed` client for one cross-vendor vector space, and
vectors inlined via `embed.VecLiteral(...)::FLOAT[]` (the go-duckdb param-binding
workaround). As with memory, **no embedder configured → BM25 keyword floor**, never a
hard dependency or a failure.

## Architecture / data flow

```
clone / each gate-passed commit → index the changed files
  file text → line-window chunks (60 lines / 15 overlap, with start/end line)
            → embed batch (shared embedder) → upsert rows (idempotent per path)
  table chunks(mission_id, path, seq, start_line, end_line, text, embedding FLOAT[], ts)
  rebuild create_fts_index after an update batch

repo_search{query, k}  (caller's mission) →
  match_bm25  ⊕  list_cosine_similarity  → mergeHits (normalize, union, Via)
  → [{path, start_line, end_line, snippet, score, via}]
```

One dedicated `corralai_repocode.duckdb` (the one-file-per-engine convention), a
single `chunks` table partitioned by `mission_id` so concurrent missions share the DB
without colliding and a finished mission's rows can be dropped.

## Components

### 1. `internal/repoindex` — the engine

- `Open(path string) (*Store, error)` — opens the DB, creates the `chunks` table and
  the id sequence, runs `INSTALL fts; LOAD fts;` (sets an `fts` flag; on failure
  degrades to LIKE keyword search, exactly like memory).
- `SetEmbedder(*embed.Client)` — memory-style; the index owns its embedder (stateful,
  re-embeds on indexing). Nil ⇒ keyword floor.
- `IndexFiles(missionID int64, files []FileInput) error` where
  `FileInput{Path, Text string}` — for each file: chunk into line windows, embed the
  batch (when an embedder is set), then **idempotent upsert keyed by `(mission_id,
  path)`**: `DELETE FROM chunks WHERE mission_id=? AND path=?` then insert the new
  chunks (the reference `Replace`-by-source shape). After the batch, rebuild the FTS
  index. Embed failure for a file → store its chunks **without** vectors (BM25 still
  finds them) and log.
- `DropMission(missionID int64) error` — `DELETE FROM chunks WHERE mission_id=?`;
  called on mission completion to keep the DB bounded.
- `Search(missionID int64, query string, k int) ([]Hit, error)` — hybrid: always
  `searchKeyword` (BM25 via `match_bm25`, or LIKE fallback) scoped to `mission_id`; if
  an embedder is set, embed the query and `searchSemantic`
  (`list_cosine_similarity` over rows `WHERE mission_id=? AND embedding IS NOT NULL`);
  `mergeHits` (max-normalize each arm, union by `path:start_line`, keep higher score,
  tag `Via ∈ {keyword,semantic,both}`), truncate to `k` (default 8).
- `Hit{Path string; StartLine, EndLine int; Snippet string; Score float64; Via string}`.

### 2. Chunker — `chunkLines(text string, window, overlap int) []LineChunk`

Language-agnostic: split into overlapping `window`-line spans (default 60/15), each
carrying `StartLine`/`EndLine` (1-based) so hits are clickable `path:line` ranges and
`Snippet` is the chunk text (capped for display). No parser, no per-language code —
symbol-aware chunking is an explicit follow-up.

### 3. Indexing trigger (wired at the per-commit seam)

The mission engine already commits gate-passed phases (repo-work-mode). At that same
seam, after a successful commit, index the files that changed in the commit
(`git diff --name-only` of that commit → read each from the working copy →
`IndexFiles(missionID, …)`), so the index tracks the branch incrementally. The initial
clone does a full index (all files). `DropMission` runs when the mission completes.
The engine holds an optional `*repoindex.Store` (nil ⇒ indexing skipped entirely),
mirroring how `RepoOps` is optional.

### 4. Brain tool `repo_search` (`internal/brain/reposearch.go`)

`repo_search{query, k}` resolves the caller's claimed mission and calls
`Search(missionID, query, k)`, returning the hits. Registered only when the repo
engine is enabled; the semantic arm additionally requires an embedder (without one it
is BM25-only, still useful). `repo_grep` (exact substring, repo-work-mode) stays —
`repo_search` is its semantic/hybrid sibling, not a replacement.

### 5. Wiring (`cmd/corral`)

Construct `repoindex.Open(env CORRALAI_REPOCODE_DB default ~/.claude/corralai_repocode.duckdb)`,
`SetEmbedder(embedder)` (the same shared `embed.New()` client used by memory/reference
— one vector space), set it on the mission engine, and pass it to the brain for
`repo_search`. Log "repo code index enabled/disabled" like the RAG line.

## Performance / error handling

- **Full-scan cosine** over `FLOAT[]` (matches memory/reference) — fine for a single
  repo's working copy. **VSS/HNSW (or fixed `FLOAT[N]`) is a noted follow-up**, not
  now; the table header documents it.
- **No embedder** → keyword floor; no failure.
- **Embed failure on a file** → chunks stored without vectors (BM25 finds them);
  logged; a later reindex backfills.
- **Mixed-dimension rows** (embed model changed mid-stream) → excluded from cosine,
  never panic; a full reindex fixes it.
- **Index lags a commit** (indexing error) → search still returns the prior state;
  the error is logged, the mission is not blocked (search is an aid, not a gate).
- **Mission cleanup** → `DropMission` prevents unbounded growth across many missions.

## Testing

- **`internal/repoindex`:** `IndexFiles` then `Search` with a **fake deterministic
  embedder** — a query whose meaning matches a chunk with no lexical overlap surfaces
  via the semantic arm; an exact-token query surfaces via BM25; a chunk hit by both is
  tagged `both`. **Nil embedder → keyword-only, no error.** `IndexFiles` is idempotent
  per `(mission_id, path)` (re-indexing a changed file replaces its rows, doesn't
  duplicate). `DropMission` removes only that mission's rows. Line ranges in hits are
  correct (a known function lands in the expected `start_line`/`end_line`).
- **chunker:** `chunkLines` produces the expected window/overlap spans with correct
  1-based line numbers, including the final short window.
- **brain (`reposearch_test`):** `repo_search` over a seeded mission returns hits for
  the claiming bee and errors for a caller with no repo mission; unregistered when no
  repo engine.

## Out of scope (follow-ups)

- **Symbol-aware chunking** (tree-sitter / `go/parser`) — better relevance and hit
  labels; v1 is line-windows.
- **VSS / ANN index** for large corpora — v1 is full-scan, matching the existing
  engines.
- **Cross-mission / whole-repo persistent index** reused across missions — v1 is
  per-mission, dropped on completion.
- **Re-embed-only-on-content-change** optimization — v1 re-indexes changed files per
  commit, which already bounds the work.
