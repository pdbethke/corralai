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

`corral` itself is not installed by this action — install it in a prior step,
pinned to whatever version you choose. The action assumes it's on `PATH`.

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
| `test-command` | yes | — | The command that runs your tests, exactly as your CI runs it (e.g. `go test ./...`). |
| `diff-base` | no | `""` (falls back to the PR's base ref) | Audit only files changed against this ref. Left empty on a `pull_request` event, the action falls back to `origin/$GITHUB_BASE_REF` (the PR's own base). On any other event (e.g. a push to `main`), there is no base ref to fall back to, so an empty `diff-base` means a whole-repo audit. |
| `goals` | no | `""` | Optional JSON file of per-file goals. Omitted means goals are derived per file by a model. |
| `model-key` | no | `""` | Provider API key for goal derivation, wired into the run as `ANTHROPIC_API_KEY` — the same environment variable corral's default model backend reads everywhere else (`internal/creds`). Required unless `goals` is supplied. Pass it as `${{ secrets.ANTHROPIC_API_KEY }}`; never write a key inline in the workflow. |

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
