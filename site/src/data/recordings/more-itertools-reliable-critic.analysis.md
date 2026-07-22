## What corral was asked

Certify a file from **more-itertools** — `more_itertools/recipes.py`, 67
functions we didn't write, from a well-loved zero-dependency library — against the
library's *own* test suite (`tests/test_recipes.py`), by execution. The
generation fanned out across shards so every function got probed, not just
whichever one a single generator happened to pick.

## What it found

Gemini 3.5 Flash planted 20 goal-violating mutants across the file; the
library's own suite, run in the jail against every one, killed **18 of 20** — a
**dev kill-rate of 90%**, measured, not asserted. That cleared the bar (0.8), so
the gate returned **CERTIFIED** and signed the verdict — offline-verifiable from
the record.

But certified is not "spotless." Two mutants **survived** the suite, and corral
signs them into the record rather than hiding them: this is
*certified-and-here's-what-you-missed*, not a rubber stamp. The two survivors are
disclosed and handed back for you to close — the gate proved your suite is strong
(90% of planted faults caught, in a jail) without pretending the last 10% isn't
there. Open the **tests** tab to see them highlighted against the code the suite
passed anyway.

## Why this one is worth watching: the critic that didn't hallucinate

The decorrelated **test-critic** here is a *stronger, different-tier* model —
**Gemini 3.1 Pro** — reading the suite cold while a lighter model did the
planting and writing. Its job is to flag tests that don't actually test anything.
It flagged one, and — the point — it **holds up under execution**:

- In `test_full_permutation`, the `if i == r` "not permuted" check is **dead**:
  `i` is a `range` object and `r` is a `tuple`, and in Python 3 `range == tuple`
  is always `False`. The check can never fire. The test still has a real
  `assertEqual` around it, so it isn't vacuous — but that one guard is inert, and
  the critic scoped its wording precisely to the check. Accurate, not overstated.

That precision is the point. An **earlier** run of this same file, with a lighter
same-vendor critic, produced a confident **hallucination** — it flagged
`test_negative_take` as vacuous, insisting `islice` silently swallows a negative
count. It doesn't: `take(-3, …)` **raises `ValueError`**, so the test passes for
exactly the right reason. A stronger, decorrelated critic makes no such mistake.

Either way, corral treats the critic's word as what it is: **unverified advice
that never gates the signed verdict** — only a jail and an exit code certify. But
watching a more reliable model produce *more reliable advice* is the whole helper
turn: better decorrelation makes your tests stronger from both ends, and every
claim on this tape is one you can re-run yourself.
