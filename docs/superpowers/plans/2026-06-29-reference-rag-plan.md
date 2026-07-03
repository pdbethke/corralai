# Reference RAG Engine — Plan (sub-project #7)

> TDD, commit per task on `feat/reference-rag`. Design:
> `docs/superpowers/specs/2026-06-29-reference-rag-design.md`.

**Goal:** bring-your-own reference material (URL/text/PDF) → chunk → embed via a
configurable REMOTE endpoint → DuckDB → agents `search_reference` for grounding.
Not tied to any workstation: embeddings are config, storage is embedded DuckDB.

## Global Constraints

- Embeddings via a provider-agnostic `/v1/embeddings` endpoint (no local model).
- Storage: DuckDB (reuses the memory engine; no Postgres/server); native
  `list_cosine_similarity` (verified available).
- Disabled cleanly when no embed endpoint is configured (no hard dependency).

---

## Task 1: `internal/reference` store (DuckDB)

`chunks` table; `Replace(source,kind,[]Chunk)` (idempotent), `Search(vec,k)` via
`list_cosine_similarity`, `Sources`, `Remove`. `var now`.

- [ ] Tests (deterministic vectors): Replace idempotent; Search ranks by cosine;
  Sources/Remove.
- [ ] Implement; `go test ./internal/reference/`. Commit.

## Task 2: embedder + chunking (`internal/reference`)

`Embedder.Embed([]string)` → POST configured `/v1/embeddings` (model+key);
`NewEmbedder()` from env (nil when unset). `chunk(text)` windows+overlap.

- [ ] Tests: embedder against an httptest fake (request shape); disabled when no
  URL; chunking windows. Implement. Commit.

## Task 3: ingestion + brain tools

`Ingest(store, embedder, source, kind, text/url)`: url fetch (SSRF guard) →
HTML→text → chunk → embed → Replace. Brain `add_reference`, `search_reference`,
`list_references` (gated on a configured reference store + embedder). Wire into
main (CORRALAI_REFERENCE_DB + embed env).

- [ ] Tests (over MCP, fake embedder): add_reference + search_reference. Commit.

## Task 4: agent + operator surface

`search_reference` in agentTools; researcher prompt consults it. `corral-admin
reference add <url|file> | list | search "<q>"`.

- [ ] Implement; build. Commit.

## Task 5: e2e + verification

- [ ] Live-ish e2e: a fake `/v1/embeddings` server; ingest text; search returns it.
- [ ] Full `go test ./...` + `go vet`. Commit. Merge.

(PDF extraction + design "looks" as kind=look: follow-ups; MVP = url + text.)

---

## Status: COMPLETE (2026-06-29)

T1 store (`13bdc68`) · T2 embedder+chunking (`8dba684`) · T3 ingest+tools
(`ffccb0e`) · T4 agent+operator surface (`f6bb589`). `go test ./...` + `go vet`
green.

**Live e2e:** brain booted RAG-enabled against a remote `/v1/embeddings` endpoint
(a stand-in keyword-vector server); `corral-admin reference add --text …` ingested
two docs into DuckDB; `reference search "where do match scores come from"` ranked
the relevant doc (0.577) above the irrelevant one (0.000) via native
`list_cosine_similarity`. **Nothing tied to the workstation** — embeddings come
from the configured endpoint, storage is embedded DuckDB. Completes #7. (PDF +
design-"looks" ingestion are follow-ups.)
