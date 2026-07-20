# CORRAL.md

> **Visibility and accountability enhance, rather than restrict, performance.**

Entry point to corralai's own developer-doc corpus — the convention any repo
audited with corralai can adopt.

Corralai is a coordinated multi-agent, multi-model **audit gate**: a headless
**brain** (`cmd/corral`) certifies a change **by execution** — it runs the check
in a jail, measures the result itself (never a self-report), and signs a
tamper-evident record — while a herd of (possibly different) models shares a
persistent, searchable memory (`internal/memory`) the whole time. See
[README.md](README.md).

## Where the herd's knowledge lives

Two kinds of knowledge feed a run:

- **Vetted memory** — lessons and guidance the herd itself discovered and a
  human promoted (`internal/learn`, `internal/brain/learn.go`). This is
  `shared=true`: trusted enough to auto-inject into new instructions.
- **This repo's developer docs** — `CORRAL.md` (this file) plus
  `docs/corral/*.md`. On a clone, the brain ingests these files as **advisory**
  memory entries (`shared=false`, tagged to this repo) — see
  `internal/brain/seeddocs.go`. Agents can find them via search but they never
  auto-inject; a repo you don't control can't smuggle "vetted" guidance in just
  by shipping a file.

## The docs/corral/ corpus

- [docs/corral/verify-gate.md](docs/corral/verify-gate.md) — the verification gate: how a check is certified by execution, not a self-report.
- [docs/corral/memory-etiquette.md](docs/corral/memory-etiquette.md) — how to write and search memory.
- [docs/corral/claims-and-leases.md](docs/corral/claims-and-leases.md) — the path/branch claim broker.
- [docs/corral/demo-map.md](docs/corral/demo-map.md) — the demo's make targets and UI tabs.
- [docs/corral/sakana-fugu-analysis.md](docs/corral/sakana-fugu-analysis.md) — dynamic orchestrator models vs. Corralai.

Anyone can add to this corpus via a normal pull request — see
[CONTRIBUTING.md](CONTRIBUTING.md). Knowledge grows the way code does: through
review, where **code review is the trust gate for knowledge exactly as it is
for code**.

## How a developer's agent queries it

A thin client (Claude Code, Cursor, Gemini CLI, ...) joins the brain by
pointing its `.mcp.json` at the brain's MCP endpoint (README.md's "Thin
client" platform row), then calls `search_memory` — full-text search over
everything you can see, including this repo's advisory `docs/corral/*.md`
entries alongside vetted lessons and guidance.
