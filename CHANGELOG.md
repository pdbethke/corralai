<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Changelog

Notable changes to corral. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions are [semantic](https://semver.org/), and pre-1.0 means the CLI surface can
still move between minor versions.

Entries describe what changed for someone *using* the tool. For the full commit
history of any release, `git log v0.3.4..v0.3.5`.

## [Unreleased] — toward 1.0.0-rc.2

- **A ledger entry has a kind, and two new kinds: retract and checkpoint.**
  `corral ledger retract <dir> <hash> --reason "…"` appends an entry that
  retracts an earlier one: the retracted scan stays in the chain (deleting
  it would break the next link, which is the chain doing its job) and stops
  being the record — the DuckDB view, `--prior`, the verdict cache and
  `corral scans` all skip it, `scans list` marks it `RETRACTED: <reason>`,
  and `verify --ledger` names the retraction. `corral ledger checkpoint
  <dir>` prunes: one genesis entry naming the replaced head, entry count
  and date stands in for everything before it, and the verifier reports
  "chain begins at a checkpoint; N earlier entries not present" rather
  than a chain that was always that short. A checkpoint anywhere but first,
  or a retraction of an entry the chain never held, is a problem by name.
  Scan entries carry no kind field, so every existing ledger hashes exactly
  as it did.
- **`corral` with no subcommand is a pointer, not a server.** The
  coordination daemon is `corral-wrangler serve`; a bare `corral` exits 2
  and says so. corralai's own unit was moved first.
- **A harness file outside any test root is test support.** A repo-root
  `conftest.py`, a `src/jest.setup.js`, a `spec_helper.rb` beside the
  code: every file a language plugin names as something its runner reads
  before any test is now excluded as `test-support`, not reported as a
  source file with no paired test — and can no longer be promoted to an
  audit subject by coverage evidence.
- **corral on an internal forge.** `docs/corral/internal-forge.md` (and the
  site page beside the Action's): the same audit and the same ledger
  branch on GitLab, Gitea, Bitbucket or Gerrit, with what stays inside the
  perimeter and what does not stated exactly, and where the chain's trust
  sits without keyless attestation.

## [v1.0.0-rc.1] — 2026-09-06

The release candidate. The record is a signed, hash-linked ledger in the
audited repository; the exam is sized to the file, fitted to the clock, and
disclosed with its confidence; a rate too loose to be a grade no longer
certifies; and five rounds of strangers tried to break it first (#224–#236).

- **BREAKING — a verdict cannot be CERTIFIED on an exam too small to
  certify.** Beside every kill rate corral now prints its 95% interval
  (Wilson, over the mutants graded). A rate that clears the threshold on an
  interval wider than 0.35 is routed to `needs-review`, marked
  `[INDICATIVE — …]` with the band, and signed as such: five of five killed
  is 0.57–1.00, and no reading of that is "adequate". Previously CERTIFIED
  verdicts on small exams do not certify under this rule; the rate itself is
  unchanged and still printed.
- **The mutant budget.** A file is planted with about one fault per decision
  point (floor 5, ceiling 40 — the old default exam) instead of five per
  generator seat regardless of what the seat held; under `--substrate
  workspace` the budget is fitted to `--timeout` from the probe's *measured*
  cost of one round of mutants. `--n-mutants` keeps its per-seat meaning and
  is never fitted; the header warns when it cannot fit. Every verdict, ledger
  row and signed statement names the budget and its rule.
- **The exam's reach.** Each verdict says how many of the file's symbols and
  decision points a fault actually landed on; the extractors keep every
  decision point's line span, and mutant spans are recorded per row (they
  were not, before). Deliberately two terms beside the interval, never one
  blended index.
- **The authored pass runs the authored test alone** — baseline, canary and
  positive control too, not only the survivor run. On psf/requests a hub
  file's proof phase went from 26 minutes and a timeout to 35 seconds.
- **The per-mutant cap follows the command actually run**, at 3× its own
  compliant duration instead of 8× the file's, so a hang costs seconds on a
  two-test command instead of the five-minute ceiling.
- **The writer pair is recorded whether or not its coefficient is** — what
  each writer proved of the same survivors, and the union and overlap of
  their misses, on the report line, both ledgers and the statement.
- **The ledger is the record, written by default.** A `--repo` scan writes
  one signed, hash-linked, gzipped JSON entry into `.corral/ledger/` under
  the repository (`--ledger <dir>`, `--no-ledger`), and reads the entries
  already there as its prior. `corral verify --ledger <dir>` walks the
  chain; `corral ledger append` re-links an entry to a moved head (the
  Action's retry loop and a laptop behind its branch). `--push <dir>/`
  writes the same entries anywhere; `seal`/`models rank`/`verify --db`
  accept a directory. DuckDB is the view, never the store.
- **BREAKING — the local DuckDB scan ledger is gone; the ledger
  directory is the only record.** `certify --repo` writes no
  `scans`/`scan_files`/`scan_mutants`/… tables anywhere, and nothing reads
  them: the entry it writes by default carries every grain, plus the
  scan-level facts the DuckDB row held (`top`, `all_candidates`,
  `total_files`, `engine_version`, `model_set`, `preflight_ran`,
  `finished_at`) and each file's `computed_at` and `mutants_from`, as new
  warehouse columns (added additively on the next push). `--record` and
  `--record-db` are removed; **`--cache-db`** names the one local file
  corral keeps — a *cache* of derived goals and instrumented test
  selections, default `~/.claude/corralai_cache.duckdb`
  (`$CORRALAI_CACHE_DB`). The verdict cache reads the ledger directory
  (`--no-ledger` also disables it); the entry therefore carries its
  verdict JSON and authored test — it lives beside the code it quotes —
  while `--push` to a warehouse still withholds source unless
  `--push-source`. `corral scans list|show` read the directory
  (`--ledger <dir>`, default `.corral/ledger`; an id is the entry's chain
  position, oldest = 1); `corral scans push` is gone (`--push` at scan
  time, `corral ledger append` between directories); `seal` and `ui`
  default to the directory. `selection_reused_from` (a scan id) is now
  `selection_reused` (a boolean) on the entry, the warehouse and `scans
  show --json --timing`. The statement's rows hash is **version 3**:
  source columns and `source_pushed` are never in it, so one statement
  verifies against both the entry (source carried) and a warehouse push
  (withheld) — v2 and v1 statements still verify the way they were signed.
- **`--prior`: the next run plants what the last one didn't.** Hand a run
  a `--record-mutants` document, a ledger directory, or a directory of
  either, and the generator is told every edit earlier runs tried on a file
  whose bytes match exactly — place, shape, hunk, outcome — and asked for
  different faults. A primed exam is a different exam: `priorsApplied` and
  the prior's digest ride on the verdict, both ledgers and the statement,
  and the digest is in the cache key. On the Action, the `ledger` input plus
  the `corral/ledger` branch recipe is where the prior lives.
- **Every mutant row carries its shape and its generator.** The kind of
  fault is read from the hunk itself (never the model's label); "which
  shapes does this model plant, which does this suite let through" is a
  query over `corral_mutants`.
- **`--max-tokens`, a cap on money.** One cap across every seat of a run;
  checked before each call, charged after; a refused call never reaches the
  provider. Disclosed on the cost line. Ollama seats now report their tokens.
- **`verify --db` survives columns added after the push.** The rows hash is
  now over a sparse canonical form (version 2, recorded on the statement);
  a v1 statement that mismatches is told why in words.
- **A rate too loose to be a grade does not certify** — see the BREAKING
  entry above. The `--local` verdict prints the interval, the budget, the
  reach, and `INDICATIVE:` with the reason.
- **`corral-wrangler register|heartbeat|claim|release|done|who|list`** — the
  claim broker without the server: the verbs open the daemon's own
  coordination store as a local file, the OS user is the principal, and a
  refused claim exits 1 naming the holder so `claim … && edit` stops first.
- **The brain is its own binary: `corral-wrangler`.** The coordination
  daemon moved out of `cmd/corral/main.go` whole (`internal/wranglerd`);
  `corral` with no subcommand still starts the same server for this one
  release and says where it went, then becomes a pointer. The audit CLI and
  the daemon no longer share a main. Deploy builds both; the brain image's
  entrypoint is `corral-wrangler`.
- `--top` is one bound over every door: evidence widening competes under it
  instead of appending past it. Files under a language's test tree
  (`tests/conftest.py`, `spec/support/`) are `test-support`, never subjects.
- The README is the first-run document; the brain is documented as optional
  and not read by any audit (`docs/corral/brain.md`).

## [v0.8.3] — 2026-09-01

Faster scoring, honest timing, and the gaps corral proved in its own code.

- **A killed mutant needs one failing test, not a whole suite.** Scoring now
  passes the runner's stop-at-first-failure flag on mutant runs — never on
  the baseline, because a green baseline must execute everything or corral
  would certify a suite it never fully ran. Where selection evidence exists,
  the covering test most likely to kill runs first. Byte-identical duplicate
  mutants are graded once and answered twice, with the count disclosed and
  the denominator deliberately unchanged. `--no-fail-fast` opts out.
  A hermetic verdict-identity test grades a recorded set down both paths and
  asserts kill rate, survivors, and every `killed_by` agree — and that the
  fast path really ran fewer tests, so a no-op cannot pass it.
- **A phase that ran no longer reports as one that did not.** corral once
  printed `total 10m32s` for a run that took 100 minutes: the per-survivor
  writer was still open when the deadline fired, and an open phase was left
  at zero — which renders as `—`, the marking reserved for *did not run*.
  Open phases are now credited with the time they actually spent, any
  residual prints as `unattributed` rather than vanishing, and the writer's
  attempts-per-survivor spread is reported so the next optimisation knows
  whether its cost is retries or slow single attempts.
- **corral audited its own signing code and we kept the tests it wrote.**
  That audit returned kill rate 0.55 — 43 of 78 planted faults killed, 35
  survivors, 30 proven catchable. Of the 51 test functions the writer
  produced, 18 survived triage (the rest were duplicates, implementation
  mirrors, already-covered cases, or did not compile). Each kept test was
  checked by breaking the line it guards and watching it fail.
  `internal/certify` goes from 18 tests to 36.

## [v0.8.2] — 2026-09-01

Declare your models once.

- **A model registry.** `.corral/models.json` (or `CORRALAI_MODELS_FILE` /
  inline `CORRALAI_MODELS`) declares the models a project may use, and seats
  name them by alias: `--writer-model writer`. Provider is a field rather
  than a substring of a model name, so the decorrelation disclosure can say
  plainly when writer and critic share a vendor. Local models are
  first-class — an entry may carry its own endpoint, so a generator can run
  on your own hardware while a hosted model writes.
- **`"strict": true` makes a typo cost two seconds.** A seat naming
  something undeclared is refused before the run starts, listing the aliases
  you declared. Without it a mistyped name falls through and dies at the
  seat — which is how a model that has never existed burned two hours of CI
  the night before this release.
- **Aliases are never authoritative.** The verdict, ledger, statement and
  cache keys all record the concrete model an alias resolved to. Renaming an
  alias cannot move a cache key or blur a record.
- **`corral models rank`** ranks models per seat from corral's own recorded
  outcomes — proven gaps per survivor attempted for the writer, missed-fault
  yield for the generator, adjudicated precision for the critic — and
  refuses to recommend on thin evidence. It prints a ranking; it never
  staffs a seat. The goal-deriver is reported unscored, because no honest
  signal for it exists yet.
- `corral scans push` sends scans the ledger already holds to a warehouse,
  so recording first and deciding on a warehouse later no longer means
  re-running (and paying for) everything.
- The GitHub Action gains Marketplace branding.
- Nothing above changes an existing invocation: concrete model names still
  work everywhere, and a seat you do not name still refuses the run.

## [v0.8.1] — 2026-08-31

The release that made a failed measurement impossible to mistake for a passing
one. Coverage evidence — already collected once per scan — now decides which
files a `--repo` scan may audit: a file with covering tests is auditable even
when no filename pairs with it, and a file with no coverage is named honestly
in one of three states rather than lumped under "no paired test": `uncovered —
no test executes this file`, `imported at load time — no test exercises it
directly`, and `no executable code`. Absence of evidence is never treated as
evidence of absence, and only library code counts toward the headline.

Proving that feature on a third-party repo exposed five defects in the
selection layer, every one an instance of a measurement failing or being
misread and then reported as fact:

- Python instrumentation requires **pytest-cov**, not merely coverage.py.
  Without it pytest exits 4 and prints nothing — and that empty output was
  recorded as a *successful* run, so per-test selection was silently inert on
  stock Python repos while corral quietly graded by the whole suite. An
  instrumented run that printed nothing is now a failure that names its cause:
  exit code, stderr tail, and the missing plugin.
- That empty evidence was cached, making the blindness sticky across runs.
  Nothing empty is cached now, and an empty cached row is treated as a miss, so
  existing ledgers heal themselves.
- A src-layout package installed with `pip install -e .` is measured outside
  the repo root, so coverage dropped every source file from the report and
  corral called a thoroughly-tested core module UNCOVERED. Instrumentation is
  now scoped to the scan's own derived source roots.
- A package `__init__.py` was called untested though every test imports it, and
  an empty `__init__.py` was too.
- `corral scans` and `corral seal` repeated the same false claim from the
  ledger; they now distinguish the states, with an additive `import_only`
  column.

`corral seal` gains `uncovered` as a state distinct from never-audited. Every
language but Python is untouched — only Python implements a test selector.

## [v0.8.0] — 2026-08-31

The stranger's path, the public record, and the sixth language.

- **A cold reader can get a graded verdict on the first attempt.** A rehearsal
  that followed the README verbatim took seven attempts; test pairing now
  probes its candidate list for files that exist and searches conventional test
  roots recursively, `corral doctor` probes the exact sandbox the run will use
  (so preflight and run can no longer disagree), and the README's real-repo
  command runs as printed — every seat named, because corral has no default
  models.
- **The public record.** `--attest --transparency` signs the audit statement
  into a DSSE envelope and, opt in, uploads it to Sigstore's Rekor
  transparency log — public and permanent. The new `corral verify` checks all
  three rungs: the signature, the warehouse rows-hash cross-reference, and
  public-log inclusion.
- **PHP** joins Go, Python, Ruby, JavaScript and TypeScript: proven on
  webmozart/assert under PHPUnit, CERTIFIED with 40 of 40 planted faults
  killed. A PHP-specific sandbox trap (Debian's `/usr/bin/php` symlink through
  `/etc/alternatives`) is now refused before it costs a model call.
- The recordings gallery leads with corral auditing its own signing code —
  NEEDS-REVIEW, gaps published on purpose.

## [v0.7.0] — 2026-08-27

The release that made corral's own numbers trustworthy. A mutant the compiler
rejected used to be scored as a KILL, so a suite could be credited with catching
bugs that never built — on a file with 0% coverage on every function, corral
signed a record claiming a 0.77 kill rate that was truthfully 0.00. That is
fixed, and much of what follows is the same theme: measure honestly, say what
was not measured, and never report a number the run did not earn.

### Added

- **A challenger test-writer, and the measurement it exists for.**
  `--shadow-writer-model` runs a SECOND writer against the same survivors as the
  primary, so the two seats' misses can be compared: Jaccard over survivors
  (agreement on kills is cheap; the signal is in what both models MISS) plus
  Cohen's kappa, reported separately and never blended. Off unless named,
  measurement only, and it never gates a verdict. Per-mutant outcomes are stored
  per seat, pair-or-nothing: if one seat produced no usable test, NOTHING is
  recorded rather than a zero that would read as a catastrophic blind spot.

  The first coefficient: **Jaccard 0.750 over 13 survivors** between two frontier
  models from different labs — they shared three quarters of their blind spots.
  Enforcing that two seats differ is necessary and demonstrably not sufficient.

- **`--local-endpoint <role>=<url>`** places a LOCAL seat on a specific ollama
  daemon. A daemon is pinned to a GPU by its own environment, so this is how two
  models occupy two cards at once; corral selects the daemon, never the device.
  Without it every local seat shares one `OLLAMA_URL`, one card and one VRAM
  budget. Unknown role, duplicate role, non-absolute URL, or an endpoint on a
  seat holding a cloud model are all refused rather than ignored.

- **`--record-stream <file>`** emits each run event as newline-delimited JSON as
  it happens — the same events `--record` collects into a tape at the end. A
  multi-hour audit used to produce nothing watchable until it finished; now a
  watcher (`tail -f`, or the cockpit) can follow a run in flight.

- **`certify --repo` reports what it spent.** A whole-repo scan is the mode that
  actually costs money and it reported no usage at all. Tokens, not dollars:
  prices change and differ by contract; a token count stays true.

- **Every ungradable file now explains itself** in one clause — whether it is
  corral failing, your invocation, or a file with nothing to audit. Bare codes
  made correct refusals read as crashes: on `spf13/afero`, two files in separate
  Go modules (unreachable by the test command) and two pure interface
  declarations (nothing a mutant could violate) were filed alongside real errors.

### Fixed

- **A mutant the compiler rejected is INVALID, not killed.** `passed` meant only
  "exit zero", so a build failure counted as the tests catching the bug. Kill
  rates of 0.77 and ~0.92 were truthfully 0.00. Mutants now pass a compile gate
  before grading, invalid ones are excluded from the denominator and reported
  separately as evidence about the GENERATOR rather than about your tests.

- **Reasoning models returned empty content, silently.** Qwen 3+, DeepSeek-R1 and
  gemma4 route their answer through a separate `thinking` field; when the budget
  runs out mid-reasoning the request still returns HTTP 200 with an EMPTY body.
  A seat looked incapable when it was never asked correctly — gemma4 produced
  three empty Go test files in a row and read as a model that cannot write Go.

- **`certify --repo` recorded rows that named no revision and no repository.**
  `--record` stored an empty commit and `repo='.'`, so a scan could not be joined
  to the code it graded — and repo is the key dimension in a warehouse spanning
  projects.

- **The jail dropped binary test fixtures.** Any suite reading `testdata/*.zip`
  failed its UNMUTATED baseline with `no such file or directory`; on
  `spf13/afero` 13 of 16 files were lost this way.

- **An interrupted audit now restores your tree.** On the `workspace` substrate
  the apply/restore ledger covered a failing command, a timeout and a panic — but
  a signal kills the process without running deferred functions, and Ctrl-C is
  how a human stops a long audit.

- **The goal deriver required a cloud vendor**, which made `certify --repo` the
  one mode that could not run locally, against corral's own local-first claim.

- **Per-request model timeout raised 300s → 600s.** 300s was itself a fix for a
  falsified 180s, and was falsified in turn from the other end of the hardware
  range: auditing `spf13/afero` with a 9B model on one 16GB GPU, 8 of 16 files
  died on `context deadline exceeded`. Nothing was hanging. Re-running at 900s
  graded 12 files and raised that panel's proven-missed count from 36 to 53.

- **Ruby: pair minitest's prefix form** `test/<sub>/test_foo.rb`. Repos using
  minitest's own house style paired at ZERO and were invisible to the scanner.

- **`num_ctx` is sized to the VRAM budget**, and a context overflow says what to
  do about it instead of failing opaquely.

- **Retries stop on a terminal failure** instead of reissuing a doomed run twenty
  times.

### Documentation

- **Corrected where corral writes.** `AGENTS.md` said "never point corral at a
  working repository you care about" and that `certify --local` mutates files in
  place. Both are false for every default invocation: both modes run in a bwrap
  jail, and `certify --local` has no `--substrate` flag at all. Only
  `certify --repo --substrate workspace` touches a real checkout — which the
  GitHub Action opts into deliberately, because an ephemeral runner IS the
  isolation boundary.

## [v0.6.0] — 2026-08-23

### Added

- **`--push <target>` — the cross-project view.** Appends a scan's per-file
  verdicts to a DuckDB the operator owns: a path, or `md:<db>` for MotherDuck.
  corral has no hosted tier and keeps nothing — your key, your runner, your
  warehouse — and any DuckDB works, so the target is a destination rather than
  a lock-in.
  It answers the question one pull request cannot. A single kill rate is a
  sample: the same unchanged diff has scored 0.85 and then 0.90, and in testing
  this feature one unchanged file scored 0.80 and then 0.60. Forty rows are a
  distribution, and "this file drifted from 0.9 to 0.6 over two months" is a
  claim no individual run supports.
  Two properties the schema enforces rather than documents. **Append-only**: a
  receipt that can be UPDATEd is not a receipt, and overwriting is exactly how
  a trend is lost. **The qualifiers travel with the numbers**: `proven_missed`
  of 0 means "nothing was proven" rather than "the suite is clean" whenever the
  writer failed or its test never graded, and aggregation is precisely where
  that distinction gets dropped and a zero silently becomes good news.
  Every row carries the sha256 of the signed statement it came from and the run
  URL, so a row in the warehouse traces back to an attestation a third party can
  verify — the table is evidence rather than self-report.
  Comparability metadata travels too (language, mutants planted, models by
  role, the thresholds, and the audited/candidates denominator), because a kill
  rate on a dense function and one on a small accessor are not the same
  measurement and a reader cannot otherwise tell a hard file from a weak suite.

## [v0.5.9] — 2026-08-23

### Fixed

- **The release title is read from the API rather than guessed from the
  checkout.** Two git incantations were tried — `git tag -l`, then
  `for-each-ref` added specifically to fix it — and both returned the COMMIT's
  subject on the runner. Neither errored; both produced a plausible wrong title,
  and two releases were published named after whatever had just been merged.
  Whatever actions/checkout leaves behind for a tag ref is not reliably the
  annotated object. The workflow now asks GitHub, which knows, and refuses a
  lightweight tag rather than naming a release after an unrelated commit.

## [v0.5.8] — 2026-08-23

### Fixed

- **The release title comes from the tag, not from whatever was merged last.**
  actions/checkout does not fetch annotated tag OBJECTS by default, so
  `%(contents:subject)` resolved to the tag's commit and the first
  auto-published release was named after the changelog commit that triggered
  it — a plausible-looking wrong title rather than an error. The workflow now
  fetches tag objects, reads the annotation explicitly, and refuses a tag with
  no message rather than naming a release after an unrelated commit.

## [v0.5.7] — 2026-08-23

### Added

- **The release publishes itself when a tag is pushed.** Three times in one week
  a tag was cut, the docs were updated to pin it, and no release followed — so
  the Releases page advertised an older build than the docs told people to
  install. The tag is the intent; the release now follows from it, with notes
  taken from this file's section for that exact version. A tag with no section
  fails rather than publishing a page that describes nothing.

## [v0.5.6] — 2026-08-23

### Fixed

- **Verify instructions that work on the CLI people have.** The Action's job
  summary told a reviewer to run `gh attestation verify`. That command does not
  exist in gh 2.45, which some distributions still ship — and an older CLI
  answers it with a help dump rather than an error, so the reader concludes they
  mistyped. Found by verifying corral's own first attestation on a machine with
  2.45. The summary now names the version constraint and gives the plain API
  call the command wraps, which needs no particular CLI version.

## [v0.5.5] — 2026-08-23

### Added

- **`--attest` — the verdict becomes a receipt.** A signed record existed only
  from `certify --local`, and only where the run happened; the Action runs
  `certify --repo`, which had no way to emit one. So a pull-request audit
  produced a readable summary and nothing anyone could keep.
  `--attest <file>` writes the scan as an in-toto Statement — every audited
  file's kill rate, survivors and proven gaps WITH the flags that say what a
  zero means, the thresholds it was judged against, the models in each role, and
  the denominator, so a clean result cannot be flattered by omitting how little
  was looked at. The Action publishes it through `actions/attest`, signed
  keylessly by the workflow's own OIDC identity, so the signature chains to the
  repository and workflow rather than to a key that lived on an ephemeral
  runner. Free on public repositories.
  The statement is written BEFORE the gate's exit code is honoured and the
  attest steps run under `always()`: a receipt you only keep when the verdict
  flatters you is not evidence, and the failing runs are the ones a reviewer
  most needs.

### Fixed

- **A changed TEST puts its source in scope** (also in v0.5.3): a pull request
  that deletes assertions touches no source file, so the gate reported
  `NOTHING IN SCOPE` and passed green on the exact change it exists to catch.

## [v0.5.4] — 2026-08-22

### Added

- **`--max-proven-missed`** (and a `max-proven-missed` Action input) — the merge
  gate that does not flap. `--min-kill-rate` is the obvious one and the wrong
  default: a kill rate is a proportion of FRESHLY GENERATED mutants, so it moves
  between runs on unchanged code. A demonstration pull request that deleted
  every assertion pinning a library's central guarantee scored 0.85 and then
  0.90 on the identical diff — the higher number on the more weakened suite —
  and a 0.8 bar passed it both times. A proven-missed gap is a survivor the herd
  then KILLED with a test it wrote and ran: a demonstrated bug, not a
  proportion. `0` means any demonstrated gap fails the build.
  It fails CLOSED. With survivors present and no test that graded them,
  `proven_missed` reads 0 because nothing was proven, not because the suite is
  clean; those files fail and report as `PROVEN-GAP UNMEASURED` rather than
  passing on a question nobody answered.

## [v0.5.3] — 2026-08-22

### Fixed

- **A changed TEST puts its source in scope.** Diff scoping matched a candidate
  only on its source path, so a pull request that deletes assertions — touching
  no source file — was scoped to nothing: `NOTHING IN SCOPE: the diff touched no
  candidate; no audit was needed`, and the check passed green while the suite it
  guarded had just been gutted. Weakening a suite is the pure form of "tests
  that pass and defend nothing", the exact change this gate exists to catch, and
  it was the one change that could not reach it. The pairing was already
  resolved (`reposcan.Candidate.TestPath`, from `--tests` or the language
  plugin's convention), so scoping on either side of the pair needs no new
  configuration.

## [v0.5.2] — 2026-08-22

### Fixed

- **A too-narrow test command is refused before the audit, not after.** corral
  writes its killing test beside your own, and if your command names a single
  test file rather than a directory, nothing collects it. That was detected —
  but only *after* the test-writer had authored a test, so you paid for a whole
  audit to be told `proven_missed: 0` and "widen the command and re-run". The
  same check now runs before any model call: one jail execution, no inference,
  and the refusal names the exact file it would have written. 1.2 seconds
  instead of a wasted run. The assumption underneath was that the dev test's
  directory is "collected by construction" — true for a discovery-based runner,
  false for `node tests/x.test.js` or `pytest tests/x_test.py`, which is an
  ordinary way to invoke a suite.
- **A cross-vendor run no longer routes a seat to Ollama.** `baseVendor()` read
  an unset `MODEL_BACKEND` as "anthropic" while `FromEnv()` read it as ollama.
  They disagreed in exactly one reachable case — a MIXED-vendor run, where no
  single backend can be inferred and the variable stays unset — so a Claude
  critic beside a Gemini generator matched a phantom Anthropic base, skipped
  cross-routing, and was handed to Ollama, which 404s a model it has never
  pulled. The documented cross-vendor shape could not run without pinning
  `MODEL_BACKEND` by hand. Unset is now told apart from a deliberately pinned
  gateway: an unpinned run routes every cloud-named seat by name, while an
  explicit `openrouter`/`ollama` keeps its hands-off guarantee.
- **A disabled critic reports as disabled.** With `--critic-model off` the
  verdict printed "critic review: no vacuous tests flagged" — a clean bill of
  health from a reviewer that never ran, because the line keyed on an empty
  findings list. It now says "not run — no test-critic was assigned". A critic
  that ran and found nothing still says so.
- **Go 1.26.6.** Seven stdlib advisories reachable from corral's own call paths
  (`net/url`, `net/http`, `crypto/tls`, `encoding/asn1`, `encoding/xml`,
  `html/template`), all fixed in that patch release. They went unnoticed because
  `scripts/check-security.sh` runs `govulncheck` only when the binary is present
  and CI never installed it — so the security gate printed "OK: all security
  invariants hold" having checked formatting and nothing else. CI now installs
  it, and the gate means what it says.

### Added

- **The Responses API.** OpenAI serves its Codex models — `gpt-5-codex`,
  `gpt-5.1-codex`, `gpt-5.1-codex-mini`, `gpt-5.3-codex` — on `/v1/responses`
  only; they are not available on `/chat/completions` at all. Naming one used to
  resolve cleanly and then fail at the API boundary, telling an operator that a
  model they can see in the docs does not exist. Routing is now per model rather
  than per vendor.
- **One key is enough.** Three provider inputs and an enforced decorrelation
  rule read like corral needs an account with every vendor. It does not: the
  verdict is measured by execution and the critic is advisory. The GitHub Action
  docs now carry a single-key workflow, along with what that mode costs — corral
  prints a decorrelation warning naming the lineage that both planted the faults
  and graded the tests.

### Note

`v0.1.0` was the day-zero, builder-era release and remained the only published
release until now, so the Releases page advertised a July build while the docs
told you to pin a much later tag. This is the first release published since.

## [v0.5.1] — 2026-08-13

### Added

- **`corral demo`** — the two-minute first run. One command, no setup beyond a
  provider key: it writes a small Go package (a five-clause password rule and a
  test that checks exactly two of them, and passes) and audits it with the real
  `certify --local`. Go on purpose — you installed corral with a Go toolchain, so
  it is the one dependency you are guaranteed to have, and it normally lives under
  `/usr` where the jail can see it.
  It exists because the honest first run was not two minutes: auditing real
  third-party repositories took six attempts to produce one verdict, five lost to
  the environment. That is not a fair first impression of what the tool does.
- **Live progress.** A run used to print a few lines and then go silent for
  minutes while eight seats worked, which reads as a hang. The pool already
  emitted fine-grained beats — `--record` captured them for replay — and they were
  being written to a file instead of the terminal. They now echo as they happen,
  to stderr so nothing parsing stdout is affected. `--quiet` turns it off.

### Fixed

- **An unset `MODEL_BACKEND` is not "Claude".** The last place corral assumed a
  vendor: an unset backend was read as "the default direct-Claude path", so a run
  demanded `ANTHROPIC_API_KEY` no matter which models had been named. All-Gemini
  runs escaped it only because vendor inference happened to fire first; a local or
  unroutable herd fell straight through to it. Unset now means "infer from the
  assigned models" — which the code already did for every vendor except Anthropic,
  the one it handled by assumption instead of by evidence. When the models name no
  cloud vendor, nothing is demanded.
- The demo project is written 0750/0600 rather than 0755/0644.

## [v0.5.0] — 2026-08-13

### Added

- **The verdict cache is real.** `reposcan` has carried a content-addressed cache
  key, a `Cache` interface and consult/populate call sites since v0.3.0 — with no
  implementation, and `nil` passed in production. Every scan recomputed every file
  forever while honestly reporting `CacheHits: 0`. There is now a DuckDB-backed
  implementation, owner-scoped in SQL, and it is enabled.
  **It fails closed without exception:** an unreadable row, verdict JSON that will
  not parse, a missing store or an empty owner all resolve to a MISS. A miss costs
  money; a wrong hit signs a claim about content that was never measured.
- **The ledger records what it measures.** `scan_files` gains the verdict's own
  numbers as columns — model attribution, mutant and region counts, the coverage
  shortfall, status, and the honesty flags — plus `cache_hit`/`reused_from_scan_id`
  so an aggregate can EXCLUDE reused rows. Without that, enabling the cache would
  have made one measurement count once per scan forever.
- **`scan_mutants`** — one row per mutant per file per scan. "Which generator
  produces mutants a suite does not catch" is a question about mutants and could
  not be asked of a table whose finest row was a file. The mutant SOURCE is
  deliberately not stored: `parent_sha256` is enough to group and compare without
  putting tenant code at rest in the warehouse.
- **`suite_baseline_ms`** — the target suite's runtime, which `adequacy.Score` has
  measured on every run since the package existed and then thrown away. It is the
  single input to the audit cost model (`O(mutants × suite runtime)`), so every
  capacity estimate before this was an extrapolation.
- **Token accounting.** Every provider reports usage on every response and corral
  discarded it at the JSON boundary. `certify --local` now ends with
  `model spend: N in / M out token(s) over K model call(s)`. Tokens, not dollars —
  prices change, a token count stays true.
- **Reuse is disclosed by AGE**, not just counted: the report names the oldest
  reused verdict. A scan where most files were reused from weeks ago, presented as
  current, is the self-flattering record this tool exists to prevent.

### Fixed

- **The jail can see the passwd database.** Without `/etc/passwd` the jail's uid
  has no entry, so `getpass.getuser()` raises — and PyTorch calls it at import
  while computing its cache directory. Any Python project transitively importing
  `torch` or `transformers` died before pytest collected a test, reporting
  `COULD-NOT-GRADE` indistinguishably from a broken project. `/etc/shadow` is not
  bound and a test pins that it never will be.
- **The cache key was blind to what it keyed.** `ModelSet` and `AuditConfig` were
  hardcoded literals, so the key could not tell two model sets apart — and the
  same constant reached the ledger, meaning every row ever written recorded
  `model_set='unset'`. Both now carry real values.
- **`EngineVersion` is a deliberate `VerdictGeneration`**, hand-bumped when engine
  behavior can move a verdict. It was the release version, so a documentation-only
  release invalidated every cached verdict for every tenant.
- **Five wrong-hit paths**, each of which would have signed a claim about content
  that was never measured: mutant source reachable from a marshalled `Verdict`
  (closed by type, via `advpool.MutantRef`); the operator's own `-- <cmd>` absent
  from the key; `TestSurfaceDigest` covering one paired test file while the whole
  suite grades; Go `testdata/` goldens outside the keying surface; and a
  file-scoped scan declared on an argv that names tests beyond the selected set
  (closed across exact-path, directory and repo-root token forms).

### Known limitations

Documented in code as open wrong-hit paths, not as design: the file-scoped path
ignores the test-surface list, so weakening `tests/conftest.py` leaves the key
unmoved; a repo-root `conftest.py` with tests under `tests/` is uncovered; and
argv tokens given as absolute paths or symlinked directories do not disqualify a
file-scoped scan.

Two properties worth knowing before judging the cache on hit rate: **any test-file
change invalidates every verdict** on the whole-suite path (correct — the grading
surface really did change for every file), and **`GoalDigest` moves between runs**
because goals are LLM-derived per run, so unless `--goals` is pinned the cache is
largely inert.

## [v0.4.0] — 2026-08-13

### Changed — BREAKING

- **corral has no default models.** Every grading seat is named by the operator.
  `--writer-model` and `--mutant-model` are **required** on `certify --local` and
  `certify --repo`; `--critic-model` has no fallback (`off` still disables it);
  `--derive-model` is required on `--repo` unless `--goals` is supplied (`--goals`
  skips derivation and calls no model at all). A run with an unnamed grading seat
  is refused before any jail, store or spend, and **the refusal reports which
  provider credentials it can actually see** — the usual cause is "I have a key, I
  just don't know what corral wants from me."
- **The challenger (shadow) seat is OFF unless named.** It defaulted to a Claude
  model and was on, which quietly kept an Anthropic seat alive through an otherwise
  all-Gemini run and then failed for want of a key the operator had deliberately
  left behind.
- **The GitHub Action's `model-key-env` no longer defaults to `ANTHROPIC_API_KEY`.**
  A `model-key` with no `model-key-env` is refused rather than guessed; `writer-model`
  and `mutant-model` are required inputs.
- **An unconfigured hosted pool is DISABLED rather than cold-starting.** With no
  `CORRALAI_ADVPOOL_MODELS` and no leaderboard evidence the adversarial pool is never
  registered; a malformed `CORRALAI_ADVPOOL_MODELS` is refused instead of falling back.

Why: corral's claim is that it is model-agnostic — *across any model, local 7B to
frontier*. A binary that names one vendor's models when the operator named none was
making an exception to that claim, and it meant anyone arriving with an OpenAI,
Gemini or OpenRouter key hit a failure on their first command and had to discover
five flags to get past it. We run Claude; we do not make anyone else.

**The one rule that survives is decorrelation** — the test-critic must differ from
the test-writer. That is a *property*, not a vendor: any two distinct models from
any provider satisfy it.

**Upgrading:** add `--writer-model`, `--mutant-model` and `--critic-model` (or
`--critic-model off`) to existing invocations, and the equivalent inputs to Action
workflows. `corral doctor` checks a herd and its credentials for free before you
spend anything.

### Fixed

- **`corral doctor` had its own hardcoded Claude defaults.** With no model named it
  substituted `claude-sonnet-5` and reported a credential failure for it — telling an
  operator holding a Gemini key to go get an Anthropic one, from the command that
  exists to prevent wasted runs. It now reports the real finding, per seat.
- **`corral doctor` was missing from `corral -h`** — routed and working since it
  shipped, but absent from the usage text and therefore from the generated CLI
  reference on the site.
- The site advertised an older Gemini model than the README in the same worked
  example; both now agree.

### Documentation

- The README leads with what the tool does; the test-soundness argument moved from
  the third bullet into its own section rather than being cut.
- Every runnable example names a herd and says plainly that its model names are an
  example rather than a default.

## [v0.3.6] — 2026-08-12

### Added
- **`corral doctor`** — check the environment before paying for a run. It verifies
  that the sandbox starts, that your test command's toolchain is reachable *inside*
  the sandbox, that a credential exists for every model you plan to route to, and
  that the file you named has a test corral can pair with. Every check is free — no
  model is called — and they run in the order an audit would hit them, so the first
  `FAIL` is the first thing to fix. Exits non-zero if any check failed.
- **Findings served to a coding agent over MCP**, and the *reason* a verdict was
  reached is now recorded alongside the verdict, so an agent can act on a gap
  instead of just being told a number. The findings corpus also works with no brain
  running.
- **The verdict names a single-vendor herd** — a run where every role resolved to
  one vendor now says so in the record, rather than leaving decorrelation to be
  inferred.

### Fixed
- **The jail can see toolchains installed outside `/usr`** (#101). Compilers and
  runtimes from snap, asdf, nvm, rustup, pyenv and Homebrew were on the host's
  `PATH` but invisible inside the sandbox, which surfaced only as an undiagnostic
  baseline failure.
- **CI hands back the test that proves the gap** (#101 sibling). A proven gap was
  reported without the authored test that proved it, leaving nothing to act on.

### Changed
- Documentation corrects a claim the audit path never supported, and adds the
  fourth participant to the description of the herd.

## [v0.3.5] — 2026-08-04

### Added
- The findings corpus works without a brain — no server required to read it.

### Changed
- `verify` now says plainly that **v0.3.3 and earlier accept an edited record**.
  If you are relying on tamper-evidence, upgrade.

## [v0.3.4] — 2026-08-04

### Added
- Twelve verifiable records published — and the hole that publishing them exposed
  is fixed.

### Fixed
- Documentation pinned v0.3.3 rather than the stale v0.3.2.

## [v0.3.3] — 2026-08-04

The release that made non-Go repositories auditable at all. The launch scheduled for
this day was called off when the first run against a real Node project showed it
could not be audited; this release is the answer to that.

### Fixed
- **A real Node/TypeScript project can now be audited at all** (#81), plus two
  further gaps found by actually running corral against a third-party TypeScript
  project.
- **The jail could not see any gem installed on Debian/Ubuntu** — Ruby audits failed
  environmentally, not substantively.
- **The cross-vendor router routes every role**, not just the critic.
- **The authored killing test is written in the *project's* harness**, not the
  plugin's (#85), and a failed authored test now says *which way* it failed (#85).
- A test command's argv survives the trip through `TestCmd` (#91).

### Added
- The brain ingests a cloned repo's `AGENTS.md` as advisory memory.
- `AGENTS.md` — the operating guide this repo never had.

### Changed
- The five-languages claim is replaced with the evidence behind it.

## [v0.3.2] — 2026-08-04

### Fixed
- **`corral version` wrote to stderr**, so anything capturing stdout got nothing.
- An all-Gemini scan asked for a Claude key.
- Self-audit: provision the toolchains the audited suite needs; audit the corral
  actually checked out rather than whatever `@main` resolves to; never spend on a
  stranger's pull request.

### Added
- **The Action accepts several provider keys at once** (`gemini-key`,
  `anthropic-key`, `openai-key`), so the critic need not be turned off.
- The Action writes the verdict where someone will read it
  (`$GITHUB_STEP_SUMMARY`) and bounds what a run costs (`top`).

## [v0.3.1] — 2026-08-02

### Fixed
- **`corral version` said "dev" for everyone who installed it.**

## [v0.3.0] — 2026-08-02

The largest release so far: whole-repo auditing, the GitHub Action, the scan ledger,
and a long run of honesty fixes.

### Added
- **`certify --repo`** — fan an audit out over a whole repository, with ranked
  candidates, bounded spend, derived goals, and full accounting of what was *not*
  audited and why. `--dry-run --json` gives you that inventory for free, with no key
  and no money.
- **corral ships as a GitHub Action**, with diff-scoped audits, `--substrate`
  selection, per-role model flags, `--tests` (a source-to-test map for repos whose
  layout convention can't pair), and an opt-in `--min-kill-rate` merge gate.
- **`corral scans`** — the scan ledger was write-only until now.
- **A DuckDB ledger** of what each scan audited and rejected, plus the evidence
  behind `ProvenMissed` rather than only the count.
- **Opt-in parallel mutant scoring**, order-preserving (safe only on the jail
  substrate — see the flag's own documentation).
- **`--critic-model off`** for deliberately single-vendor runs.
- A coverage pre-flight that fails closed, and test-pairing against ordered
  per-language conventions.
- Signature extractors for Ruby, JavaScript and TypeScript.
- A foreign-repo enumeration sweep pinned in CI on every PR.

### Fixed
- **A surviving canary invalidates the report** — a suite that ignores the audited
  file is *ungradable*, not zero.
- **Stopped fabricating `ProvenMissed`** in repo-aware scoring, and stopped claiming
  pairing evidence for files that were never paired.
- A same-length mutant could reuse a stale `.pyc` and read as a survivor.
- The authored test is sited where the project actually collects it, and a positive
  control proves it ran.
- The workspace substrate is serialized (files share one checkout), and idle workers
  no longer eat the mutant budget.
- Action inputs are no longer interpolated directly into shell scripts.
- Extraction of Python class methods — most real Python was invisible to the scan.

## [v0.2.0] — 2026-07-22

### Added
- The cockpit UI: verbose per-agent audit console, a human gate on proposed tests,
  and views grounded in real DuckDB audit data.

### Fixed
- `COULD-NOT-GRADE` is reported when the jail baseline fails, instead of a
  fabricated score.
- The jail test command is shell-quoted (argv-safe), `GOTOOLCHAIN=local` is pinned
  in the offline jail, and Go dependencies are auto-vendored for `--repo-dir`.
- Corrective test-writer retries feed the compiler's own error back instead of
  blindly repeating.

## [v0.1.0] — 2026-07-03

First tagged release.

[v0.7.0]: https://github.com/pdbethke/corralai/releases/tag/v0.7.0
[v0.6.0]: https://github.com/pdbethke/corralai/releases/tag/v0.6.0
[v0.5.9]: https://github.com/pdbethke/corralai/releases/tag/v0.5.9
[v0.5.8]: https://github.com/pdbethke/corralai/releases/tag/v0.5.8
[v0.5.7]: https://github.com/pdbethke/corralai/releases/tag/v0.5.7
[v0.5.6]: https://github.com/pdbethke/corralai/releases/tag/v0.5.6
[v0.5.5]: https://github.com/pdbethke/corralai/releases/tag/v0.5.5
[v0.5.4]: https://github.com/pdbethke/corralai/releases/tag/v0.5.4
[v0.5.3]: https://github.com/pdbethke/corralai/releases/tag/v0.5.3
[v0.5.2]: https://github.com/pdbethke/corralai/releases/tag/v0.5.2
[v0.5.1]: https://github.com/pdbethke/corralai/releases/tag/v0.5.1
[v0.5.0]: https://github.com/pdbethke/corralai/releases/tag/v0.5.0
[v0.4.0]: https://github.com/pdbethke/corralai/releases/tag/v0.4.0
[v0.3.6]: https://github.com/pdbethke/corralai/releases/tag/v0.3.6
[v0.3.5]: https://github.com/pdbethke/corralai/releases/tag/v0.3.5
[v0.3.4]: https://github.com/pdbethke/corralai/releases/tag/v0.3.4
[v0.3.3]: https://github.com/pdbethke/corralai/releases/tag/v0.3.3
[v0.3.2]: https://github.com/pdbethke/corralai/releases/tag/v0.3.2
[v0.3.1]: https://github.com/pdbethke/corralai/releases/tag/v0.3.1
[v0.3.0]: https://github.com/pdbethke/corralai/releases/tag/v0.3.0
[v0.2.0]: https://github.com/pdbethke/corralai/releases/tag/v0.2.0
[v0.1.0]: https://github.com/pdbethke/corralai/releases/tag/v0.1.0
