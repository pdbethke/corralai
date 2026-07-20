## What corral was asked

Certify a file from **more-itertools** — `more_itertools/recipes.py`, 67
functions we didn't write, from a well-loved zero-dependency library — against the
library's *own* test suite (`tests/test_recipes.py`), by execution. The
generation fanned out into six shards so every function got probed, not just
whichever one a single generator happened to pick.

## What it found

Gemini 3.5 Flash planted 30 goal-violating mutants across the file; the
library's own suite, run in the jail against every one, killed **26 of 30** — a
**dev kill-rate of 87%**, measured, not asserted. Four survivors slipped through
(dropped `maxlen` handling in `running_min`/`running_max`, a missed `key=` in
`unique`, an order-loss in `random_combination_with_replacement`). A
**test-writer** then authored a single test that kills all four — proving the
gaps are real and catchable. The gate returned **CERTIFIED** and signed the
verdict; it's offline-verifiable from the record.

## Why this one is worth watching: the critic that didn't hallucinate

The decorrelated **test-critic** here is a *stronger, different-vendor-tier*
model — **Gemini 3.1 Pro** — reading the suite cold while a lighter model did
the planting and writing. Its job is to flag tests that don't actually test
anything. It flagged two — and, unusually, **both hold up under execution**:

- In `test_replacement`, the trailing `if len(set(combo)) == len(combo)` "no
  duplicates" check is **dead**: `combo` is ten elements drawn *with
  replacement* from five, so it can never have ten distinct values (we confirmed
  it — max five distinct across two thousand draws). The check can't fail.
- In `test_full_permutation`, the `if i == r` "not permuted" check is **dead**:
  `i` is a `range`, `r` is a `tuple`, and in Python 3 `range == tuple` is always
  `False`. The check can't fire.

Both are dead *checks* sitting inside otherwise-live tests (each still has a real
`assertEqual`), and the critic scoped its wording precisely to "the check" —
accurate, not overstated. We verified every claim by running it.

That precision is the point. An **earlier** run of this same file, with a
lighter same-vendor critic, produced a confident **hallucination** — it flagged
`test_negative_take` as vacuous, insisting `islice` silently swallows a negative
count. It doesn't: `take(-3, …)` **raises `ValueError`**, so the test passes for
exactly the right reason. A stronger, decorrelated critic here made no such
mistake — and found two real dead checks the lighter one missed entirely.

Either way, corral treats the critic's word as what it is: **unverified advice
that never gates the signed verdict** — only a jail and an exit code certify. But
watching a more reliable model produce *more reliable advice* is the whole helper
turn: better decorrelation makes your tests stronger from both ends, and every
claim on this tape is one you can re-run yourself. Open the **tests** tab to see
the survivors highlighted against the code the suite passed anyway.
