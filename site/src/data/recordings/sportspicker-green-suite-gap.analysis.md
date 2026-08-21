## What corral was asked

Certify `awards.py` from **sportspicker-core** — a small, zero-dependency
library that scores sports pick'em contests — against the library's own test
suite, by execution. The goal it had to defend is the guarantee the whole design
rests on: *a contest contributes exactly its budget*. Whatever scoring rule is
chosen, however many members played, and however they scored, the points awarded
in a round must sum to that round's pot.

The branch under audit is deliberately broken, and says so in its own
`DEMO-BUG.md`. `min_participants` counts everyone who submitted picks rather
than everyone who *scored*, and the test that would have caught it is gone —
the remaining cases all use fields where every member scored, so the two counts
are the same number and the distinction never arises. **The suite is green: 80
tests, well under a second.** CI has nothing to say about this branch.

## What it found

Gemini 3.6 Flash planted 20 goal-violating mutants across the file, sharded four
ways by function. The library's own suite, run in the jail against every one,
killed **15 of 20 — a 75% kill-rate** — and **5 survived**. The gate returned
**NEEDS-REVIEW** and signed the verdict.

Where the survivors landed is the interesting part. Two sit in `effective_pot`,
the function that carries the planted bug; two in `award_round`; one in
`pot_for`. Among them:

- `positions = competition_ranks(scores)` replaced by a naive `enumerate` — so
  tied members stop sharing a rank. It survives because no remaining fixture
  contains a tie.
- `min_participants` defaulting to `1` instead of `0`.
- the `scoring >= minimum` boundary flipped to `>`, so a field sitting exactly
  on the minimum is treated as short.

Be precise about what this is and isn't. corral grades **test adequacy by
execution**; it did not announce "there is a planted bug here", and it does not
claim to. What it did is measure that the tests guarding this file no longer
defend it — and the same file on `main`, one test richer, scores 0.90 and
certifies. Same code path, same command, two verdicts, both with a green suite.

The decorrelated critic (Claude Haiku 4.5) flagged three tests it read as
vacuous, including an assertion that a zero-scoring member is awarded zero
points — which holds arithmetically no matter what the distribution logic does.
That is a second model's opinion, marked UNVERIFIED, and it never gates the
verdict. The 75% is what the jail measured.

## Why this one is worth watching

It is the thesis in one screen. The tests pass. The branch would merge. The bug
is real, documented, and sitting in the diff — and the only instrument that
registers anything is the one that runs the suite against faults it planted
itself. Open the **tests** tab and watch a surviving mutant highlighted against
the code the suite passed anyway.
