## What corral was asked

Certify an inclusive-range check — `Contains(lo, hi, x)` reports whether `x` is
within `[lo, hi]` — against a **thorough** suite that tests both boundaries and
the outside cases.

## What it found

The same decorrelated cross-vendor herd — **Claude Sonnet 5** planting faults,
**Gemini 3.5 Flash** grading — planted 7 faults. The suite killed **all 7**.
**CERTIFIED** — signed, clean, no survivors, no gap.

## Why this one is worth watching

It's the counterpart to the gappy runs. A **known-thorough** target, and the
audit certified it clean instead of manufacturing a problem. A gate that only
ever finds fault is useless theatre; this is the gate **passing work that
deserves to pass** — and signing it, so anyone can re-run the record and confirm.
Same herd, same method, opposite outcome. Both honest. That's the metric
calibrating in the other direction: on code whose suite is genuinely strong, the
survivors go to zero.
