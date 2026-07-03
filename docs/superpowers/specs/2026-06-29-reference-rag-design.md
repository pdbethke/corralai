# Reference RAG Engine — Design (sub-project #7)

**Status:** design · **Date:** 2026-06-29

## Where this fits

The swarm models a dev org (#1–#6). The **researcher** role turns a directive
into requirements — but today it works only from the directive + shared memory.
#7 gives it **grounding**: a reference corpus the user brings in (PDFs, URLs,
docs, design "looks") that any agent can semantically search.

## Goal (and the hard constraint)

A user adds reference material; the brain chunks + embeds it and stores it; agents
(esp. the researcher) `search_reference(query)` for grounding — **with zero
dependence on the user's workstation**: embeddings come from a configurable remote
endpoint, storage is embedded pure-Go, and nothing requires a local GPU or Python.

## Portability (the crux)

- **Embeddings = a provider-agnostic OpenAI-compatible `/v1/embeddings` client**
  (`CORRALAI_EMBED_URL`, `CORRALAI_EMBED_MODEL`, `CORRALAI_EMBED_KEY`). Point it at
  OpenAI, Voyage/Cohere (compat), a self-hosted server, or any Ollama. The brain
  never runs the model itself. Unset URL ⇒ RAG disabled (no hard dep).
- **Storage = DuckDB** (already a brain dependency — the memory store uses it; no
  Postgres, no new engine, no server). Embeddings stored as `FLOAT[]`; **cosine via
  DuckDB's native `list_cosine_similarity`** (vector math in the DB, model-agnostic
  dimension). Brute-force scan is ample for a reference corpus; DuckDB's VSS/HNSW
  index is a later optimization, not a dependency. (If a DuckDB build lacks
  `list_cosine_similarity`, fall back to computing cosine in Go.)
- Result: the embed endpoint is config (cloud or self-hosted), the store is
  embedded DuckDB shipping with the brain. Nothing is pinned to one machine, and
  no database server is required.

## Components

### 1. `internal/reference` store (DuckDB — reuses the memory engine)

```sql
CREATE TABLE chunks (
  id         BIGINT PRIMARY KEY,
  source     TEXT NOT NULL,   -- doc name / URL
  kind       TEXT NOT NULL,   -- pdf | url | text | look
  seq        INTEGER NOT NULL,
  text       TEXT NOT NULL,
  embedding  FLOAT[] NOT NULL, -- normalized embedding (model-agnostic dim)
  created_ts DOUBLE NOT NULL
);
```

API: `Replace(source, kind, chunks []Chunk)` (idempotent: delete the source's old
chunks, insert new — like Kirby), `Search(queryVec []float32, k int) ([]Hit,
error)` (`ORDER BY list_cosine_similarity(embedding, $q) DESC LIMIT k`; Hit =
{source, kind, text, score}), `Sources()`, `Remove(source)`. Vectors are
unit-normalized at ingest. `var now` seam. (The query vector is passed as a
`[...]::FLOAT[]` literal — floats only, no injection risk.)

### 2. Embedder (`internal/reference/embed.go`)

`Embedder.Embed(texts []string) ([][]float32, error)` — POSTs to the configured
`/v1/embeddings` (model + bearer key), returns vectors; **normalizes** to unit
length. `NewEmbedder()` from env; nil/disabled when no URL. Mirrors the agent's
openai-compatible backend.

### 3. Ingestion (`internal/reference/ingest.go`)

- `chunk(text)` — split into ~overlapping windows (token-ish by characters).
- text: chunk → embed → `Replace`.
- url: fetch (SSRF-guard via the existing gateway guard) → HTML→text → chunk.
- pdf: pure-Go text extraction (a lightweight lib) — or defer to a follow-up if
  no clean dep; MVP = text + url first.

### 4. Brain tools (`internal/brain`, gated on a configured reference store)

- `add_reference { source, kind?, text?, url? }` → ingest + embed + store →
  `{ chunks }`.
- `search_reference { query, k? }` → embed query → `Search` → `{ hits:[{source,
  kind, text, score}] }`.
- `list_references` → sources + chunk counts.

### 5. Agent + operator surface

- `search_reference` in `agentTools()`; the researcher prompt says to consult it.
- `corral-admin reference add <url|file>` / `list` / `search "<q>"`.

## Data flow

1. `corral-admin reference add https://… ` → brain fetches, chunks, embeds (remote
   endpoint), stores.
2. researcher bee on a mission calls `search_reference("scoring data sources")` →
   top chunks → grounds its requirements in shared memory.

## Testing strategy

- **store:** Replace is idempotent; Search returns nearest by cosine (deterministic
  fake vectors — e.g. orthonormal basis — assert ranking); Remove/Sources.
- **embedder:** against an httptest fake `/v1/embeddings` (assert request shape +
  normalization); disabled when no URL.
- **chunking:** windows + overlap; empty input.
- **tools:** add_reference + search_reference over MCP with a fake embedder.
- **live-ish e2e:** ingest text via a fake embed server; search returns it.

## Decisions deferred to the plan

- PDF extraction lib (pure-Go) vs. deferring PDF to a follow-up (MVP: text + url).
- Design "looks": store the source URL/description + (optionally later) a
  screenshot reference; MVP treats a look as a `kind=look` text/url entry.
- Whether to gate `add_reference` to superusers (probably yes in prod).
