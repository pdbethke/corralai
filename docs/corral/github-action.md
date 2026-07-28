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

## Inputs

| Input | Required | Default | Meaning |
|---|---|---|---|
| `test-command` | yes | — | The command that runs your tests, as a single-line invocation (e.g. `go test ./...`, `pytest -k "not slow"`). Quoting is honoured; pipes, `&&`/`\|\|`, redirection and globs are not — see "Inputs never become script text" above. |
| `diff-base` | no | `""` (falls back to the PR's base ref) | Audit only files changed against this ref. Left empty on a `pull_request` event, the action falls back to `origin/$GITHUB_BASE_REF` (the PR's own base). On any other event (e.g. a push to `main`), there is no base ref to fall back to, so an empty `diff-base` means a whole-repo audit. |
| `goals` | no | `""` | Optional JSON file of per-file goals. Omitted means goals are derived per file by a model. |
| `model-key` | no | `""` | Provider API key for goal derivation, wired into the run as `ANTHROPIC_API_KEY` — the same environment variable corral's default model backend reads everywhere else (`internal/creds`). Required unless `goals` is supplied. Pass it as `${{ secrets.ANTHROPIC_API_KEY }}`; never write a key inline in the workflow. |
| `corral-version` | no | `""` (falls back to the action's own ref, `github.action_ref`) | Which `corral` to `go install`, as a version suffix (a tag, branch, or commit). Leave it empty unless you deliberately want a different `corral` release than the action you pinned in `uses:`. |

## Exit codes

- **0** — the scan ran and graded at least one file (whatever its kill rate
  came out to), or nothing was in scope at all (a docs-only PR is a legitimate
  green). **The action does not currently fail the merge on a low kill
  rate** — a weak-but-gradable suite still exits 0. Read the report for the
  number; don't rely on the exit code alone if you want CI to block on it.
- **1** — a real failure: files were in scope and *none* of them could be
  graded at all (`COULD-NOT-GRADE:`, e.g. every candidate's baseline suite was
  already broken or flaky), or enumeration failed. Per-file goal derivation
  that runs and comes back empty for every file lands here too — the scan
  happened, it just graded nothing.
- **2** — the run never started: a usage error (bad flags), or the goal deriver
  could not be CONSTRUCTED at all — the usual cause being no `model-key` (and no
  `goals` file) to derive goals with. Distinct from exit 1: nothing was
  attempted, so nothing is being reported about your code.
