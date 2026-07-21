## What corral was asked

Certify `version4.go` from **google/uuid** — one of the most-used Go libraries
there is — against the package's own test suite, by execution. The goal: `New` /
`NewRandom` return a valid RFC-4122 version-4 UUID (122 random bits, the version
nibble set to 4, the variant bits set to RFC 4122).

## What it found

Claude Sonnet 5 planted 20 goal-violating mutants across the file (sharded four
ways); google/uuid's own suite, run in the jail against every one, killed
**13 of 20 — a 65% kill-rate**, and **7 survived**. The gate returned
**NEEDS-REVIEW** and signed the verdict. This is the point worth sitting with: a
library this widely trusted, with a real test suite, still leaves a third of the
planted faults uncaught.

The decorrelated critic (Haiku 4.5) put its finger on the shape of the gap:
`TestRandomUUID` calls `New()` many times and asserts each result has version 4
and the RFC-4122 variant — but it **never verifies the 122 bits are actually
random**. So a mutant that fixes or narrows the randomness (while keeping the
version/variant bytes correct) sails straight past. The suite checks the
*structure* of a UUID thoroughly and its *randomness* not at all. (The critic's
read is unverified advice, marked as such; the 65% is what the jail measured.)

## Why this one is worth watching

It's the "your tests suck" thesis on code nobody would call badly tested. corral
doesn't grade google/uuid as *bad* — it grades it *by execution* and hands back
exactly which faults its suite can't see. Open the **tests** tab to watch a
surviving mutant highlighted against the code the suite passed anyway.
