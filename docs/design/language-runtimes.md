<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Language runtimes: pinning the gate, not just the logic

**Status:** design, 2026-08-28. Nothing built. Written after three gate fixes in
one week made the underlying problem visible.

## The problem, stated as a measurement defect

corral's compile gate decides whether a mutant is **graded** or **discarded as
invalid**. That decision sets the denominator of every kill rate it reports.

The gate is currently whatever the host happens to have installed:

| language | gate today | catches | needs |
|---|---|---|---|
| Go | `go vet ./...` | syntax, types, arity, unused, unreachable | Go toolchain |
| TypeScript | `tsc --noEmit` | full type check | node + typescript |
| Python | `py_compile` + `ruff` (optional) | syntax; undefined names, unused imports | interpreter + ruff |
| JavaScript | `node --check` + `oxlint` (optional, config-gated) | syntax; undefined names | node + oxlint + project lint config |
| Ruby | `ruby -c` | syntax only | ruby |

Both optional checks are resolved with `exec.LookPath` and silently skipped when
absent, because the caller treats any non-zero command as a rejection — so a
missing tool would fail the gate closed on nothing and mark **every** mutant
invalid.

That produces the defect:

> **The same audit, on the same commit, with the same models, can report a
> different denominator on two machines.** A mutant rejected as invalid on a box
> with ruff is graded on a box without it — and if it then fails the suite for
> the wrong reason, it is scored as a KILL.

This is the same shape as the three bugs already fixed this week: a
compiler-rejected mutant counted as caught (`fc26fe8`), a timed-out mutant
counted as caught (`9ea7121`), and an undefined-name mutant reaching grading in
Python. In every case an environmental accident moved a number that is supposed
to be a property of the code.

For a tool whose output is a **signed, third-party-verifiable record**, "it
depends what was installed" is not an acceptable answer.

## What is NOT the problem

**The plugin interface is fine.** `lang.Plugin` already carries `CompileCheck`,
`TestPaths`, `Preflight`, `WorkspaceRunEnv` and the role prompts. Adding ruff and
oxlint took a few lines each and needed no interface change. Nothing here argues
for replacing that seam.

The gap is not *where the logic lives*. It is *where the tools come from*.

## Three options

### A. Status quo plus disclosure

Keep `LookPath`, and record which checks actually ran in the signed record and
the scan ledger. Cheap, and it converts a silent difference into a visible one:
a reader can see that this scan's Python gate was syntax-only.

Does not make runs comparable — it makes them **legibly incomparable**, which is
strictly better than today and much weaker than B.

### B. Per-language runtime images

A `corral-runtime-python` image carries the interpreter, ruff at a pinned
version, and nothing else. The gate becomes a property of the image tag rather
than of the laptop. Two runs of the same audit produce the same denominator, and
a third party re-running a published row gets our answer.

Costs, stated plainly:

- **Distribution burden.** corral has no hosted tier, and "install one binary,
  point it at a repo" is a real strength. Images are a second thing to publish,
  sign, and keep current.
- **It changes the isolation story.** Today the audit runs in a bwrap jail with
  no network, and the GitHub Action deliberately uses the workspace substrate
  because an ephemeral runner IS the boundary. A container is a THIRD substrate,
  not a replacement for either — `--substrate jail|workspace|image`.
- **Startup cost per file.** Scoring already runs the suite once per mutant; a
  container per audit is fine, a container per mutant is not.

### C. A declared toolchain manifest, verified at preflight

The operator declares what the gate requires; corral refuses to start when it is
absent, rather than silently weakening. No images, no new substrate — it makes
the requirement explicit and fail-closed.

Weaker than B (it does not pin VERSIONS, and ruff's rule set moves between
releases — `E999` was removed) but far cheaper, and it composes with B later.

## Recommendation

**A now, C next, B when there is a reason beyond tidiness.**

A is a few fields in a record that already exists, and it removes the silent part
of the defect immediately. C makes the requirement explicit. B is the only option
that makes cross-machine denominators identical, and it should be driven by a
real need — a published dataset, or a user who cannot reproduce a row — rather
than by architectural preference.

The ordering matters because B is the one that changes what corral IS. A tool you
install is a different product from a tool that ships a container fleet, and the
current shape is part of why the audit can run offline, on a laptop, against a
private repo, with no vendor in the path.

## Ruby is the honest limit

No option above fixes Ruby. Method resolution happens at runtime, so
"undefined method" is not statically decidable — there is no `no-undef` to reach
for. RuboCop's `Lint/` department catches some of it (`Lint/UselessAssignment`,
`Lint/DuplicateMethods`, `Lint/UnreachableCode`); Sorbet and Steep type-check
annotated code only.

So a Ruby kill rate carries more gate-slack than a Go one, and that is a property
of the language rather than a gap in corral. **Cross-language kill rates are not
directly comparable**, and any published comparison must say so. Python showed a
12% invalid-mutant rate against Go's 21–46% before ruff landed — a gap that
partly measured gate strictness, not generator quality. That number was not
published for exactly this reason.

## What would make this urgent

- A published dataset spanning languages, where a reader can recompute a row.
- A user reporting a kill rate we cannot reproduce.
- A second optional check whose absence changes a headline number.

Until one of those lands, A and C are proportionate and B is speculative
infrastructure.
