## What the herd was asked

Build a Go package `lru` — a generic (Go type-parameter) LRU cache with
`New`/`Get`/`Put`/`Len`, least-recently-used eviction at capacity, table-driven
tests over eviction order, updates, and the capacity edges. Gated on
`go build ./...` and `go test ./...`.

## Why this tape is the headline

This is the **mixtape**: local and frontier models in the *same* mission, over
one shared brain. Not a benchmark of one against the other — a single herd where
the work flows across the line between them:

- **Qwen 2.5 Coder 7B — local, on an RTX 5070** — the **designer**: it read the
  directive and laid down the cache's shape (`lru.go`) before anyone built.
- **Claude** (Sonnet) — **builder + integrator**
- **Gemini** (2.5 Pro) — **tester**
- **Codex** (GPT-5-Codex) — **pentester + second builder**

A cheap local model did the design; frontier models built, tested, and hardened
it; the deterministic gate held all of them to `go build`/`go test`. Converged
in **~8 minutes**. That is the whole thesis in one tape — *any* model, local or
frontier, mixed in one mission, no vendor lock-in, no API keys on the frontier
side (they run on their own subscription CLIs). Watch the **files** tab: the tree
fills in colored by *whichever* model claimed the path — local green next to
frontier.

## The honest warts

- The local 7B designer is the slowest single step; the frontier build/test is
  quick. That split is the point — put the cheap model where cheap is fine and
  spend frontier tokens where they earn it.
- As with every published tape, the final workspace was independently
  re-verified (`go build`, `go test`, `go vet` all clean) before the mission was
  accepted. Model attribution is backfilled from the exact model each agent was
  launched on (the live `report_host` self-report is still partial across
  vendors). Published unedited.

## What we learned

Mixing models isn't a mode you switch on — it's the default the platform was
built for. The brain doesn't know or care that `Qwen` is a 7B on a local GPU and
`Claude` is a frontier CLI; they're both just agents claiming tasks over MCP.
The "work by model" breakdown on the recordings page shows all four pulling
their weight. Local for the cheap parts, frontier for the hard parts, one gate
keeping everyone honest.
