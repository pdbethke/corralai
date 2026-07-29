# github-action

`action.yml` at the repo root wraps `corral certify --repo` as a GitHub composite
action, so a PR gets an adversarial test audit without anyone standing up a
jail: the runner is already an ephemeral, isolated VM with your toolchain on it,
so corral mutates your real checkout in place and runs your own test command
against it, then restores the tree.

## What it does

The action runs one command:

```
corral certify --repo "$GITHUB_WORKSPACE" --substrate workspace \
  --owner <github.repository_owner> --commit <github.sha> \
  --diff-base <base> -- <test-command>
```

- `--substrate workspace` (`internal/reposcan/cachekey.go`'s `SubstrateWorkspace`)
  tells corral to mutate the workspace checkout directly and grade each mutant with
  your own test command — no bubblewrap jail, no tree copy, no `go mod vendor`
  seed. **The runner is the isolation boundary, not corral.** Point this action
  only at a checkout you're fine seeing mutated mid-run — a throwaway CI
  checkout, never a working tree with uncommitted changes you care about.
  Mutations are applied and reverted in place; corral never commits and never
  pushes, but a crash mid-mutation on a machine you also use for other work is
  not a risk worth taking outside CI.
- `--diff-base <base>` scopes the audit to files the change touched, instead of
  the whole repo. **This is the default** — see below.
- `--owner` and `--commit` name the record. The report header is
  `Repo adequacy — <owner>/<repo> @ <commit>`, built from those two plus the
  basename of `--repo`; the action fills all three from what GitHub already
  knows (`github.repository_owner`, `github.sha`, and `$GITHUB_WORKSPACE`, whose
  basename is the repository name). A record that names nothing is not a record.

## Inputs never become script text

Every `${{ inputs.* }}` / `${{ github.* }}` value the action needs travels
through the step's `env:` block and is read back as an ordinary quoted shell
variable (`"$DIFF_BASE"`, `"$TEST_COMMAND"`, etc.) — never interpolated
directly into a `run:` script. This matters because GitHub expands `${{ }}`
into the script's literal text **before** bash ever sees the line; an input
interpolated that way is not data, it's code, and a value containing shell
metacharacters (`;`, backticks, `$( )`) executes. `env:` values don't have
that problem — GitHub still expands `${{ }}` there, but into an environment
variable's *value*, which the shell never re-parses as script.

The one deliberate exception is `test-command`: it's still meant to split
into argv the way a real shell parses a command line (`go test ./...`
becomes three separate arguments to `corral -- `, and `pytest -k "not
slow"` keeps `"not slow"` — quotes removed — as one argument, not two), so
the run step parses it with `xargs`, which does shell-style quote handling
*without being a shell*: it understands single and double quotes, but it
never evaluates `$( )`, backticks, `;`, `&&`, redirection, or globs. (`xargs
-d` is deliberately not used — that flag turns the quote handling off,
which is exactly the part being relied on.) An unmatched quote makes
`xargs` fail, and the step fails with it rather than silently parsing a
different command than the one written. A `test-command` containing an
embedded newline (the shape a YAML block scalar, `test-command: |`,
produces) is rejected up front for the same reason: quietly running only
its first line would grade a different, truncated command than the one in
the workflow file.

An **empty or whitespace-only** `test-command` is rejected the same way,
even though the input is declared `required: true` — GitHub does not
actually enforce that on composite-action inputs, and a reusable workflow
can still resolve `test-command` to `""`. Without a check, GNU `xargs`
runs its command once even on empty input unless told not to, producing a
one-element argv holding `""`, which corral would treat as an explicit,
empty test command and try to exec — every candidate fails its baseline
for an opaque reason, the exact undiagnosable shape this whole section
exists to prevent, arriving through a different door than the newline
case. `xargs -r` (no-run-if-empty) is also passed, as defense in depth.

This door has reopened four times now, each through a different INPUT
shape:

- A whitespace-only pre-parse check (above) catches the bare-empty and
  whitespace-only cases with a clear message, but not a literal
  empty-quoted value: `test-command: '""'` or `"''"` is not whitespace, so
  it survives that check, and is then reduced to a single zero-length word
  by `xargs`'s own quote removal — the exact mechanism that makes
  `pytest -k "not slow"` work. Reachable via a generated or reusable
  workflow that defensively quotes an interpolated value
  (`test-command: '"${{ inputs.cmd }}"'`) whose inner value resolves
  empty.
- A guard written as "reject an argv with zero elements, or exactly one
  element that's empty" is *still* a shape special-case, and
  `test-command: '"" ""'`, `"'' ''"`, and `'"" pytest'` all walk straight
  through it — two-or-more elements, first one empty. `certify_repo.go`
  honours any non-empty argv as an explicit test command and execs
  `argv[0]` — empty — for every candidate.

Guarding a shape of the input loses this game structurally: there is no
way to enumerate every shape that can become an unusable command, and each
attempt so far was walked around by the next one. The actual invariant is
about what corral *needs*, not what the input looked like: **there must be
at least one argv element, and the first one — the program name corral is
about to exec — must not be empty.** Nothing else about the argv's shape
matters; an empty argument anywhere *other than* the first position is
completely legitimate (`pytest ""`, `pytest -k ""` — see below). Stated
that way, over the value corral actually receives, the check holds
regardless of which input shape produced the argv, including shapes
nobody has constructed yet:

```bash
if [ "${#TEST_ARGV[@]}" -eq 0 ] || [ -z "${TEST_ARGV[0]}" ]; then
  echo "::error::test-command has no command to run (the program name is empty or missing)." >&2
  exit 1
fi
```

The pre-parse checks above are kept for their clearer error messages on
the common cases, not because they are what makes this safe — the
`argv[0]` check is.

The split itself is NUL-delimited (`xargs ... printf '%s\0'` into
`mapfile -d ''`), not newline-delimited, so a **trailing empty argument**
survives: `pytest ""` becomes two words, and `pytest -k ""` keeps the
empty value rather than silently dropping it. A newline-delimited version
(piping through `$( )` command substitution) would strip exactly that
trailing empty field, since `$( )` always strips trailing newlines — the
same ambiguity a plain `echo` has for a value that legitimately ends in
blank lines. Non-trailing empty arguments (`pytest -k "" -x`) round-trip
correctly either way, which is what makes the trailing case easy to miss.

`cmd/corral/action_test.go` covers all of this:
`TestActionTestCommandWordSplitNotEvaluated` proves a `test-command`
containing `;`, backticks, and `$( )` arrives as literal argv words instead
of running; `TestActionTestCommandStillSplitsAnOrdinaryCommand` and
`TestActionTestCommandPreservesQuotedWords` pin the two splitting shapes
above; `TestActionTestCommandUnmatchedQuoteFailsClosed`,
`TestActionTestCommandRejectsEmbeddedNewline`, and
`TestActionTestCommandEmptyFailsClosed` (bare-empty, whitespace-only, both
literal-quoted-empty shapes, and multi-element argvs whose first element
is empty — `'"" ""'`, `"'' ''"`, `'"" pytest'`) cover the failure modes;
`TestActionTestCommandPreservesEmptyArguments` pins the trailing/leading/
middle empty-argument cases, which must keep working precisely because
only `argv[0]` is checked.

What this does **not** give you is full shell fidelity: pipes (`|`), `&&`
/ `||` chaining, output redirection, and glob expansion in `test-command`
are not supported — `xargs`'s quote parsing covers quoting only. Write
`test-command` as a plain invocation (`go test ./...`, `pytest -k "not
slow"`), not a shell one-liner that chains multiple commands.

## Usage

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0
- uses: pdbethke/corralai@main
  with:
    test-command: "go test ./..."
    model-key: ${{ secrets.ANTHROPIC_API_KEY }}
```

**There is no `v1` tag.** The action ships on `main`; the repo's cut tags
(`v0.1.0`, `v0.2.0`) predate it, so no released tag contains an `action.yml`.
Use `@main`, or pin the commit SHA you reviewed (`pdbethke/corralai@<sha>`) if
you want an immutable reference. This document will name a version tag when one
is actually cut, and not before.

That's the whole workflow — you don't install `corral` yourself. The one
requirement is a `go` binary on the runner's `PATH`; GitHub-hosted runners
ship one, and this action installs `corral` with it (see below). A
self-hosted runner with no Go toolchain at all will fail fast on a clear
error rather than a bare "command not found".

## Installing `corral`

The action installs `corral` itself — there is nothing to add before it. Its
first step runs `go install github.com/pdbethke/corralai/cmd/corral@<ref>`
into a private `GOBIN` under `$RUNNER_TEMP`, then prepends that directory to
`$GITHUB_PATH` so the second step finds `corral` on `PATH`. This deliberately
does **not** use `actions/setup-go`: that action replaces the toolchain on the
runner, and for a Go project this action is auditing that would silently
change the toolchain the project's own test suite runs under — corrupting the
very thing corral is measuring. Instead it uses whatever `go` binary the
runner already has. If `go` is not on `PATH` at all, the step fails fast with
a message telling you to add `actions/setup-go` yourself, rather than dying
deep inside on a bare "command not found".

`<ref>` defaults to `${{ github.action_ref }}` — the ref the action itself was
resolved at — so the installed binary always matches the action version you
pinned in `uses:`. Override it with the `corral-version` input if you need a
different `corral` release than the action's own ref (rare). If
`github.action_ref` is empty — this happens for a local `uses: ./` reference
(not exercised by this repo's own CI today; nothing in `.github/workflows/`
currently references this action that way) — the install step falls back to
`corral@main` and logs a warning saying so.

`go install <path>@<version>` has been module-independent since Go 1.16: it
builds the requested module in its own module cache, not against your
project's `go.mod`. The audited checkout's `go.mod` and `go.sum` are
untouched — verified by diffing them byte-for-byte before and after a real
`go install` of this repo's `cmd/corral` in this project's own tree.

## `fetch-depth: 0` is required

`--diff-base` computes the changed-file set with a **three-dot** git range
(`<base>...HEAD`, i.e. against the merge base), because that's what "what this
PR changed" means — a two-dot compare would also catch files that changed on
the base branch after the fork point. GitHub's default checkout is
**depth 1**, which has no merge base to find. On a shallow checkout the diff
computation fails closed (exit 1, not a silent full-repo scan) — so the
single most common way this action breaks on a first run is a missing
`fetch-depth: 0` on the checkout step above it. Set it.

When `diff-base` is left empty on a `pull_request` event, the action fetches the
base branch itself before computing the diff:

```
git fetch --no-tags origin "+refs/heads/$GITHUB_BASE_REF:refs/remotes/origin/$GITHUB_BASE_REF"
```

Both halves of that are load-bearing. The **explicit refspec** is what actually
creates `refs/remotes/origin/<base>`; a bare `git fetch origin main` updates only
`FETCH_HEAD`, and `actions/checkout` configures a single-ref refspec that does not
cover the base branch — so `origin/main` would simply not exist and the run would
die on `unknown revision`. And there is deliberately **no `--depth`**: a shallow
fetch writes `.git/shallow` and truncates the base's ancestry, which destroys the
very merge base `fetch-depth: 0` was set to provide (`no merge base`, exit 1).

## Audited files are graded one at a time

On this substrate the scan runs **one worker**, whatever `--swarm` says, and the
run's readout says so. There is exactly one checkout, and every job mutates it in
place: two jobs at once would mean one job's suite running while the other job
has a mutant — or corral's deliberately non-compiling canary — written into a
file, which silently records surviving mutants as killed and can fail a healthy
baseline. Giving each job its own copy of the tree is the memory ceiling this
substrate exists to escape, so serialization is the accepted cost. Combined with
`--diff-base` scoping (the default), it is a cost measured against the handful of
files a PR touched.

## Why scoped by default, and why whole-repo is opt-in

Auditing one file runs a full adversarial herd against it — generate mutants,
run the project's real suite against each one, repeatedly — roughly 84 suite
runs per audited file, against CI's one. That's a normal cost for the three
files a PR actually touched; it is not for every file in the repo. Leave
`diff-base` at its default (the PR's base ref) and the action audits only
what changed. Passing an empty `diff-base` audits the whole repository instead
— expensive, and something you should opt into deliberately, not something
this action does by default.

A file the diff didn't touch is still counted in the report's denominator, as
`not-selected` — a scoped run reports a genuinely low coverage fraction of the
repo, on purpose. It's telling you what it covered, not claiming the whole
repo passed.

A diff that touches no auditable candidate (a docs-only PR, or one that only
touches files with no paired test) is a legitimate pass: the action prints
`NOTHING IN SCOPE:` and exits 0.

## Coverage pre-flight (`--preflight`) — CLI only, not yet an Action input

`corral certify --repo` has a separate `--preflight` flag this action does
not expose (there is no `preflight` `with:` input yet — pass it by invoking
`corral` yourself instead of through the composite action, if you want it in
CI today). It answers a different question than the audit above: not "how
adequate are this repo's tests" but "which files does the suite ever touch
at all" — useful on its own, especially for a repo where the audit's own
test-pairing finds almost nothing (a common state for a JS/TS project, which
this flag also cannot help with — see below).

It runs the project's test suite **one extra time**, instrumented for
coverage, and reports three buckets, printed only when the flag is given:

- **executed** — files the suite ran at least once. Not a finding; the point
  of the flag.
- **measured and never executed** — files the instrumentation watched and
  recorded zero executions for. This is the actual finding, and the only
  bucket printed **by name**.
- **not measured at all** — files outside the instrumented run's own scope,
  or with nothing measurable in them (e.g. a genuinely empty `__init__.py`
  the run legitimately never records either way). Printed **only as a
  count**. A file lands here for reasons that have nothing to do with
  whether it's tested — coverage.py's own `[tool.coverage.run] source =`
  scoping is one; a zero-statement file is another — so naming one would be
  an accusation about a file the run never actually looked at.

**Coverage-grade evidence, not proof.** A `measured and never executed`
verdict means the instrument saw nothing; it cannot see everything —
subprocess calls, dynamic imports, and native extensions are common blind
spots for both `coverage.py` and Go's own instrumentation. Treat a finding
here as "worth a look", not as a certified defect the way a killed mutant is.

**Go and Python only.** The pre-flight is implemented for exactly two
languages (`go test -coverpkg=./... -coverprofile=…` and `pytest`/`coverage
run` + `coverage json`). Ruby, JavaScript, and TypeScript have no
coverage-pre-flight plugin at all — this project does not document
capability it hasn't built, so a scan in one of those languages, or a repo
whose candidate set spans more than one language at once (one instrumented
run cannot cover two languages), reports `could not run: …` and names zero
files, rather than guessing or silently doing nothing. The same fail-closed
report is what you get when the coverage tool itself isn't installed on the
runner (`coverage`/`pytest-cov` missing from the Python environment, for
example) — `--preflight` never treats "the tool didn't run" as "nothing is
covered".

**Cost: one suite run, but "one run" is not "instant".** The design's claim
is O(1) in the number of source *files* — one instrumented invocation
classifies every file in the repo, instead of the ~84-suite-runs-per-file
the adversarial audit costs. That held on every repo in the foreign-repo
sweep this flag was proven against: exactly one suite invocation regardless
of file count. But the flag's wall clock is still bounded below by however
long the project's *own* test suite takes to run once — a repo with a large,
network-heavy suite pays that cost every time, independent of how many
source files it has (`psf/requests`: 174 files walked, coverage pre-flight
wall clock ≈80s, dominated by tests that make real network calls; `pallets/
flask`: 321 files walked, ≈3s, because its suite itself runs in under a
second). And on Go, closing a real false-positive found during that same
sweep (below) required adding `-coverpkg=./...`, which makes the *profile
size* — not the wall clock — scale with how many tested packages import a
given file, not just with repo size; it is bounded the same way the rest of
the pre-flight's output is (a hard cap, reported as `could not run:
…truncated…` rather than a silently partial result, never a partial finding
list).

**A false accusation this flag caught in itself, on this sweep.** Proving
this flag against `gin-gonic/gin` surfaced a real bug, not a documentation
gap: without `-coverpkg=./...`, `go test -coverprofile=… ./...` only
instruments each package's *own* tests, so a package with no `_test.go`
files of its own always reports synthetic all-zero coverage — even when its
exported code runs constantly via a shared interface from another package's
tests. `codec/json/json.go` was reported `measured and NEVER executed` under
the old command; the root package's own `errors_test.go` calls
`json.API.Marshal`, which dispatches straight into that file, every run.
Fixed by adding `-coverpkg=./...` (`internal/lang/go.go`); re-verified on
the same repo, same commit, same suite — the finding disappears, because it
was never real. This is the standard this feature is held to: a false
`measured and never executed` verdict is worse than no pre-flight at all,
and the fix belongs in the code, not a caveat in these docs.

## Inputs

| Input | Required | Default | Meaning |
|---|---|---|---|
| `test-command` | yes | — | The command that runs your tests, as a single-line invocation (e.g. `go test ./...`, `pytest -k "not slow"`). Quoting is honoured; pipes, `&&`/`\|\|`, redirection and globs are not — see "Inputs never become script text" above. |
| `diff-base` | no | `""` (falls back to the PR's base ref) | Audit only files changed against this ref. Left empty on a `pull_request` event, the action falls back to `origin/$GITHUB_BASE_REF` (the PR's own base). On any other event (e.g. a push to `main`), there is no base ref to fall back to, so an empty `diff-base` means a whole-repo audit. |
| `goals` | no | `""` | Optional JSON file of per-file goals. Omitted means goals are derived per file by a model. |
| `min-kill-rate` | no | `""` (unset) | Fail the run (exit 1) if **any individual audited file's** kill rate is below this value. Range 0.0-1.0 inclusive; a *minimum*, so a file exactly at the value passes. Opt-in — leave empty to keep the pre-`min-kill-rate` behaviour, where a weak-but-gradable suite still exits 0. See "Failing on a weak kill rate" below. |
| `model-key` | no | `""` | Provider API key for goal derivation, wired into the run as `ANTHROPIC_API_KEY` — the same environment variable corral's default model backend reads everywhere else (`internal/creds`). Required unless `goals` is supplied. Pass it as `${{ secrets.ANTHROPIC_API_KEY }}`; never write a key inline in the workflow. |
| `corral-version` | no | `""` (falls back to the action's own ref, `github.action_ref`) | Which `corral` to `go install`, as a version suffix (a tag, branch, or commit). Leave it empty unless you deliberately want a different `corral` release than the action you pinned in `uses:`. |

## Failing on a weak kill rate

By default, a scan that successfully grades a file exits 0 **no matter what
kill rate it measured** — a file with every mutant surviving (0.00) merges
exactly as cleanly as one with a perfect score. That is deliberate: this
input is opt-in, and giving it a default would silently change the exit code
of every existing caller of this action.

Set `min-kill-rate` (a number from `0.0` to `1.0`) to give the gate teeth:

```yaml
- uses: pdbethke/corralai@main
  with:
    test-command: "go test ./..."
    model-key: ${{ secrets.ANTHROPIC_API_KEY }}
    min-kill-rate: "0.7"
```

The check is **per file, not on the aggregate**. If any one audited file
scores below the threshold, the whole scan exits 1 — a well-tested file
elsewhere in the PR cannot average out, or mask, a weak one. `0.7` here means
*at least* 70%: a file scoring exactly `0.70` passes (`min-kill-rate` is a
minimum, checked inclusively), and a file at `0.69` fails the run.

When a run breaches the threshold, the human-readable report names every
breaching file and by how much, on its own `KILL-RATE BREACH:` line —
distinct from `COULD-NOT-GRADE:`, which means something different (nothing
was measured at all, rather than measured and found weak):

```
  KILL-RATE BREACH: 1 file(s) below --min-kill-rate 0.70:
    0.40  pkg/widget.go (0.30 below threshold)
```

This is independent of, and decided after, the two existing zero-file
outcomes: an empty `--diff-base` scope (`NOTHING IN SCOPE:`, exit 0 — nothing
was ever meant to be measured) and a non-empty scope where nothing could be
graded (`COULD-NOT-GRADE:`, exit 1) both still take priority over the
threshold check, exactly as they did before this input existed. A threshold
can only fail a run that actually produced at least one real kill-rate
measurement.

## Exit codes

- **0** — the scan ran and graded at least one file, and (if `min-kill-rate`
  was given) every audited file met it; or nothing was in scope at all (a
  docs-only PR is a legitimate green). **With `min-kill-rate` left unset**, a
  weak-but-gradable suite still exits 0 regardless of its score — read the
  report for the number, or set `min-kill-rate` if you want CI to block on it.
- **1** — a real failure: either files were in scope and *none* of them could
  be graded at all (`COULD-NOT-GRADE:`, e.g. every candidate's baseline suite
  was already broken or flaky), enumeration failed, or (only when
  `min-kill-rate` was given) at least one audited file scored below it
  (`KILL-RATE BREACH:`). Per-file goal derivation that runs and comes back
  empty for every file lands here too — the scan happened, it just graded
  nothing.
- **2** — the run never started: a usage error (bad flags, including a
  `min-kill-rate` that doesn't parse or falls outside 0.0-1.0), or the goal
  deriver could not be CONSTRUCTED at all — the usual cause being no
  `model-key` (and no `goals` file) to derive goals with. Distinct from exit
  1: nothing was attempted, so nothing is being reported about your code.
