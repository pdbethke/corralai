## What the herd was asked

Build a Go package `stack` — a LIFO stack of ints with `New`, `Push`, `Pop`,
`Peek`, `Len`; `Pop`/`Peek` return an error when empty; table-driven tests —
gated on `go build ./...` and `go test ./...` both passing.

## Why this tape is worth watching

This is a re-planning tape. It is not the herd sailing through a plan — it is
the herd **rewriting its own plan in real time** as the verify gate refuses to
certify work that doesn't yet pass. One directive on a single local 7B model
(`qwen2.5-coder:7b`, one RTX 5070) turned into a 60-task, 83-finding, 49-minute
grind that only ended when `go build`/`go test` genuinely went green and a
reviewer accepted. Scrub the **files** tab and watch the tree fill in as agents
claim `stack.go`; scrub **progress** and watch the plan grow from the seed tasks
to 60 as the reflex re-planner turns findings into fix work.

## Play-by-play (mission time — scrub to these moments)

- **0:00** — Seed plan laid down: design → build-core → build → test →
  integrate → docs. The herd claims `stack.go` and starts.
- **2:24** — First finding. The verify gate catches the core build short.
- **2:42** — **Re-plan, live.** The reflex re-planner *supersedes* the original
  core build with `build-core#1-r2` — a new task that replaces the old one, not
  a blind retry. This is the moment the plan stops being the seed plan.
- **2:55** — `build#2` supersedes the original completion build for the same
  reason. Two tasks rewritten inside 30 seconds.
- **3:55 / 9:37 / 16:12 …** — The gate keeps refusing to pass unproven work and
  **escalates loudly** after repeated refusals — the anti-livelock guard doing
  its job instead of spinning silently.
- **~20:00 → 44:30** — The tester (`test#1`) hammers adversarially for ~24
  minutes: reissue after reissue, escalation after escalation, refusing to
  certify until `go build ./...` and `go test ./...` both go green. This is the
  slow part, and it is the point: the gate does not take the model's word for it.
- **45:15** — Integration.
- **48:13 → 48:45** — The reviewer requests changes four times in a row.
- **48:58** — Review accepted, mission complete. 58 of 60 tasks done, 83
  findings filed and worked along the way.

## The honest warts

Forty-nine minutes for a LIFO stack is slow — that is what a gated language a
7B model is shaky in looks like when the gate refuses to lie about it. Two of
the 60 tasks never reached done (they were superseded, not completed). As with
every published tape, this is the raw run: the acceptance means the gate saw a
passing verify, and — per the convergence work still in flight — "accepted"
records that a passing run happened, so treat the final workspace as evidence,
not a warranty. Published unedited, as always.

## What we learned

The re-planning is the product. A cheap local model will happily claim success;
the deterministic gate plus the reflex re-planner is what turns that into 83
findings, two superseded tasks, a fistful of loud escalations, and eventually a
green build — visibly, on a scrubber, one moment at a time.
