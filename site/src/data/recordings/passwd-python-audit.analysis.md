## What corral was asked

Certify a password validator — valid *iff* length ≥ 12 **and** it contains an
uppercase letter, a lowercase letter, a digit, and a symbol — against a Python
suite that only ever feeds one *valid* password (and one too-short one), so it
never exercises the four character-class rules.

## What it found

Claude Sonnet 5 planted 5 goal-violating mutants; the developer's own suite,
run in the jail against every one, killed **0 of 5**. Every survivor is a
dropped character-class check the length-only test can't see. A **test-writer**
(Sonnet 5) then authored a test that killed **all 5** survivors — proving the
gaps are real and catchable, not equivalent mutants. The gate returned
**NEEDS-REVIEW** and signed the verdict: it will not certify a suite that guards
a fraction of the spec. The decorrelated critic (Haiku 4.5) independently flagged
both tests as vacuous — and here it was **right**: neither can fail on a
character-class mutation, and that's exactly what the execution showed.

## Why this one is worth watching

The same blind spot as the Go password recording — a length-only test that sails
past every character-class fault — now in **Python**, from corral's own
known-adequacy eval corpus. It's the whole loop in about forty seconds: find the
gap by execution (a 0% kill-rate, measured, not asserted), *prove* it (a written
test that kills every survivor), and grade the suite honestly (needs-review,
signed). Open the **tests** tab to watch a surviving fault highlighted against
the code the suite passed anyway.
