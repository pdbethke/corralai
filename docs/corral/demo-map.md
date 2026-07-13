# demo-map

> **`make demo-mission` and `make demo-mission-epic` are RETIRED and no longer
> run** — they invoke `corral-admin mission create`, a verb removed in the
> 2026-07-13 re-focus to a reactive audit/certification gate (mission creation
> has no MCP or CLI entry point today). `make demo` / `demo-cpu` / `demo-clobber`
> / `demo-observe` / `demo-models` below are unaffected. See
> `deploy/demo/README.md` and
> `docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`.

`deploy/demo/Makefile` wraps `docker compose -f docker-compose.yml` with
per-scenario profiles. The UI is always at `http://localhost:9019`, with tabs
`swarm` / `progress` / `topology` / `memory` / `lookbook` (`internal/ui/web/index.html`).

## Make targets and what each shows

- `make demo` — `--profile coordinated`: builder/tester/pentester/reviewer
  coordinating over shared claims and memory. Watch the **swarm** tab.
- `make demo-cpu` — same as `demo` but with the GPU reservation removed
  (`docker-compose.cpu.yml`), for hosts without an NVIDIA GPU.
- `make demo-clobber` — `--profile clobber`: the same agents, but ignoring
  claim conflicts and trampling shared files — the "why claims matter"
  contrast case.
- `make demo-observe` — `--profile coordinated --profile observe`: adds the
  read-only observer console at `http://localhost:9020`.
- `make demo-mission` — `--profile mission`: the full adaptive loop — a
  directive becomes a mission the herd builds and re-plans. Watch the
  **progress** tab; this is the flagship demo.
- `make demo-mission-epic` — `demo-mission` with a harder directive (a
  recursive-descent arithmetic parser), useful for showing a frontier model.
- `make demo-models` — `--profile models`: pentester and reviewer run on
  *different* local models (`qwen2.5-coder:7b` vs `llama3.2:3b`) so the
  **topology** tab's model_comparison table shows a genuine A-vs-B.
- `make down` — stops every profile and removes the workspace volume.

## Where the UI tabs live

The UI is served by `internal/ui` (`Server`, built from `internal/ui/web/index.html`).
Its state view (`stateView` in `internal/ui/ui.go`) assembles tasks, findings,
missions, executions, activity, topology, and proposals from the brain's
stores each poll — that one struct backs all four tabs.
