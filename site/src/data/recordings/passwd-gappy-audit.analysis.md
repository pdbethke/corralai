## What corral was asked

Certify a password validator — valid *iff* length ≥ 12 **and** it contains an
uppercase letter, a lowercase letter, a digit, and a symbol — against a test
suite that only checks the **length**.

## What it found

A decorrelated cross-vendor herd — **Claude Sonnet 5** planting faults, a
*different* model, **Gemini 3.5 Flash**, grading — planted 8 faults. The suite
caught 4 and **missed 4**. Every survivor is a fault in the *character-class*
rules the suite never exercises. The gate returned **NEEDS-REVIEW** and signed
the verdict: it will not certify a suite that guards half the spec.

## Why this one is worth watching

This is a **known-adequacy** target from corral's own eval corpus: we
*deliberately* gave it a gappy suite (length-only), and the audit found exactly
the gap we planted — the character-class faults sailing straight through. That's
the whole point of the corpus — it lets us prove the metric against ground
truth: the gate isn't *inventing* gaps, it's finding the ones that are really
there. Open the **tests** tab to see a surviving fault highlighted against the
original code your suite passed anyway.
