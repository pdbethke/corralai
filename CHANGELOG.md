<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Changelog

Notable changes to corral. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions are [semantic](https://semver.org/), and pre-1.0 means the CLI surface can
still move between minor versions.

Entries describe what changed for someone *using* the tool. For the full commit
history of any release, `git log v0.3.4..v0.3.5`.

## [Unreleased] — toward 1.0.0-rc.10

- **Seven doors of `cmd/corral` say what the record says.** Found by a Claude
  Code reviewer on the verbs (review `8377ae6320cc`, Codex verifying; all seven
  confirmed). A retraction now reaches every door a person looks through —
  `corral ui` marks a retracted entry of any kind and lists only standing
  reviews, `review show` announces a withdrawn review before printing it,
  `review plan` never counts one as coverage — through ONE reader
  (`readLedgerRecord`) that `brief` uses too. `seal --repo --json` names
  the scan's own time `audited` (null when none was recorded, never the push
  time under that name), carries `pushed` beside it, and the caveat and
  its three honesty flags — from the same functions the text table uses.
  `review --attest` says "signed into" only for an envelope this run wrote,
  removes a stale one first, and reports the signer's error by name. `scans
  show -h` reaches usage (flags first), which put its four flags on the
  executed-surface manifest for the first time. The generated CLI reference
  no longer documents `scans push` — a verb the dispatcher lost — with its
  error as the body: the generator derives every verb and refuses a section
  whose body is an error, and a docs gate checks the shipped reference.
- **The Action runs the review.** `reviewer-model` (and `verifier-model`)
  run `corral review` on the pull request after the audit: the changed
  top-level directories (or `review-scope`), at most `review-max-scopes`,
  one signed entry each in the same `ledger` directory, each report mirrored
  into the step summary. `review-fail-on: reproduced` (default) fails the
  step with the new `corral review --fail-on reproduced` exit 3 when a
  REPRODUCED finding stands — after the entry is written; `never` only
  records. The agentic seats are refused on a runner by name; CI review is
  API seats through the `*-key` inputs.

- **One shape rule at both doors of the ledger.** An adjudication with no
  finding id, an empty verdict and nobody deciding was placed, signed and
  *verified* when it entered through `corral ledger append` instead of
  `review adjudicate` — the verbs validated, the placer did not, and the
  verifier trusted the placer. Found by a Codex reviewer re-attacking the
  morning's fix batch (review `559719120ef0`, Claude Code verifying).
  `EntryShapeProblem` is now the one rule for every kind — retraction,
  checkpoint, review, adjudication — used by the placer (refuses) and by
  `ledger verify` (names the entry). The reviewer's reproduction is the
  regression test, inverted.
- **The container jail honours `PerEntry` binds.** The bwrap backend
  mounted a per-entry bind as one read-only mount per top-level entry so the
  parent stayed writable; the container backend mounted the whole tree
  read-only, and toolchains that write a cache into `node_modules` hit
  EROFS on containers only. Found by a Gemini reviewer on the jail's first
  review (`f29544721ecd`, Codex verifying).
- **One party is named once.** A `Co-authored-by` trailer that repeats the
  author (a squash merge does this) folded into the author, and repeated
  trailers fold — seen on the first entry to carry the party ("change by P,
  with P, Claude Code"). The committer seat counts one observation per
  party per audit, even for rows written before the fold.
- **The review loop keeps score honestly.** Six defects in `internal/review`
  found by the loop reviewing itself (a Claude Code reviewer, Codex
  verifying; review `61dc210a39fd`), all confirmed on adjudication. A
  reproduction the HARNESS could not run — the worktree gone, `sh`
  missing, a timeout — was recorded like a script that ran and disagreed,
  and the reviewer was charged for it "by execution"; it is now `unrun` on
  the record, has no outcome, and grades nobody. A verifier's refutation
  that reproduced against a CODE-READ claim was run, recorded, and then
  never consulted; a refutation that reproduced is now an outcome for a
  finding of any tier. `Parse` took the first parseable object in the
  reply as the review, so a JSON literal in the prose dropped the findings
  behind it; it now wants a review-shaped object or errors. A scope with
  `..` (or a symlink) read outside the checkout and shipped the bytes to
  the provider; `LoadScope` refuses by name. The planner counted every
  finding of a covering review toward every scope it covers; a finding
  now counts where its file is. A verifier's ids are read as the review
  names them (`r1`, `1` → `R1`), the first verdict on an id stands, and an
  unknown id or a second verdict is said on the record (`verifier_note`),
  never dropped. `corral review` prints "NOT RUN (harness)" for the first
  case, not a demotion.
- **The prior's cut line names the tail it dropped.** This morning's fix
  said where the undisclosed edits were — but sliced the caller's
  UNSORTED list after Render began sorting a copy, so it named whichever
  edits sat past the cut in the caller's order. Found by a Gemini reviewer
  the same afternoon (review `dcd0874fecb8`), Claude Code verifying: a fix
  that introduced a defect, caught by the next round. It slices the
  ordered list.
- **The scorer proves every door it grades through.** Four defects in
  `internal/adequacy/score.go` found by a Claude Code reviewer with Gemini
  verifying (review `b3307bbbb5e6`), all confirmed on adjudication, three
  reproduced. The canary — the fail-closed check that the suite actually
  reaches the file — ran only with the shared test command, so under
  per-mutant narrowed commands (the selection default) a narrowing that
  never executed the file reported every mutant as a **survivor** under
  `CanaryKilled=true`: a coverage gap that did not exist. The canary is now
  proven once per distinct grading command, and a command that passes on
  source that cannot compile leaves its mutants UNMEASURED, with the
  reason on record. The per-command timeout was keyed by the raw argv and
  looked up with the fail-fast argv, so with both options set — which the
  gate always sets — it silently never applied; one key at both doors. A
  timed-out mutant's health re-probe ran the *shared* suite under the
  mutant's narrowed budget, so a slow suite aborted a whole file's report
  as "too loaded" for a real non-terminating mutant on a healthy box; the
  re-probe runs the command that graded the mutant. And an unmeasured
  mutant's grading record (which command, which rule) was computed and
  dropped; it is kept. The reviewer's three reproductions are regression
  tests, inverted (`reviewed_scorer_doors_test.go`).
- **The signed statement carries what the record says. Review predicate
  `https://corralai.dev/review/v2`.** Six defects in `internal/certify`,
  one shape, found by a Claude Code reviewer on the signing code's first
  full round (review `167dd45b25cc`, Gemini verifying), all six confirmed.
  A `certify --local` record measures no wall clock and its statement
  signed `durationS: 0` — a claim, over a signature, that the audit took
  no time; the duration is now optional and omitted when unmeasured, like
  an unmeasured kill rate. The reproductions hash did not cover the
  harness-failure marker (`unrun`) added the same morning, so the marker
  could be added or cleared under a valid statement; v2 hashes it, and
  hashes the reproductions as canonical JSON (sorted keys) rather than
  this binary's struct order — the statement map now holds no Go struct,
  which `CanonicalStatement`'s own contract required. The predicate also
  carries the review's own coverage note (a blanket approval is attested
  AS one), the seats' tools and versions, the language, the audited party,
  the bytes shown and the verifier's note — everything the entry records
  about HOW the review was made, so an agentic seat that read the whole
  checkout is distinguishable from an API seat under a cap. v1 statements
  still verify: `corral verify --attest` recomputes the reproductions under
  the rule the statement's predicate version was written with, and the v1
  rule is pinned by a test against the old bytes. A per-file audit entry
  whose every mutant was rejected by the compile gate signs its absent kill
  rate with `noGradableMutant`, the flag that says which kind of unmeasured
  it was — the one predicate the report's marker and the statement share.

## [v1.0.0-rc.9] — 2026-09-07

The record names both parties: who wrote the change beside who judged it,
and a committer seat that grades each — a person and an agent alike — by
the changes that held. And `corral brief`, the auditor's report: what the
record says is open on these files, for whoever writes next.

- **`corral brief` — the auditor's report.** The record, handed back to
  whoever writes next: `corral brief --scope <path>` (repeatable) or
  `--changed <base ref>` renders, per file, the newest scan's verdict, each
  fault the suite missed — with its hunk when the entry carries one, and
  the authored test that closes a proven gap — and each review claim that
  stands, with the verifier's verdict and any adjudication; claims that did
  not hold are counted, not listed. Retracted entries are left out and the
  header says how many. `--json` is the same report as one document for an
  agent to read; `--max-items` bounds it and names the cut. It reads the
  ledger and writes nothing, and it renders nothing the entries do not
  hold. The same move `--prior` makes for the mutant generator, made for
  the author.
- **The record names the audited party.** *Nemo iudex in causa sua* names
  two parties, and until now the record named only the judge. A scan entry
  and a review entry now carry the commit's author, committer and
  `Co-authored-by` trailers — by name, never an address; the trailer is
  where an agent is, in code an agent helped write (`Claude Code`, `Codex`)
  — and the warehouse has them as `author`, `committer`, `co_authors` (one
  name per line) on `corral_scans` and `corral_reviews`. A checkout that
  cannot say records nothing. `corral review` prints the party beside the
  seats. `corral models rank --seat committer` grades each party — a
  person and an agent the same way — by the changes that held under audit
  (a scan that passed its gate; a review none of whose checked claims
  held), under the same evidence floor as every seat; a row written before
  the party was recorded is evidence about nobody. The row says what it
  measures and nothing about what to do with it; its first use is the
  party's own view of what the audit gave back. Additive: older entries
  and warehouses lack the fields.

## [v1.0.0-rc.8] — 2026-09-07

The record hashes what it holds: a three-seat round on `internal/auditpush`
found that the ledger's sparse hash let a recorded zero be edited away
under a valid signature — format `corral-ledger-3` fixes it, with five
more. Any agent you assign can sit in a seat; the authored test keeps its
suffix; the prior merges by edit.

- **The ledger entry's hash is over its bytes; a retraction reaches every
  kind; the identity derives. Format `corral-ledger-3`.** Six defects in
  `internal/auditpush` — the record itself — found by a Claude Code reviewer
  on the first three-seat round (Codex verifying; review `ed079ca08965`), all
  six confirmed on adjudication. The entry hash was over the SPARSE canonical
  form, which prunes `false` and `0`, so an entry recording *passed: false,
  kill rate 0* and one recording *never measured* hashed and signed
  identically — an entry could be edited from one claim to the other and
  still verify, which falsified the one sentence the ledger exists for. From
  format 3 the hash is over the entry's full canonical bytes as written (a
  reader re-hashes the file, not this binary's struct), and `verify` names
  an edit. A scan entry's `scan_uid` now derives from its row and its own
  `pushed` time, so `RecomputeScanUID` over the entry — or the view's row —
  reproduces it (it never did: the uid was minted from a clock the entry did
  not carry); `verify` checks that too. A retraction now reaches reviews and
  adjudications as it reached scans — a retracted review's rows no longer
  load into the view or push to a warehouse. `CanonicalizeForWarehouse`
  normalises the two timestamps added with the ledger (`finished_at`,
  `computed_at`), which `verify --db` had been reporting as a false tamper
  on every run that carried them. A chain check names the entry's own file
  (two sort orders were being re-paired). A review push is one transaction.
  Format-2 entries still read and verify under their own rules, and the
  check says which rules those were. Review entries are named by their own
  hash, so two reviews of one commit inside a second do not collide.
- **The README opens with what corral is:** an auditing engine, with two
  extensions — `certify` by execution, `review` by adversary.
- **The authored test keeps its suffix.** `authoredTestPath` inserted the
  `_corral` marker by an unanchored first-occurrence replace of the code
  file's stem, so a stem that is a substring of the test suffix or the
  extension mangled the name: `st.go` beside `login_test.go` authored
  `login_test_corral.go`, `go.go` beside `foo_test.go` authored
  `foo_test.go_corral` — files no runner collects, the trap that reports
  `proven_missed 0` forever. The stem is now matched as a whole token in
  the name part only. Found by a gemini reviewer on `internal/advpool`,
  let stand by a Claude Code verifier, confirmed (review a79b583bacea#R1);
  the reviewer's reproduction is the regression test.
- **An agentic seat may read with a shell.** The reviewer's brief told an
  agent it "cannot run tests or commands, and must not try"; an agent whose
  only file interface is a shell (Codex) took that literally and, twice,
  returned a null review — recorded correctly as coverage unknown, but a
  round spent. Both briefs now carry one paragraph: read by whatever your
  tool provides, a file reader or a shell used only to read; do not run
  the tests, build, or execute the code under review; the script you hand
  back is what corral runs. Found by running the three-seat round.
- **The ledger's mutant rows carry their hunks, and the prior merges by
  edit in any order.** Every ledger row was bare (no search/replace), so a
  later run's prior could not tell two runs' different edits at one place
  apart and folded whichever arrived second into the first — order-dependent
  (a Cursor read of #286, confirmed). The recorder that fed `--record-mutants`
  is now always on and stamps each row's `code` with its hunk; `code` was
  already in the custody set, so a `--push` without `--push-source` still
  withholds it. The prior reads the hunk back, keys an edit by its hunk
  whenever a row has one, folds a bare row into the one hunked edit at its
  place (and keeps it separate beside several), and orders edits totally,
  so Render and Digest never depend on the order the sources were read in.
- **A prior recorded against no bytes is refused as such on the report.**
  The `certify --repo` path now pins the sentence — "recorded with no
  version of this file to match against" — beside the different-version
  one, in the CLI test that drives both.
- **An agentic seat is any agent you assign, not a vendor list.** A seat
  name is a command-line definition: `CORRALAI_AGENT_<NAME>="<command
  line>"`, run in the disposable worktree with the brief on stdin and the
  reply on stdout (or in `{out}`); `{dir}` is the worktree, `{model}` /
  `{model:FLAG}` carry the pin from `<name>:<model>`. A pin the definition
  cannot carry, a definition that demands a pin it was not given, and a
  definition that does not split are each refused by name — corral never
  guesses what an agent runs. `claude-code` and `codex` are defined in the
  same form and run the same argv as before. An operator-defined agent's
  record carries its definition beside its version, since that is what sat
  in the seat. Corral does not confine what the command does; the
  definition is the operator's.

## [v1.0.0-rc.7] — 2026-09-07

The seats can be coding agents, and the first agentic round paid: Claude
Code reviewing and Codex verifying `internal/prior` found six defects, all
confirmed, all fixed here; a round on `internal/brain` found a security gap.

- **The prior no longer loses, disguises or misreports what was tried.** Six
  defects in `internal/prior`, all found by the first agentic review round
  (Claude Code reviewing, Codex verifying, `corral review --scope
  internal/prior`), all six confirmed on adjudication. Mutant ids are
  positional per run, so two runs' different edits at one place shared a
  merge key and the later one was DROPPED — and one run's hunk was grafted
  onto another run's line; the key now identifies the edit. `Digest` did not
  hash the hunk it renders, so two priors handing the generator different
  text shared a cache key. Edits recorded with NO parent hash were reported
  as "a different version" (now `ErrNoVersion`, said as such). Invalid and
  timed-out mutants were silently excluded, so the next generator re-rolled
  them. The bounded render said "… and N more" without saying where — the
  cut is always the file's tail, and it now names the stretch. A hunk was
  cut mid-codepoint. The reviewer's two held reproduction scripts are now
  regression tests, inverted (`reviewed_merge_key_test.go`,
  `reviewed_digest_test.go`).
- **Agentic seats: Claude Code and Codex as the reviewer or the verifier.**
  `--reviewer-model claude-code` / `codex` (or pinned, `claude-code:<model>`,
  `codex:<model>`) starts the coding CLI in a disposable worktree at the
  commit with read-only tools, the brief on stdin and the scope's file
  list — it reads the tree itself, beyond any byte cap — and its final
  message is the reply. The contract does not move: it hands back scripts,
  corral runs them in another copy, and nothing the agent did itself is
  on the record. The tool and its version are recorded beside the model;
  the decorrelation rule sees through the tool to the model. This is what
  the five cold reviews of corral were by hand. First run, on
  `internal/prior` (Claude Code reviewing, Codex verifying): six findings,
  two with scripts that held, all six let stand with line references,
  all six confirmed — on a package flash had passed with a false claim.
- **SECURITY — the brain's ad-hoc SQL door is a human door.** `mission_analytics`
  with `sql` gated on `isAdmin`, which a delegation token minted for a
  subagent under a superuser passes; every other admin door gates on
  `isHumanAdmin`, which it does not. Such a token could read the whole
  telemetry store. Found by `corral review --scope internal/brain`
  (claude-sonnet-5 reviewing, 2026-09-07): its reproduction script held and
  is now the regression test, inverted. A rule enforced at one door and
  not the other — the fifth review's shape, caught by the loop it produced.
- **A verifier that returns no verdicts is said, not silent.** A verifier
  whose reply parsed but carried no verdict on any finding (flash, on a
  450 KB scope) was recorded as if it had nothing to say; now its reply is
  kept on the record, marked, every finding is graded unverified, and
  stderr says so.

- **`corral ledger push <dir> <dsn>`.** The directory's record — every
  scan, review and adjudication entry — appended to a warehouse or
  MotherDuck, skipping what the target already holds by entry hash
  (`corral_scans.entry_hash`, new and additive; `review_uid`;
  `corral_adjudications.entry_hash`), retracted scans left out and said,
  retractions and checkpoints counted as chain facts. Source travels only
  with `--push-source`; `--dry-run` plans and creates nothing, not even
  the file. A run's own `--push` now stamps its entry's hash on the scan
  row, so a later push of the same directory skips it. This is the
  backfill verb `corral scans push` was for the retired record.
- **`corral review plan` — the round planner.** Every scope of the
  repository (directories two deep holding source), when the ledger last
  saw it reviewed and at what commit, its findings by outcome (held, fell,
  awaiting a verdict), how many of its files changed since that commit —
  and a proposal for the next round: never reviewed first (largest first),
  then changed since review (a fix batch nobody has re-attacked), then the
  stalest. No model runs, nothing is written, and it is a proposal: a
  person names the scope. On corral itself it said what was true — every
  scope reviewed that evening had since been changed by the fixes those
  reviews prompted.

## [v1.0.0-rc.6] — 2026-09-07

The review loop closed: the reviewer and the verifier graded, and the
three warehouse grains that carry the argument to MotherDuck.

- **The reviewer and the verifier are graded.** `corral models rank --db
  <ledger dir>` now carries two more seats. A reviewer's row is *claims
  that held, of those checked*; a verifier's is *verdicts that agreed with
  the outcome*. The outcome of a finding is one rule (`internal/review`,
  `OutcomeOf`): a person's adjudication when there is one; else execution
  — a REPRODUCED claim's recorded tier after its script and any reproduced
  refutation; a CODE-READ or HYPOTHESIS claim nobody adjudicated has no
  outcome and grades nobody. A verifier is right when REFUTED met a claim
  that did not hold or STANDS met one that did. The language dimension is
  the scope's. The evidence floor applies as to every seat: a row below
  `--min-runs` is printed and never preferred. Over one evening's reviews
  of corral itself: reviewer flash 2/3, sonnet 1/3; verifier flash 3/3,
  haiku 1/1 — all insufficient at the default floor, as they should be.
- **A review with nothing in it says so.** No findings and fewer than
  three items checked-and-found-sound is recorded and printed as
  `coverage unknown … a blanket approval, not a review`.

- **The review loop's warehouse grains.** Three tables, additive to the
  five `certify` pushes: `corral_reviews` (one per review, keyed by
  `review_uid` = the entry's hash), `corral_findings` (one per finding —
  declared and recorded tier, script and output as hashes always and as
  bytes with `--push-source`, exit, demotion, the verifier's refutation on
  the same terms) and `corral_adjudications` (one per verdict, joined on
  `(review_uid, finding_id)`). The view over a ledger directory loads
  them; `corral review --push <dsn>` and `review adjudicate --push <dsn>`
  append them to a warehouse or MotherDuck; `models rank` reads the
  reviewer and verifier seats from the grains — so `--db md:` grades the
  seats the same way the directory does, across every repository that
  pushes. `corral_reviews.lang` is the scope's language.

## [v1.0.0-rc.5] — 2026-09-07

The verifier seat, the reproductions signed, and the record readable in
a browser — chain, reviews, the two seats' argument, and the person's
verdict.

- **The verifier seat.** `corral review --verifier-model <m>`: a third
  model, never the reviewer's (refused before anything is spent), is
  handed the review as recorded — demotions, scripts and their output
  included — and told to refute every finding. Its refutations carry the
  same tiers and the same demote-only rule: a REPRODUCED refutation whose
  script does not hold becomes CODE-READ on the record; one that holds
  demotes the finding it refutes, with the record naming the model and the
  argument; a CODE-READ refutation is carried as opinion; STANDS is carried
  with what was tried. The brief writes down the rule the fifth review
  taught: a search that finds nothing is never a refutation. A person's
  adjudication still outranks all of it.
- **`corral review --attest <path>`: the reproductions, signed.** An in-toto
  statement (predicate `https://corralai.dev/review/v1`) carrying, per
  finding, the declared and recorded tier, the sha256 of its script and of
  what it printed, its exit, and the verifier's refutation on the same
  terms — plus `reproductionsSha256` over all of it. The opinion is bound
  by its hash and never carried: the signature vouches for what was
  executed, not for a judgment. The statement is written first and the
  ledger entry then names it; `corral verify --attest <path> --db <ledger
  dir>` finds the entry by that name and recomputes the reproductions'
  hash, and an entry edited after signing is named as such.
- **`corral review` could not read a reply with a brace in its prose.**
  `extractJSON` took the first `{` in the text as the payload. Found by
  the reviewer seat on `internal/review` itself; the verifier seat, told
  to refute it, could not and said so; confirmed and fixed.

- **A Claude 5 seat that thought through its whole budget returned nothing,
  silently.** The Anthropic backend sent `max_tokens: 4096`, which bounds
  thinking as well as text; a sonnet reviewer spent all of it in one empty
  thinking block, the API said `stop_reason: max_tokens`, and corral read
  an empty message as the model saying nothing. The budget is 32,000, and
  a reply with no text that ran out of it is an error naming the model,
  the budget and the tokens spent. Found by asking why a review had zero
  findings.
- **`corral ui` shows the whole record, not just the seal.** Over a ledger
  directory the page now carries the chain — every entry newest first,
  its kind, what it is about, its signature checked against the local
  certify key, a retracted scan marked, a problem named — and every
  review: the opinion, the verifier's opinion, each finding with its
  declared and recorded tier, the reviewer's and the verifier's scripts
  with their output (collapsed), the demotion reasons, the verifier's
  STANDS/REFUTED argument, and the person's confirmed/refuted verdict.
  Read fresh on every reload. Over a warehouse file the page says the
  chain and the reviews are not there rather than rendering an empty one.

## [v1.0.0-rc.4] — 2026-09-06

`corral review`: a cold model's opinion, linked to reproductions the run
executed — the review loop's first slice, run on corral itself the day it
was written.

- **`corral review` — the review loop's first slice.** A cold reviewer
  seat (`--reviewer-model`, no default) is handed a scope of the repository
  at HEAD and told to assume the code is wrong. Every finding carries a
  tier it declared — REPRODUCED with a `sh` script that exits 0 iff the
  defect is demonstrated, CODE-READ, HYPOTHESIS — and the run executes
  every REPRODUCED script against a detached worktree at the commit; a
  script that does not hold demotes its finding to CODE-READ on the record,
  saying why. The reviewer must list what it checked and found sound. The
  review is one ledger entry (`kind: review`) beside the audits; the
  opinion is printed and carried, and only the reproductions are what the
  entry's signature vouches for. `corral review adjudicate <dir> <hash>#Rn
  --confirm|--refute --reason` is a person's verdict as its own entry (the
  newest per finding stands); `corral review show` prints a review with its
  adjudications; `verify --ledger` names both kinds. Not a gate: exit 0
  either way. The verifier seat, the statement, the scorecard rows and the
  warehouse grains follow (docs/design/adversarial-review.md).
- **`corral ledger append` refused nothing for a relative directory.** The
  "never re-linked in place" check compared an absolute source against a
  relative target and skipped itself on the error. Found by `corral review`
  on the file, the day the verb was written.

## [v1.0.0-rc.3] — 2026-09-06

`corral ledger` runs. rc.1 and rc.2 documented and tested it and never
dispatched it; the gate that let that through enumerated instead of
derived.

- **`corral ledger …` is dispatched.** In rc.1 and rc.2 the ledger verbs
  were documented and tested through their own function, and the shipped
  binary never reached them: `subcommand()` was a hand-maintained
  allowlist that `ledger` was never added to, so `corral ledger append`
  fell through to starting the coordination server (rc.1) or to the
  no-subcommand pointer (rc.2). The allowlist is gone — main's own switch
  decides, and an unknown verb is refused by name — and the gate now
  derives every documented verb from the usage text and every dispatched
  one from main.go's source and holds the two sets equal. Found by running
  the installed rc.2, not by any test; the sixth time an enumeration
  stood where a property should have.

## [v1.0.0-rc.2] — 2026-09-06

The record can be taken back without being rewritten, and the recipe runs
on any forge.

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
