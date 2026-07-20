# demo-map

`deploy/demo/Makefile` wraps `docker compose -f docker-compose.yml` with
per-scenario profiles that stand up a local brain + herd against Ollama —
no API key required. The UI is always at `http://localhost:9019`, with tabs
`swarm` / `progress` / `topology` / `memory` / `lookbook`
(`internal/ui/web/index.html`).

For the audit product itself — `corral certify --local` mutation-scoring a
change, and the repo/control gates — see the README quickstart and
`deploy/demo/README.md`; this page only maps the coordinated-herd Compose
demo and its make targets.

## Make targets and what each shows

- `make demo` — `--profile coordinated`: builder/tester/pentester/reviewer
  coordinating over shared claims and memory — the queue/claim/lease
  mechanics that back every gate. Watch the **swarm** tab.
- `make demo-cpu` — same as `demo` but with the GPU reservation removed
  (`docker-compose.cpu.yml`), for hosts without an NVIDIA GPU.
- `make demo-clobber` — `--profile clobber`: the same agents, but ignoring
  claim conflicts and trampling shared files — the "why claims matter"
  contrast case.
- `make demo-observe` — `--profile coordinated --profile observe`: adds the
  read-only observer console at `http://localhost:9020`.
- `make demo-models` — `--profile models`: pentester and reviewer run on
  *different* local models (`qwen2.5-coder:7b` vs `llama3.2:3b`) so the
  **topology** tab's model_comparison table shows a genuine A-vs-B —
  the same decorrelated-role machinery the adversarial certify pool uses.
- `make down` — stops every profile and removes the workspace volume.

## Where the UI tabs live

The UI is served by `internal/ui` (`Server`, built from `internal/ui/web/index.html`).
Its state view (`stateView` in `internal/ui/ui.go`) assembles tasks, findings,
executions, activity, topology, and proposals from the brain's stores each
poll — that one struct backs all four tabs.
