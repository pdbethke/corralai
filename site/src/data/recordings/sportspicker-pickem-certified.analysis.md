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
