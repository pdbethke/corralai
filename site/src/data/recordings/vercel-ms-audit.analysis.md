## What corral was asked

Certify `src/index.ts` from **vercel/ms** — the tiny, ubiquitous
duration-string library (`ms('2 days')` → `172800000`, and back) — against its
own **Jest** suite, by execution. This is the first recording of a **real
TypeScript project audited with its real, dependency-heavy test toolchain**.

## The part that made this recording possible

ms's test run pulls in a **337 MB `node_modules`** (Jest, its TS transform, the
lot). corral's jail seeds a copied workspace with a size cap, and 337 MB blows
straight through it — so until recently this audit *failed at the seed stage*,
before a single test ran. The fix, shipped just before this run: corral
**bind-mounts dependency directories read-only** into the jail instead of copying
them. You can see it in the run's first line — `deps: bound 1 dir(s) read-only
(node_modules)`. Deps must be *present* (vendored, exactly as CI installs them);
corral binds them, never installs them. That's what lets it audit real-world
JS/TS code and not just zero-dependency toys.

## What it found — and a lesson about certify-by-execution

Claude Sonnet 5 planted 5 mutants; ms's own Jest suite killed **4 of 5 — 80%**,
1 survivor, **NEEDS-REVIEW**, signed.

Getting an *honest* number here took one correction worth telling on ourselves.
corral certifies by **exit code** — it runs your test command and trusts what the
process returns. ms's Jest config enforces a **100% coverage threshold**, so the
command exits **non-zero even when every test passes** (coverage sits at 98.4%).
On the first run that made the exit code meaningless: every mutant "failed" for
the coverage gate, not for catching a bug, and the kill-rate signal was garbage.
Re-running with coverage off — so the exit code reflects *test* pass/fail — gave
the real 80%. The takeaway is a genuine edge of execution-gated auditing: **a
suite whose exit status is dominated by a coverage (or lint) gate confounds a
tool that reads exit codes.** corral is honest about what it can and can't infer
from a process's return.

## Why this one is worth watching

Real TypeScript, a real Jest suite, real vendored dependencies — the exact shape
of code the earlier toy recordings couldn't prove out — audited by execution to a
signed verdict. Open the **tests** tab to see the surviving fault against the code
ms's suite passed anyway.
