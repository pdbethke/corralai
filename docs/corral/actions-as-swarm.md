# Actions as the swarm, MotherDuck (or any DuckDB) as the seal

The GitHub Action (`docs/corral/github-action.md`) already runs one audit per
pull request, scoped to the files that PR touched. GitHub already runs
twenty such jobs at once, one per open PR. `certify --repo`'s `--push`
already appends every audited (and rejected) file's verdict, as a row, to a
DuckDB the operator owns. Put those three existing facts together and the
swarm already exists: **many narrow jobs, each a few files, all pushed to
one growing warehouse.** This page is that workflow, written down, plus what
reads it back.

Nothing here is new infrastructure. It's a `pull_request` trigger, a
`workflow_dispatch` for the files no PR touches, and `corral seal` (or the
`corral_seal` view in a MotherDuck share) as the one place to ask "what does
the repo look like right now."

## The per-PR workflow

```yaml
name: corral audit
on:
  pull_request:
  workflow_dispatch: {}   # also used for the seeding run, below

jobs:
  corral:
    runs-on: ubuntu-latest
    permissions:
      id-token: write        # required by `attest: true`
      attestations: write
      contents: read
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0      # required — see github-action.md's "fetch-depth: 0 is required"
      - uses: pdbethke/corralai@v0.8.3
        with:
          test-command: "python -m pytest -q"
          # diff-base left empty: on a pull_request event the action already
          # falls back to origin/$GITHUB_BASE_REF (the PR's own base) — see
          # github-action.md's Inputs table.
          push: "md:corral"
          push-source: "false"    # default; only the numbers leave the runner
          motherduck-token: ${{ secrets.MOTHERDUCK_TOKEN }}
          attest: "true"
          derive-model: gemini-3.6-flash
          writer-model: gemini-3.6-flash
          mutant-model: gemini-3.6-flash
          critic-model: "off"
          gemini-key: ${{ secrets.GEMINI_API_KEY }}
```

Every input named above is a real `action.yml` input, spelled exactly as
declared there: `test-command`, `diff-base`, `push`, `push-source`,
`motherduck-token`, `attest`, `derive-model`, `writer-model`, `mutant-model`,
`critic-model`, `gemini-key`. `permissions:` is required for `attest: true` —
see `github-action.md`'s attestation section; it's not enforced by the
Action itself, only by GitHub refusing the `actions/attest` step without it.

`push: "md:corral"` targets a MotherDuck database named `corral` (the
`md:<db>` form `action.yml`'s `push` input documents — `<db>` is a database
name, not a table; the tables inside it are `corral_audits`,
`corral_scans`, `corral_mutants`, `corral_model_calls`, `corral_events`, one
transaction per push). `motherduck-token` is exported as `motherduck_token`,
which is what MotherDuck's own DuckDB extension reads. A plain local path
(`push: "/data/corral.duckdb"` on a self-hosted runner with a persistent
volume, say) needs no token at all.

**Read the "Who pays" section of `github-action.md` before turning this on
for a public repo.** GitHub already withholds secrets from fork PRs, so a
fork's audit — and its push — simply doesn't run; don't reach for
`pull_request_target` to "fix" that, for the reason that section explains.

## The seeding run

A `pull_request` trigger only ever audits files a PR touched — most of a
repo's history is files nobody has opened a PR against recently. A
`workflow_dispatch` (or a scheduled `on: schedule` cron, if the run's cost is
acceptable on a recurring basis) with an empty `diff-base` and a `top` bound
covers the rest:

```yaml
  - uses: pdbethke/corralai@v0.8.3
    with:
      test-command: "python -m pytest -q"
      diff-base: ""        # whole-repo scope — see "why scoped by default"
      top: "25"             # bound what one run can cost
      push: "md:corral"
      motherduck-token: ${{ secrets.MOTHERDUCK_TOKEN }}
      attest: "true"
      derive-model: gemini-3.6-flash
      writer-model: gemini-3.6-flash
      mutant-model: gemini-3.6-flash
      critic-model: "off"
      gemini-key: ${{ secrets.GEMINI_API_KEY }}
```

Run this by hand (`workflow_dispatch`) the first time, before deciding
whether a recurring schedule is worth its cost on your repo's suite — see
`docs/design/cost-model.md`'s cost expression before scheduling anything
that runs unattended.

## Concurrency needs nothing extra

Twenty PRs open at once means twenty jobs, each on its own checkout, each
producing its own scan. Nothing coordinates them, and nothing needs to:

- Every warehouse row's key is `(repo, run_url, scan_id)`
  (`internal/auditpush/bundle.go`) — `run_url` comes from the job's own
  `GITHUB_SERVER_URL`/`GITHUB_REPOSITORY`/`GITHUB_RUN_ID`
  (`cmd/corral/certify_repo_bundle.go`'s `githubRunURL`), so two jobs never
  collide on the same key even if they audit the same file the same minute.

  **Join on `scan_uid`.** Since schema_version 3 every row on all five grains
  carries it: one globally unique column, derived per push from the scan's own
  identity (repo, run URL, host, corral version, commit, substrate, ledger scan
  id, and the push timestamp — length-prefixed, then hashed). It is derived
  rather than random so you can recompute it from the row you are holding and
  check it. Rows written before schema_version 3 have the column and NULL in
  it, which is the honest value: the identity was never recorded and cannot be
  reconstructed.

  Everything below is why that column exists, and still applies to any row
  predating it.

  **`scan_id` ALONE IS NOT A KEY, and a query that treats it as one will be
  wrong without erroring.** It is a per-ledger sequence: every local
  `--record` ledger starts again at 1, so the same small integers recur across
  every ledger that has ever pushed to your warehouse. Joining
  `corral_mutants` to `corral_scans` on `scan_id` alone — the obvious thing to
  write — silently unions unrelated scans. Observed: a two-file scan that
  pushed 76 mutants reported 170, having absorbed another ledger's scan 1.
  **Join on `(repo, run_url, scan_id)`.**

  One case the composite key does not cover, and it is worth knowing before
  you point a dashboard at this: `run_url` is EMPTY for a local run, because
  those three environment variables only exist inside Actions. So for local
  runs the key degenerates to `(repo, scan_id)`, and two different local
  ledgers that both audited the same repository can collide for real. In CI —
  the case this page is about — `run_url` is always present and always
  distinct, so the swarm was never affected. `scan_uid` closes it for
  everyone else, and needs no `--record` to exist.

  `corral_seal` is immune either way: it partitions on `(repo, path)` ordered
  by `ts` and never reads `scan_id` (`internal/auditpush/seal.go`).
- The push is **append-only**, one transaction per job
  (`internal/auditpush.PushBundle`). Nothing reads-then-writes across jobs,
  so there is no lost-update race to design around.
- DuckDB's own single-writer lock still applies to a **local file** target
  (not MotherDuck, which serializes writes server-side) — the same caveat
  `--record`'s local ledger already carries in the README. Twenty jobs
  pushing to one *local* DuckDB path at once will serialize or fail-open on
  the lock; twenty jobs pushing to `md:<db>` do not have this problem.

So: many writers, one grain each, no shared mutable state between them. The
swarm is exactly as wide as GitHub's own concurrent-job limit lets it be.

## `corral seal` — the reader

`corral seal` (CLI) or the `corral_seal` view (any DuckDB attached to the
same warehouse, including a MotherDuck share) is the single surface for
"what does this repo look like right now" — the union of the still-valid
verdicts across every job that has ever pushed, not any one job's snapshot:

```bash
corral seal --db md:corral --repo .
```

It reads `corral_seal` — the latest kill-rate-bearing row per `(repo, path)`
(`internal/auditpush/seal.go`), creating the view if a writer never has —
and, given `--repo`, judges each of the repo's top-N files **live** (bytes
unchanged since the audit), **stale** (changed since), or **never audited**.
Read-only, always: it never writes a row, so running it costs nothing and
touches nothing.

Two queries against `corral_seal` for a reader without the CLI (e.g. inside
a MotherDuck share's own SQL console) are in `docs/design/cost-model.md`'s
residuals section: minutes-per-file by runner size, and kill-rate trend per
path.

## Said plainly: wide, not fast

**Each job is 15–35 minutes per touched file on a 4-vCPU runner.** Nothing
about running this per PR makes any single audit faster — it is still
*mutants × that file's suite runtime*, per file, exactly as
`docs/design/cost-model.md` describes. What changes is that twenty PRs
running twenty jobs in parallel is twenty files audited in the time one
would have taken serially. The swarm is horizontal: more runners covering
more files at once, not any one file's audit getting quicker. A larger
GitHub-hosted runner (an organization-plan feature, not available to a
personal account) buys a faster *single* file by widening its tree count
(`docs/design/cost-model.md`'s runner-size table) — that's the other lever,
and it's the calling workflow's `runs-on:`, not anything this Action decides
for you.
