## What corral was asked

Certify a change to `internal/fence/fence.go` — the wrapper that fences
untrusted content (ingested documents, agent-written memory, reported findings)
so a consuming agent can't mistake it for its authoritative task — **by
execution**. The question isn't "does the code look right"; it's "does this
file's own test suite actually guard the security property: that untrusted
content can never forge or close the fence?"

## Why this tape is worth watching

This is the audit gate, not a builder — *nemo iudex in causa sua*, no one is
judge in their own cause. A **decorrelated cross-vendor herd** put the suite to
the test: **Claude Sonnet 5** planted the faults (and drafted the exposing
test), and a **different** model — **Gemini 3.5 Flash** — graded whether the
developer's tests caught them. The model that plants the exposing fault is never
the model that judges the suite. That separation is enforced, not hoped.

Open the **tests** tab (the audit lens) to see the whole thing in one place: the
signed verdict, the code under review with the surviving fault highlighted, and
the developer's suite corral graded. Every catch on this tape is proven by a
test that *actually ran in the jail and killed a fault* — never a model's word.

## What it found

- **kill-rate 0.75** — the developer's suite killed **3 of 4** planted faults.
- **1 survivor** — a planted fault the suite passed anyway. That's the gap; the
  audit refused to certify and returned **NEEDS-REVIEW** rather than a green it
  couldn't stand behind.
- **proven-missed 0** — the pool authored a test targeting the survivor, but it
  didn't kill it on this run. So the gap is *flagged*, not yet *closed* — and the
  tape says exactly that, instead of pretending otherwise.
- **signed record 10** — the verdict is tamper-evident and offline-verifiable
  (`corral certify verify`). You don't trust it; you re-run it.

## The honest floor

`corral certify` is the shipped atom; the adversarial pool that produced this
tape is experimental and off by default. And one run is a **data point, not a
ranking** — `fence.go`'s suite is genuinely strong, so whether a fault survives
is partly a coin flip. The bug-catching numbers only mean something at volume,
which the eval harness accrues across a whole corpus before any of it gets
published. We'll always tell you which floor you're standing on.
