## What corral was asked

Certify `sportspicker_core/awards.py` — the award arithmetic for a pick'em
scoring library we wrote — against its own suite (90 tests, green), by
execution. Same file this gallery has shown before, this time on `main`, not
the deliberately broken demo branch.

## What it found

Gemini 3.6 Flash planted **20** goal-violating mutants across the file. The
library's own suite, run in the jail against every one, killed **18 of 20 —
a 90% dev kill-rate**, clearing the 0.8 bar. The gate returned **CERTIFIED**
and signed the verdict.

**2 survivors** remained — the suite passed despite them. Corral doesn't stop
at the pass/fail line: the per-survivor writer went back in and authored a
compiling test for each one, and **both** proved catchable by execution — the
decorrelated Gemini 3.7 Flash critic then read the resulting suite and flagged
nothing further. So the honest shape of this run is not "certified, done" —
it's certified *and* both of the two gaps a green 90-test suite still had were
found and proven, not just counted.

## Why this one is worth watching

CERTIFIED is not the same claim as "nothing left to find." A 90% kill-rate
clears the bar and a verdict gets signed — and the tool kept going anyway,
proving out the two survivors a merge-worthy suite still missed. That's the
whole differentiator from a green CI check: passing isn't the end of the
measurement, it's where this one starts.
