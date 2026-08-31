## What corral was asked

Certify `src/index.ts` from **vercel/ms** — the duration parser/formatter
underneath a huge share of the Node ecosystem's timeouts and cache TTLs —
against the project's own jest suite (167 tests), by execution, in the jail.

## The run that couldn't grade, first

The recording that ships here is the *second* attempt. The first came back
**COULD-NOT-GRADE**: the dev suite didn't even pass on the clean, unmutated
code inside the jail. The failure wasn't a real bug in `ms` — jest's own
loader threw `Cannot find module 'import-local'` trying to launch
`jest/bin/jest.js` inside the sandboxed copy. The jail had bound
`node_modules` in read-only, exactly as the log records — but a bound
directory of *files* isn't the same guarantee for every install layout. Some
package managers (pnpm's content-addressable store is the sharpest example)
install `node_modules` as a tree of symlinks pointing *outside* that
directory, into a global store; bind-mount the tree without the store behind
it and a symlink resolves to nothing, however complete the top-level listing
looks. Corral called that what it was — a build/environment failure, not a
test-quality verdict — and graded zero of the 34 mutants it had already
generated rather than pretend a broken baseline proved anything.

## What the corrected run found

With the dependency layout the jail could actually resolve, Gemini 3.6 Flash
planted **34** goal-violating mutants across 7 sharded regions of the file.
The project's own 167-test jest suite, run against every one, killed **32 of
34 — a 94% dev kill-rate**, clearing certify. The gate returned **CERTIFIED**
and signed the verdict.

**2 survivors** remained. The per-survivor writer fanned out, hit one
`tsc --strict` compile failure on the first pass (a relative import missing
its explicit extension under `nodenext` module resolution — reissued and
fixed), then had one authored test fail on the clean baseline and get
reissued again with the failure fed back. On the third pass, both survivors
came back **proven catchable by execution** — not asserted, not left as
`0 of 2`.

## Why this one is worth watching (an operator note)

The lesson isn't "vercel/ms has a bug" — it doesn't, by this measurement.
It's that a mutation jail's isolation guarantee is only as good as what it
actually bind-mounts, and a symlink-based install can look complete on `ls`
while resolving to nothing inside the sandbox. If a "clean" baseline fails
inside the jail with a module-resolution error and passes fine on the host,
check what your package manager actually put on disk before assuming the
target project is broken — corral's own COULD-NOT-GRADE status exists
precisely so that question gets asked instead of silently swallowed into a
wrong verdict.
