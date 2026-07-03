# Semantic Hive-Mind Recall — Design

**Status:** design · **Date:** 2026-06-30 · **Sub-project:** #14

## Where this fits

corralai's shared memory (`internal/memory`) is the swarm's collective brain: every
agent reads and writes one corpus, persisted as markdown under
`~/.claude/projects/*/memory` with a derived DuckDB index. Today that index is
**BM25 keyword-only**. The hive-mind premise — "hey Hawk, have you seen this
vulnerability before, or did you patch it?" — needs **semantic** recall: a new vuln
phrased differently than the old note won't match lexically. The reference/RAG
engine (`internal/reference`) already proves the pattern on the same DB: an
env-configured `Embedder` plus native DuckDB `list_cosine_similarity`. This
sub-project gives the memory corpus that same semantic layer, adds per-agent
**attribution**, and wires it into the read-only "ask a bee" narrator so the
collective experience is queryable in an agent's voice.

It builds on the seeded self-describing meta-memory + liberal-journaling prompts
already landed (commit `29d0ec2`): liberal writes create the experience; this makes
that experience findable by meaning, not just by keyword.

## First principle: graceful degradation, never a hard dependency

Carried from the reference engine: if no embedder is configured
(`CORRALAI_EMBED_URL` unset) the corpus silently falls back to BM25. Embedding is an
enhancement, never a requirement — the brain runs, search works, nothing fails.

## Components

### 1. `internal/embed` (new) — shared embedder, DRY extract

Move `Embedder` out of `internal/reference` into a small shared package
`internal/embed` so both `reference` and `memory` use ONE embedder rather than a
store-to-store dependency. The public surface is unchanged from today's
`reference.Embedder`:

- `func New() *Client` — from env (`CORRALAI_EMBED_URL`, `CORRALAI_EMBED_MODEL`
  default `text-embedding-3-small`, `CORRALAI_EMBED_KEY`); returns nil when URL
  unset.
- `func NewFor(url, model, key string) *Client` — explicit (tests).
- `func (c *Client) Embed(texts []string) ([][]float32, error)` — batch embed.

`internal/reference` keeps its existing API stable via a **type alias** —
`type Embedder = embed.Client` — and its `NewEmbedder`/`NewEmbedderFor` delegate to
`embed.New`/`embed.NewFor`. So every existing `reference.Embedder` caller and test
compiles and behaves identically; only the implementation moves. `cmd/corral`
constructs one `*embed.Client` and passes it to both stores.

### 2. `internal/memory` — attribution + embeddings + hybrid search

**Schema (idempotent migrations on `Open`, mirroring the existing `shared`-column
ALTER pattern):**
- `ALTER TABLE mem ADD COLUMN author VARCHAR DEFAULT ''`
- `ALTER TABLE mem ADD COLUMN embedding FLOAT[]`

**Write path:**
- `Add(...)` gains an `author string` parameter, persisted on the row. When an
  `*embed.Client` is set, embed the entry's text (title + description + body) and
  store the vector; on embed error, store the row WITHOUT a vector (keyword still
  finds it) and log.
- `Build(dirs)` re-indexes as today AND, when an embedder is set, embeds entries
  that lack a vector (backfill). The store holds an optional `embedder *embed.Client`
  set via a **`SetEmbedder(*embed.Client)` setter** — NOT a change to `Open`'s
  signature — so the many existing `memory.Open(path)` call sites and tests stay
  unchanged (the lesson from prior tasks: avoid signature churn). `cmd/corral` calls
  `memStore.SetEmbedder(c)` after `Open`.

**Read path — hybrid `Search`:**
- Keep the BM25 query (unchanged) as the floor.
- When an embedder is set: embed the query, run
  `list_cosine_similarity(embedding, <qvec>::FLOAT[])` over rows that have a vector,
  take the top-k.
- **Merge + dedupe by slug**, producing one ranked `[]Hit`. Each `Hit` carries its
  `author` and a source tag (`semantic` | `keyword` | `both`). Ranking: normalize
  each list's scores to [0,1] and take the max across the two for a shared entry
  (a simple, explainable blend — no learned weights). Cap at the caller's `limit`.
- Embedder nil → cosine skipped, BM25 result returned as-is.

`Hit` gains `Author string` and `Via string` ("semantic"|"keyword"|"both").

### 3. `add_memory` brain tool — stamp the author

In `internal/brain/memory.go`, the `add_memory` handler computes
`author := identity(req, in.Name)` (the calling bee, authoritatively) and passes it
to `store.Add`. The agent's `brain` closure already routes the agent name; the brain
stamps the authoritative identity server-side (same pattern as every other
identity-bearing tool). `search_memory`/`get_memory`/`list_memory` results include
`author` so callers (and the UI memory tab) can show who logged what.

### 4. Narrator (`internal/ui/ask.go`) — recall in an agent's voice

`buildTrail` gains a **semantic memory search keyed on the question** (not the
agent's recent trail): embed the user's question, cosine over the whole corpus
(the hive-mind), take the top few, and add them to the grounding block, each flagged
`(your own)` when `author == the asked agent` vs `(hive: <author>)`. So "ask Hawk:
have you seen this vuln?" surfaces Hawk's own findings AND the hive's, attributed —
and the narrator already answers in Hawk's first-person voice, grounded only in what
it's given. Embedder nil → fall back to a keyword memory search for the grounding.

The UI server holds the shared `*embed.Client` (added to `ui.Deps`) and the memory
store (it already has neither for ask — add both) to run this search.

### 5. Demo wiring

Set on the demo brain service so semantic recall is live with the existing key:
- `CORRALAI_EMBED_URL=https://generativelanguage.googleapis.com/v1beta/openai/embeddings`
- `CORRALAI_EMBED_MODEL=gemini-embedding-001` (verified working, 3072-dim)
- `CORRALAI_EMBED_KEY=${OPENAI_API_KEY}` (reuse the demo key)

Without these, the demo degrades to keyword — the seeded meta-memory + liberal
prompts still fill the tab; only the "find by meaning" part needs the embedder.

## Data flow

```
add_memory(body)         → author=identity(req); embed(text) → mem row {author, embedding} + .md
search_memory(q)         → BM25(q)  ⊕  cosine(embed(q))  → normalize, merge by slug, rank
ask Hawk ▸ "this vuln?"  → embed(question) → cosine over corpus
                           → top memories (author==Hawk flagged "your own", else "hive: X")
                           → grounding → narrator answers as Hawk
```

## Error handling / edge cases

- **No embedder** (`CORRALAI_EMBED_URL` unset): semantic skipped everywhere; BM25
  floor; narrator uses keyword memory search. No failure.
- **Embed failure on `Add`**: row stored without a vector (keyword finds it); logged;
  a later `Build` backfills.
- **Cosine on a NULL/empty/mismatched-dim embedding**: that row scores 0 / is
  excluded; never panics. (Mixed dims can occur if the embed model changes —
  documented; a full `Build` re-embed fixes it.)
- **Author empty** (dev, unauthenticated, or pre-existing rows): `author=''`;
  treated as "hive, unattributed" by the narrator.
- **Embedding column + markdown source of truth**: the vector lives only in the
  DuckDB index (derived), NOT in the markdown front-matter — so the `.md` stays the
  clean portable source and a `Build` recomputes vectors.

## Testing

- **`internal/embed`**: a `NewFor` against a stub `/v1/embeddings` server returns
  the expected vectors; `New()` with unset URL returns nil.
- **`internal/reference`**: existing tests stay green after the extract (no behavior
  change) — proves the refactor is safe.
- **`internal/memory`**:
  - `Add` records `author`; `Get`/`Search` return it.
  - hybrid `Search` with a **fake embedder** (deterministic vectors): a query whose
    meaning matches an entry with NO lexical overlap is surfaced by the semantic
    arm; an exact-token query is surfaced by BM25; a shared hit is tagged `both`.
  - **nil embedder → keyword-only, no error** (graceful degradation).
  - migration is idempotent (re-`Open` an existing store; existing memory tests stay
    green).
- **`internal/brain` (memory_test)**: `add_memory` over MCP stamps the authoritative
  `author`; it appears in `search_memory` results.
- **narrator (`internal/ui`)**: with seeded attributed memories + a fake embedder,
  `ask` grounding includes the semantically-relevant entry and flags the asked
  agent's own vs the hive's.

## Out of scope (follow-ups)

- ANN / vector index (linear cosine is fine for swarm-sized corpora).
- Re-embed-only-on-change optimization (Build re-embeds missing vectors; acceptable).
- Per-agent PRIVATE memory (everything stays shared/attributed; "private" is a
  separate visibility design).
- Dedup/curation of near-duplicate liberal jottings (a retrieval-quality follow-up
  once the corpus is large).
