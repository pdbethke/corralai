## What the herd was asked

Build a Go module `calc` — a recursive-descent parser that evaluates arithmetic
expression strings (`+ - * /` and parentheses, correct operator precedence),
returning a clear error on malformed input, with table-driven tests. Gated on
`go build ./...` and `go test ./...` both passing.

## Why this tape is worth watching

This is the **all-frontier** run: no local models at all. Three frontier
vendors, each on its own subscription CLI (no API keys), coordinating as one
herd over MCP against a single brain:

- **Claude** (Sonnet) — the builder
- **Gemini** (2.5 Pro) — the tester
- **Codex** (GPT-5-Codex) — pentester, and a *second builder* added mid-run to
  drain the fix queue when one builder couldn't keep up with the findings

They built a working parser — lexer, recursive-descent parser + AST, evaluator,
and 37 passing subtests — and converged in **under 13 minutes**, versus 49 for
the single local 7B model on a comparable task. Same platform, same gate, same
re-planning loop; the only variable is the horsepower behind each agent. Scrub
the **files** tab to watch the tree fill in as each vendor claims paths; open a
task in **progress** to see its causal chain.

## The privacy gate earned its keep

On export, the egress privacy scan **refused to publish this tape** — it caught
the operator's username in an absolute file path one agent had claimed, and
hard-failed rather than ship it. Containment isn't just on the way *in* (the
jail); it's on the way *out* too. The path was normalized to `/work/…` and the
tape re-verified through the same gate before publishing. The herd built the
thing; the gate made sure publishing it didn't leak anything.

## The honest warts

- **Model attribution is partial.** Only Codex self-reported its model through
  `report_host`; Gemini hit a tool-schema quirk and Claude didn't emit one, so
  the per-event `model` field under-reports the roster. The agent **names**
  (Claude / Gemini / Codex) carry the story instead — the models list above is
  the real roster the bees were launched on.
- **Copilot sat this one out.** It was assigned the reviewer role, but the plan
  produced no reviewer *task* — the final review was the client-acceptance gate,
  which the operator performed — so Copilot idled and doesn't appear on the tape.
- 19 findings for a calculator is a lot: the frontier testers are aggressive
  about edge cases (precedence, nesting, division by zero, malformed input),
  which is the point of an adversarial gate. The final workspace was
  independently re-verified (`go build`, `go test`, `go vet` all clean) before
  acceptance. Published unedited, as always.

## What we learned

Frontier models don't change the *shape* of a corral mission — the serial plan,
the verify gate, the reflex re-planner, the findings grind are all identical to
the local run. They change the *clock*: ~4× faster to converge here. The
platform is model-agnostic by construction, and this tape is the proof — three
vendors' flagship CLIs, one mission, no API keys.
