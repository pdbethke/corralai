## What corral was asked

Certify `lib/text/levenshtein.rb` from **threedaymonk/text** — a real, pure-Ruby
edit-distance implementation (UTF-8 aware) with a minitest suite — against its
own tests, by execution. The goal: `distance(a, b)` returns the minimum number of
single-character insertions, deletions, or substitutions to turn `a` into `b`.

## What it found

Claude Sonnet 5 planted 5 goal-violating mutants in the algorithm; the library's
own minitest suite, run in the jail against each, killed **4 of 5 — an 80%
kill-rate** with **1 survivor**. That cleared the bar, so the gate returned
**CERTIFIED** and signed the verdict. A thorough algorithmic suite mostly does
its job — and corral says so honestly, rather than only ever finding fault.

The decorrelated critic (Haiku 4.5) flagged something tangential but telling: a
helper in the *test file itself* references an undefined local (`se` where `seq`
was meant), which would raise a `NameError` if that path ran. It's the critic
reading the suite cold and noticing a latent break in the tests — advisory,
marked unverified, never part of the signed 80%.

## Why this one is worth watching

A real algorithm in a third language (Ruby, minitest), same loop as the Go and
Python audits: plant faults, run the dev's *own* suite in a jail, grade by the
kill-rate — no self-report. It's the counter-example that keeps the gate honest —
a genuinely solid suite certifies, and the one survivor is still handed back for
you to close.
