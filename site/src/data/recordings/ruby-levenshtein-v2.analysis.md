## What corral was asked

Certify `lib/text/levenshtein.rb` from **threedaymonk/text** — a small,
zero-dependency UTF-8 edit-distance implementation we didn't write — against
the gem's own minitest suite, run through `rake`, by execution.

## The free refusal, first

The first attempt against this file never spent a token. The operator's test
command named a single spec file directly, and corral writes its killing test
*beside* the developer's own tests — `test/levenshtein_corral_test.rb` — which
a single-file command will never collect. Corral checked that before running
any model: **your command does not collect that file, so this audit could not
prove a gap even if it found one.** It refused, in 2.5 seconds, for free, and
told the operator exactly what to widen the command to (a directory or the
runner's own discovery, not one spec path). Nothing was spent on a verdict
that couldn't have meant anything.

## What the corrected run found

With the test command widened, Gemini 3.6 Flash planted **9** goal-violating
mutants across the file's three functions. The gem's own suite, run in the
jail against every one, killed **7 of 9 — a dev kill-rate of about 78%**. That
left the gate short of certify, and it returned **NEEDS-REVIEW**, signed.

**2 survivors** remained. The per-survivor writer authored a compiling test
for each and **proved 1 of the 2 catchable by execution** — a real gap in the
gem's own suite, not a hypothetical one. The second stayed unresolved: either
a genuine untested edge the writer couldn't pin down in the run, or an
equivalent mutant with no observable effect. Corral discloses it rather than
rounding it away, and doesn't call it a defect on its own authority.

## Why this one is worth watching

Two lessons, not one. First: the pre-spend check is real money-in-the-bank —
a misconfigured command gets refused before a single model call, not after a
paid run produces a verdict that was structurally incapable of proving
anything. Second: "1 proven" is not "1 of 2, so half-credit." It means one
survivor now has a compiling test in hand that kills it, and one is still
open and honestly labeled that way — the two are not interchangeable claims,
and this tape is the difference in one screen.
