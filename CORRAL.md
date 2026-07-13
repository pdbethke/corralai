# CORRAL.md

> **Visibility and accountability enhance, rather than restrict, performance.**

> **RETIRED FLOW.** The "plans, builds, verifies, and re-plans" description
> below is the build-from-directive mission loop, which is being retired as
> corral re-focuses to a reactive audit/certification gate (the repo gate +
> control gate are the current live surface — see `README.md`'s "What runs
> today"). Kept for reference pending a rewrite; see
> `docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`.

Entry point to corralai's own developer-doc corpus — the convention any repo
built with corralai can adopt.

Corralai is a coordinated multi-agent, multi-model orchestrator: a headless
**brain** (`cmd/corral`) turns a directive into a mission, and a herd of agents
plans, builds, verifies, and re-plans it — sharing a persistent, searchable
memory (`internal/memory`) the whole time. See [README.md](README.md).

## Where the herd's knowledge lives

Two kinds of knowledge feed a mission:

- **Vetted memory** — lessons and guidance the herd itself discovered and a
  human promoted (`internal/learn`, `internal/brain/learn.go`). This is
  `shared=true`: trusted enough to auto-inject into new mission instructions.
- **This repo's developer docs** — `CORRAL.md` (this file) plus
  `docs/corral/*.md`. On a repo mission's clone, the brain ingests these files
  as **advisory** memory entries (`shared=false`, tagged to this repo) — see
  `internal/brain/missions.go` / `internal/brain/seeddocs.go`. Agents can find
  them via search but they never auto-inject; a repo you don't control can't
  smuggle "vetted" guidance in just by shipping a file.

## The docs/corral/ corpus

- [docs/corral/verify-gate.md](docs/corral/verify-gate.md) — the verification gate on task completion.
- [docs/corral/memory-etiquette.md](docs/corral/memory-etiquette.md) — how to write and search memory.
- [docs/corral/claims-and-leases.md](docs/corral/claims-and-leases.md) — the path/branch claim broker.
- [docs/corral/mission-lifecycle.md](docs/corral/mission-lifecycle.md) — directive to sprint, end to end.
- [docs/corral/demo-map.md](docs/corral/demo-map.md) — the demo's make targets and UI tabs.
- [docs/corral/sakana-fugu-analysis.md](docs/corral/sakana-fugu-analysis.md) — dynamic orchestrator models vs. Corralai.

Anyone can add to this corpus via a normal pull request — see
[CONTRIBUTING.md](CONTRIBUTING.md).

## How a developer's agent queries it

A thin client (Claude Code, Cursor, Gemini CLI, ...) joins the brain by
pointing its `.mcp.json` at the brain's MCP endpoint (README.md's "Thin
client" platform row), then calls `search_memory` — full-text search over
everything you can see, including this repo's advisory `docs/corral/*.md`
entries alongside vetted lessons and guidance.
