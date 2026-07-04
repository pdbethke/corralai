## What the herd was asked

Build a Node.js module `lru` — an `LRUCache` class with `get`/`set`,
capacity-based eviction of the least-recently-used entry, and `size`, plus
JSDoc, a README, and `node:test` tests, gated on `node --test` passing.

## How it went

This tape is the second attempt. The first died in a verify-refusal
livelock: the gate (correctly) refused an unverified completion 35 times
while a long-done agent's stale path lease pinned the artifact nobody could
then fix. We kept that run's full evidence, disqualified it, and fixed the
brain — completions now release their leases, and a refusal loop trips a
loud escalation after five refusals instead of spinning silently.

Attempt two converged in 14.5 minutes on the same single RTX 5070: the plan
grew from 11 tasks to 29 as the reflex re-planner turned findings into fix
tasks, and the herd filed 34 findings along the way — a much noisier, more
adversarial run than the Python one, which is what a gated language the
model is shakier in looks like.

## The honest warts

The final workspace fails its own `node --test`: the cache logic is right,
but `lru.js` never exports the class — a one-line `module.exports` miss the
mid-mission verify pass didn't catch, because the gate records that a
passing run happened, not that the final state still passes. A stray
`workspace/` directory of build-script scraps shipped too. Published
unedited, as always.

## What we learned

Finding-heavy missions are where coordination actually gets tested: this
directive found one livelock bug (fixed), one lease-hygiene gap (fixed),
and a claim-path normalization wart (filed). The recording is the sales
pitch and the bug report at once — that's the point of publishing tapes.
