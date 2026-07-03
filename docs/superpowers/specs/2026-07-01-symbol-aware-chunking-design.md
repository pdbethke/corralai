# Symbol-Aware Code Chunking — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** B (repo code index #17 enhancement)

## Problem

The per-mission repo code index (`internal/repoindex`) chunks source files with a single
hardcoded sliding window — `chunkLines(text, 60, 15)` at `store.go:76` — with **zero language
awareness**. A chunk is 60 arbitrary lines; a function is split across chunk boundaries, and a
hit reports only `path:startLine-endLine` with no idea what symbol it landed in. This hurts
retrieval relevance (a semantic unit is fragmented) and hit readability (no symbol/kind), and
it undersells the code-index story for a release-facing project.

**Goal:** chunk source along symbol boundaries (functions, methods, types/classes) using
tree-sitter, tag each chunk with its symbol name + kind + language, and surface that in search
hits — while degrading gracefully (unsupported language or parse failure → the existing
line-window chunker). Indexing must NEVER fail.

## Decisions (locked)

- **Multi-language via tree-sitter** (`github.com/smacker/go-tree-sitter`), CGO.
- **Grammar set (~12):** Go, Python, JavaScript, TypeScript, Rust, Java, C, C++, C#, Ruby, PHP,
  Bash. Each grammar is a smacker sub-package; each is C compiled into the `corral` binary.
- **Chunking runs brain-side only** (indexing happens in `corral` at `create_mission` +
  per-commit reindex), so tree-sitter is confined to `corral` — consistent with "corral is the
  sole CGO binary"; bees (`corral-agent`) never link it.

## Architecture

Replace the single `chunkLines` call in `IndexFiles` with a dispatcher:

```
chunkFile(path, text) []Chunk:
  lang := langForExt(path)                 // extension → language (or "" if unknown)
  if lang == "" || !supported(lang):
      return chunkLines(text, 60, 15)      // fallback, tagged lang="" 
  chunks, err := chunkSymbols(text, lang)  // tree-sitter parse + query
  if err != nil || len(chunks) == 0:
      return chunkLines(text, 60, 15)      // parse failure → graceful fallback (tagged lang)
  return chunks
```

`chunkSymbols` parses the file with the language's grammar and runs a per-language tree-sitter
**query** (`.scm`) that captures definition nodes with their name + kind. Each captured
definition becomes one chunk over its line range, tagged `Symbol`/`Kind`/`Lang`. Rules:

- **Oversized definition** (line span > a threshold, e.g. `2*window` = 120 lines): sub-chunk it
  with `chunkLines` *within its own range*, each sub-chunk tagged with the same symbol + a part
  suffix (`Foo#1`, `Foo#2`), so a large function stays retrievable AND attributed.
- **Nesting:** capture leaf definitions (a method, not the whole enclosing class body) so bodies
  aren't double-indexed; a class/type contributes a small **header** chunk (declaration +
  fields/signature, excluding method bodies) tagged `kind=class`/`type`. (Per-language query
  design; the plan specifies node types per grammar.)
- **Preamble:** the region before the first captured definition (package/imports/license) →
  one `chunkLines` pass over that span, tagged `lang`, empty symbol. Nothing is lost.
- **Whole-file coverage invariant:** every line of a parseable file is covered by exactly one
  chunk lineage (preamble ∪ definitions ∪ oversized sub-windows); gaps between top-level
  definitions fold into the adjacent preamble/inter-def line-window chunk.

## Components / changes

### 1. `internal/repoindex/lang.go` (new)
- `func langForExt(path string) string` — extension → canonical language id (`.go`→`go`,
  `.py`→`python`, `.ts`/`.tsx`→`typescript`, `.js`/`.jsx`→`javascript`, `.rs`→`rust`, …).
  Returns `""` for unknown.
- `func grammar(lang string) *sitter.Language` / `supported(lang) bool` — maps a language id to
  its smacker grammar; the single place the 12 grammars are registered.
- `queryFor(lang string) string` — the per-language `.scm` definition-capture query.

### 2. `internal/repoindex/chunk.go` (modify)
- Extend the chunk record (currently `LineChunk{Seq, StartLine, EndLine, Text}`) with
  `Symbol string`, `Kind string`, `Lang string`.
- Keep `chunkLines(text, window, overlap)` unchanged (the fallback + the within-symbol
  windower); its chunks carry empty `Symbol`/`Kind` and a caller-supplied `Lang`.
- Add `chunkSymbols(text, lang string) ([]LineChunk, error)` (tree-sitter parse + query +
  oversized-windowing + preamble).
- Add `chunkFile(path, text string) []LineChunk` (the dispatcher above).

### 3. `internal/repoindex/store.go` (modify)
- `chunks` table gains `symbol VARCHAR, kind VARCHAR, lang VARCHAR` via migration-safe
  `ALTER TABLE chunks ADD COLUMN IF NOT EXISTS ...` (mirror the #21 `vetted` migration pattern;
  the columns are nullable — fallback chunks store empty symbol/kind).
- `IndexFiles` calls `chunkFile(f.Path, f.Text)` instead of `chunkLines(f.Text, 60, 15)`; the
  INSERT writes the three new columns.

### 4. `internal/repoindex/search.go` (modify)
- `Hit` gains `Symbol string`, `Kind string`, `Lang string`; `Search` selects + returns them.
- `repo_search` (brain/reposearch.go) surfaces them, so a hit reads e.g.
  `func OpenPR (function) internal/repo/pr.go:120-164` rather than a bare range.

## Error handling / edge cases

- **Unsupported extension** (`langForExt`==`""`) → `chunkLines` fallback, `lang=""`.
- **Parse failure / empty capture** (broken syntax, partial file) → `chunkLines` fallback,
  `lang` still tagged. Never crash, never drop the file.
- **Binary / >512 KiB** → unchanged existing gate in `IndexPaths` (skipped before chunking).
- **Oversized definition** → windowed within its range; never a single unbounded chunk.
- **File with no definitions** (a script, a data file that parsed) → whole file via `chunkLines`,
  `lang` tagged.
- **Existing indexes** — the new schema columns are additive; old rows read back with empty
  symbol/kind. Re-chunking applies going forward and on the next per-commit reindex; no forced
  re-embed of historical chunks (a follow-up if desired).
- **Concurrency / determinism** — a tree-sitter parser is not goroutine-safe; `chunkSymbols`
  creates (or pools) a parser per call. Chunk output is deterministic for a given file+grammar.

## Testing

- **Go file** → chunks for each `func`/`type`/method with correct `Symbol`+`Kind`+line ranges;
  `package`+imports land in a preamble chunk.
- **Python + TypeScript files** → symbol chunks (the multi-language proof — at least two
  non-Go grammars exercised end to end).
- **Oversized function** (> threshold) → multiple sub-chunks all tagged with the same symbol +
  part suffix; union of sub-chunk ranges == the function's range.
- **Syntactically broken file** → `chunkSymbols` errors → `chunkFile` returns line-window
  chunks; no panic; file still indexed.
- **Unsupported extension** (`.md`/`.txt` or an unlisted language) → line-window fallback.
- **Whole-file coverage** — for a parseable multi-def file, every source line is covered by some
  chunk lineage (no lost lines).
- **Retrieval** — a `repo_search` hit surfaces `Symbol`/`Kind`/`Lang`; searching a function name
  ranks its symbol chunk highly.
- **Store migration** — adding the 3 columns to an existing `chunks` DB doesn't error; a mixed
  DB (old fallback rows + new symbol rows) searches fine.

## Out of scope (follow-ups)

- Cross-file symbol graph / call edges / import resolution.
- Config-file (JSON/YAML/TOML) "symbol" chunking — weak structure, low value.
- Forced re-embed/re-chunk of historical indexes (applies going forward + on reindex).
- Query-level symbol filtering in `repo_search` (e.g. "only functions") — the metadata is stored
  and surfaced; a filter arg is a later nicety.
- Binary-size/build-time optimization (lazy grammar loading, build tags per language) — accepted
  cost of the broad grammar set for v1.
