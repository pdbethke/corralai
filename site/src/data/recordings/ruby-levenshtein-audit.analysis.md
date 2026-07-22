## What corral was asked

Certify `lib/text/levenshtein.rb` from **threedaymonk/text** — a real, pure-Ruby
edit-distance implementation (UTF-8 aware) with a minitest suite — against its
own tests, by execution. The goal: `distance(a, b)` returns the minimum number of
single-character insertions, deletions, or substitutions to turn `a` into `b`.

## What it found

Run head-on, the suite looks pristine: **35 tests, 5,241 assertions, 100%
passing.** Then corral graded it by execution. Claude Sonnet 5 planted 5
goal-violating mutants in the algorithm; the library's own minitest suite, run in
the jail against each, killed only **3 of 5 — a 60% kill-rate** with **2
survivors**. That is *below* the bar (0.8), so the gate returned **NEEDS-REVIEW**
and handed the two surviving faults back.

This is the gap the whole tool exists to expose: a green, assertion-heavy suite
that still lets two goal-violating edits through undetected. Passing count is not
adequacy — and corral says so by execution rather than by vibe. Open the
**tests** tab to see the two survivors highlighted against the code the suite
passed anyway.

## Why this one is worth watching

The decorrelated critic (Haiku 4.5), reading the suite cold, flagged something
tangential but telling: a helper in the *test file itself*
(`LevenshteinGeneratedDataTest#substitute`) references an undefined local — `se`
where `seq` was meant — which raises a `NameError` on the path that hits it. It's
the critic noticing a latent break inside the tests that grade the code. That
finding is **advisory, marked unverified, and never part of the signed 60%** —
only the jailed kill-rate certifies — but it's a second, independent reason to
look twice at this suite.

Same loop as the Go and Python audits, in a third language (Ruby, minitest):
plant faults, run the dev's *own* suite in a jail, grade by the kill-rate — no
self-report. Here the honest answer was *not yet* — and that refusal, on a suite
that passes 100% of its own assertions, is the point.
