<!-- SPDX-License-Identifier: Elastic-2.0 -->

# The cost model: what an audit spends, quoted from the ledger

**Status:** the table below is GENERATED — never hand-edit it. It comes from
`docs/design/fixtures/cost-model-flask.json` and `cost-model-requests.json`
(two real recorded scans, 2026-08-30 — each the slimmed output of `corral
scans show <id> --timing --json` against the ledger those runs wrote) via
`scripts/gen-cost-table.sh`, checked in CI by `scripts/gen-cost-table.sh
--check` alongside `scripts/gen-cli-docs.sh --check`. Run
`scripts/gen-cost-table.sh` and commit the result after editing a fixture;
editing the table's Markdown by hand will be overwritten and will fail the
check either way.

## The cost expression

Scoring runs the audited file's test command once per mutant, so an audit
costs, per file:

```
O(mutants × the audited file's suite runtime)
```

(`internal/adequacy/score.go`'s `WithConcurrency` doc: "cost is
O(mutants x suite runtime) — measured at 1.46s/suite for pallets/flask but
77s for psf/requests, where the suite is ~96% of a file's audit.") That
multiplier — not the size of the diff, not the size of the file — is what
decides whether an audit takes two minutes or fourteen. Test selection
(`docs/design/test-selection.md`) narrows *which* tests run per mutant; it
does not change that the multiplier is still mutants × runtime.

## Measured: the two reference files

Both scans below ran the real herd (`gemini-3.6-flash` for every seat,
`--critic-model off`) against a replayed, fixed mutant set
(`certify --repo --mutants`, so the two scans' numbers are comparable to
each other rather than confounded by run-to-run mutant-generator variance)
on a 24-core box (`--swarm` budget/4 ⇒ N=6 private trees). Recorded 2026-08-30.
Cite scan id and date, never a bare number: `corral scans show 1 --timing`
and `corral scans show 2 --timing` against the ledger these runs wrote
reproduce the `time:`/`cost:` lines below exactly.

<!-- cost-table:start -->
| Phase | `src/flask/cli.py` (scan 1, pallets/flask, 2026-08-30, N=6 trees) | `src/requests/adapters.py` (scan 2, psf/requests, 2026-08-30, N=6 trees) |
|---|---|---|
| selection | 6s | 1m21s |
| generation | — | — |
| pool | 3s | 1m05s |
| dev pass (mutants, median, max) | 12s (40 mutants, median 1s, max 2s) | 11m25s (39 mutants, median 1m03s, max 5m00s) |
| authored | 2m00s | 1m49s |
| critic | — | — |
| **total** | 2m16s | 14m21s |
| cost | 0.5M tokens in / 1.5k out across 2 calls — test-writer 0.5M/1.5k (2 calls) | 0.1M tokens in / 1.1k out across 1 call — test-writer 0.1M/1.1k (1 call) |
| kill rate | 0.40 (24 survivors, 5 proven missed) | 0.59 (16 survivors, 12 proven missed) |
<!-- cost-table:end -->

`generation` and `critic` both read `—`: these two runs replayed a recorded
mutant set (mutant-generator made no billed call this run, hence no row for
it in the `cost` line above despite `ModelsByRole` naming it) and ran with
`--critic-model off`. `—` is the honest rendering for "did not run", never
`0s` — see `cmd/corral/timing_line.go`'s `unmeasured` doc, which this table's
generator (`scripts/gen-cost-table.py`) reproduces byte-for-byte.

Both the `time:` row and the `cost` row above are generated straight from
`corral scans show <id> --timing --json`'s own output: the JSON carries
`selection_ms`, the per-file `*Millis` columns (`scanstore.File`), and the
full `model_calls` array (`role`, `model`, `calls`, `input_tokens`,
`output_tokens`, `wall_ms`) in one shape. `scripts/gen-cost-table.py`'s
header documents the field mapping in full. Nothing on this page is
hand-transcribed.

## What the first cost line found

The test-writer prompt sends the **whole mutated file**, not a hunk, once
per survivor it is asked to kill — `internal/advpool/roles.go:155`:

```go
fmt.Fprintf(&b, "\n--- SURVIVOR %s ---\n%s\n", m.ID, m.Code)
```

where `m.Code` (`internal/adequacy.Mutant.Code`) is the complete mutated
source file. `pallets/flask`'s `src/flask/cli.py` is 37,184 bytes (~36 KB) and
the flask scan above had 24 survivors: at roughly 3.5 bytes/token for source
code, one test-writer call that must see every survivor at once is
`24 × 36 KB ≈ 864 KB ≈ 250k tokens` of survivor payload alone, before the
rest of the prompt. This is a **measured finding** about the shape of the
prompt as it stands today — not a projection — and it is the single largest
line item the cost table's `cost` row does not yet break out per survivor.
The fix (sending each survivor as a small hunk around its mutated line
rather than the whole file) is a separate design and is not described
further here.

## Runner size and the tree count

`certify --repo`'s `--swarm` flag sizes the private trees that score one
file's mutants concurrently at `budget / 4` (minimum 1) — `cmd/corral/
certify_repo.go`'s `resolveSwarm`/`treeBudget`, budget defaulting to
`runtime.NumCPU()` when `--swarm` is unset. That arithmetic, not a
measurement, is what the vCPU column below derives from:

| Runner | vCPUs | Trees (`budget/4`, min 1) | Total, flask (measured) | Total, requests (measured) |
|---|---|---|---|---|
| GitHub-hosted `ubuntu-latest` | 4 | 1 | — | — |
| GitHub-hosted "large", 16-core¹ | 16 | 4 | — | — |
| GitHub-hosted "large", 32-core¹ | 32 | 8 | — | — |
| the box these two scans ran on | 24 | 6 | 2m16s (scan 1) | 14m21s (scan 2) |

¹ GitHub's larger hosted runners (16-core and up) are a GitHub Team/Enterprise
organization-plan feature — not available on a free or Pro personal account.
The Action itself (`action.yml`) does not select a runner size; that is the
calling workflow's `runs-on:`.

Every cell above that is not a real recorded scan reads `—`, on purpose: the
`budget/4` arithmetic is code, checkable without spending anything, but the
wall clock at a given vCPU count is not — running the same file's audit on a
4-vCPU `ubuntu-latest` runner or on a 16- or 32-core organization runner
would take longer or shorter in proportion to how many mutants a single tree
processes serially, and nobody has recorded that run yet. An estimate here
would look exactly like a measurement to a reader who didn't check the
fixture; the em dash is the honest alternative.

## Known residuals

These are stated once here, in full, rather than scattered across the
warehouse design doc and this page disagreeing about them:

- **The validity key is the audited file's hash only.** `parent_sha256` says
  the *source* is unchanged; a change to a test file, a fixture, or a
  dependency can make a "live" verdict wrong without moving it. The
  selection digest in the local cache key catches a test-set change locally;
  the pushed warehouse row does not carry it, so `corral seal` can only mark
  a row "aging" when it holds a checkout to compare the file's *own* last
  change against.
- **`killed_by` is per-language and best-effort.** It requires BOTH a
  `lang.FailureParser` for the language (`internal/lang/lang.go`) — shipped
  for Go and Python, deliberately unimplemented for Ruby/JS/TS, where a
  wrong guess is worse than none — AND a jail that can hand its run output
  back (`adequacy.DetailedJail`). Both substrates satisfy the second half
  today: the bwrap jail and the workspace substrate both implement
  `DetailedJail` (`internal/adequacy/jail_detailed_test.go`'s
  `TestJailImplementsDetailedJail` pins this — it was NOT always true; the
  bwrap jail used to record NULL here for every mutant it killed). A suite
  whose reporter is configured away from its default summary (pytest
  `-q -q`, `-p no:terminal`) still yields NULL, disclosed as NULL, never
  guessed.
- **Per-role usage cannot split a shared batched backend.** `scan_model_calls`
  attributes a call to the role that made it; a backend that batches calls
  across roles cannot be un-batched after the fact. None does today.
- **`--push-source` sends mutant code nowhere — only the authored test and
  the verdict JSON.** On, it sends the pool's authored test source and the
  full verdict JSON to the warehouse; off (the default, for both the CLI and
  the Action's `push-source` input), pushed rows carry numbers, hashes,
  reasons, and model names, and no source leaves the box. Mutant code is
  **never** carried by either setting — corral keeps no mutant source at
  rest, so `corral_mutants.code` is NULL regardless. The scan row records
  which setting was used, so the custody question is answerable from the
  table.
- **The public cross-repo dataset is out of scope here.** A Tier-1 aggregate
  across public repos' pushed rows needs a corral-owned account and a policy
  page; every row this design writes is shaped so that dataset can be a
  union later, but building it is not part of this work.
- **A front end is out of scope.** The surface for now is the MotherDuck UI
  over a share carrying `corral_seal` — no corralai.dev dashboard exists or
  is designed here. Two queries an operator can run against that share
  today:

  ```sql
  -- minutes per file, by runner size (once corral_audits carries a
  -- runner-vCPU column and enough rows to bucket by it)
  SELECT trees, path, total_ms / 60000.0 AS minutes
  FROM corral_audits
  WHERE total_ms IS NOT NULL
  ORDER BY trees, path;

  -- kill-rate trend per path, latest first
  SELECT path, ts, kill_rate
  FROM corral_audits
  WHERE kill_rate IS NOT NULL
  ORDER BY path, ts DESC;
  ```

  A corralai.dev dashboard rendering these same views is a later, separate
  design — not started here.

## See also

- [`docs/corral/actions-as-swarm.md`](../corral/actions-as-swarm.md) — what
  running this per PR, at scale, looks like: many narrow jobs, one growing
  ledger, `corral seal` as the reader.
- [`docs/design/test-selection.md`](test-selection.md) — the other half of
  the multiplier: which tests grade a mutant.
- [`docs/corral/github-action.md`](../corral/github-action.md) — the
  `push`/`push-source`/`motherduck-token`/`attest` inputs this page's numbers
  come from.
