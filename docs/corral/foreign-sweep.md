# The foreign-repo sweep

`scripts/foreign-sweep.sh` runs
`certify --repo --dry-run` — enumeration, language detection, test pairing,
ambiguity demotion, ranking, and selection, but **no audit and no suite
execution** — against eight SHA-pinned real-world repos nobody on this
project wrote (nine rows: `expressjs/express` is scanned twice, with and
without a `--tests` map), and diffs the walked/candidate/ambiguous file counts against
the checked-in golden file `testdata/foreign-sweep-expected.tsv`. It exists
because pointing corral at foreign repos surfaces defects the in-repo suite
never can (a suite only ever exercises repos shaped like this one); see the
script's own header comment for the incident that motivated it. It runs in
`.github/workflows/foreign-sweep.yml` on every pull request and every push
to `main` — not just after merge — and needs only network access and the Go
toolchain (no model key, no jail, no language toolchain beyond Go).

If a change legitimately moves one of these counts (a pairing improvement
that finds candidates it used to miss, a new demotion rule, etc.), regenerate
the golden file and commit the diff:

```
rm testdata/foreign-sweep-expected.tsv
FOREIGN_SWEEP_BOOTSTRAP=1 bash scripts/foreign-sweep.sh   # writes a fresh golden file
git diff testdata/foreign-sweep-expected.tsv
```

A missing golden file is refused (exit 1), not silently bootstrapped: if
it's ever lost to a bad rebase or a careless PR, the gate must fail loudly
rather than quietly regenerate itself against whatever the tree happens to
produce and stay green forever. `FOREIGN_SWEEP_BOOTSTRAP=1` is the explicit
opt-in that says "yes, I mean to write a new golden file right now."

**Moving these numbers is a deliberate act, not something to do reflexively
to make the gate pass.** The diff is the whole point — review it like a
production change, because a golden file that silently absorbs a regression
(a pairing rule that quietly finds *fewer* real candidates, say) is worse
than no gate at all. `expressjs/express` is pinned to pair **zero** test
candidates on purpose: that's a known JS/TS test-pairing limitation, not a
bug, and if a future change makes that number nonzero, look hard at *why*
before accepting the new golden file.

That `express` pin is a **one-directional** canary: it catches JS/TS test
pairing going from zero candidates to nonzero, but would not notice the
JS/TS plugin being removed or `express` being dropped from language
detection entirely — a "still zero" diff looks identical to both "still
broken as expected" and "the whole plugin silently disappeared." `gin-gonic/gin`
is the positive canary that covers that direction (a live, working plugin
whose count must never go to zero). The asymmetry is inherent to pinning a
zero and doesn't prove more than it does.

