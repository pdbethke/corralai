## What corral was asked

The same question as the sportspicker branch audit, against the same file — but
on `main`, with the full suite. Certify `awards.py` from
**sportspicker-core** against its own 81 tests, defending the guarantee that a
contest contributes exactly its budget.

## What it found

Gemini 3.6 Flash planted 20 mutants across the file's four functions. The suite
killed **18 of 20 — a 90% kill-rate** — and the gate returned **CERTIFIED**.

The two survivors are both off-by-one boundary conditions, and both are real:

- `pot_for`: `if round_count <= 0` widened to `<= 1`, so a **single-round
  contest pays nothing at all** — a direct violation of the budget guarantee,
  in the one shape (a one-off fight card) the library exists to support.
- `effective_pot`: `if scoring >= minimum` narrowed to `>`, so a field landing
  exactly on the minimum turnout is treated as short of it.

The test-writer then proved both catchable by execution and handed back a test
that kills them. The critic flagged no vacuous tests.

## Why this one is worth watching

It is the honest companion to the branch audit — and a caution about
self-graded numbers. This repository ships its own mutation dry-run:
sixteen goal-violating changes planted by hand, and the suite kills all
sixteen. The README says to treat 16/16 as a floor rather than a claim,
because those are the failures *the test author imagined*.

That caveat turns out to be the whole point. An adversarial generator with no
stake in the author's assumptions planted two faults he had not imagined, and
his suite let both through — one of which silently zeroes out every one-round
contest. A 90% CERTIFIED verdict is not a gold star; it is a measurement, with
the two gaps named and a killing test attached.

## What happened next

Both survivors were real, and both are now covered. The library added a test
pinning `pot_for(budget, 1)` to the full budget, and another pinning a field
landing exactly on `min_participants` — asserted in **void** mode, because the
existing test at that boundary ran in scale mode, where `pot * (scoring /
minimum)` equals `pot` and the right answer comes back under either comparison.
It read as coverage and discriminated nothing.

The critic earned its keep on the re-run too. It flagged a test asserting that
a zero-scoring member receives zero points — which holds arithmetically whatever
the code does, since normalized weights already give a non-scorer nothing. That
member now scores, so the assertion has to be earned.

A re-audit of the fixed suite scored **0.95, CERTIFIED**, with one survivor.

Read the improvement carefully, because this is exactly where a kill-rate can be
oversold: mutants are generated fresh each run, so 0.90 → 0.95 is two samples,
not a controlled measurement. What is *not* a sample is that two specific faults
got through before and are now covered by tests checked against those faults
directly — apply the mutant, watch that test and only that test fail.

The tape above is left as it was recorded. It is a dated artifact of the commit
it graded, and `demo/thin-boundaries` in the repository preserves that exact
suite so the gaps stay readable rather than disappearing into a diff.
