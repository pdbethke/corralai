## What the herd was asked

Build a Python package `ratelimit` — a token-bucket `RateLimiter` with
`allow(key)`, configurable capacity and refill rate, docstrings, a README,
and unittest tests, gated on `python3 -m unittest` passing.

## How it went

Eleven agents, one model (`ollama:qwen2.5-coder:7b`), 3m43s wall-clock on a
single RTX 5070. The plan held at its original 11 tasks — no re-planning, no
finding storms: one medium finding filed, addressed, done. This was the first
outing of the language-aware verify gates: before this run, every mission was
gated on `go build`/`go test` no matter what it built, which made a Python
directive structurally impossible (nothing in a Python workspace can make
`go build` exit 0). The directive's own language now picks the gate.

## The honest warts

The gate checks that a passing verify run was **recorded during the mission**,
not that the final workspace still passes — and the final state doesn't: one
root-level test encodes a stricter refill expectation than the implementation,
and a test file carries a stray markdown fence. Classic 7B rough edges,
published unedited in the result repo. Final-state verification is an open
gap the gate does not yet close.

## What we learned

A small, single-language directive on one local 7B converges fast and clean —
the interesting failure modes (verify-refusal livelocks, stale leases) only
showed up in the longer, finding-heavy runs. Language-aware gates were the
single change that opened the corral to non-Go work.
