<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Changelog

Notable changes to corral. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions are [semantic](https://semver.org/), and pre-1.0 means the CLI surface can
still move between minor versions.

Entries describe what changed for someone *using* the tool. For the full commit
history of any release, `git log v0.3.4..v0.3.5`.

## [Unreleased]

### Added

- **A ledger entry can be signed keylessly, by the GitHub repository that ran
  the audit.** Set `CORRALAI_LEDGER_SIGNER=sigstore` in a GitHub Actions run
  (with the `id-token: write` permission, which it requires) and every ledger entry corral writes there is signed
  through Sigstore with the workflow's own identity instead of a key an
  ephemeral runner cannot keep. Such an entry is format `corral-ledger-6`, keyid
  `sigstore`, and carries a Sigstore bundle in place of an Ed25519 signature.
  Any other value is refused naming `sigstore`; unset keeps today's behaviour.
  `corral verify --ledger`, `corral ledger verify` and `corral ui` take a new
  `--ledger-repo owner/name`, which a keyless entry is checked against; it is
  required only when the chain holds one, and without it that entry fails with
  a Problem saying so. It is never guessed from the scan's repo, the directory
  or git config. The Sigstore root the entry is checked against is the
  public-good root embedded in the release.
  Keyless signing has been exercised only against an in-process test
  Fulcio and Rekor; it has not yet been run end to end in GitHub Actions.
  `corral ledger checkpoint` and `corral ledger signer add|revoke|list` verify
  under the root key only and cannot operate on a ledger that holds keyless
  entries (the signer verbs report those entries as failing; `checkpoint`
  refuses outright, before deleting anything, because pruning would delete
  entries a repository's identity vouched for).
  **Older readers:** a corral release before this one cannot read, and refuses
  the whole ledger, once it holds a `corral-ledger-6` entry. Key-signed and unsigned entries are still
  written as `corral-ledger-5`, so a ledger nobody signs keylessly stays
  readable by older releases.

### Changed

- **`--substrate workspace` no longer hands the audited suite your desktop
  session.** `DISPLAY`, `WAYLAND_DISPLAY`, `WAYLAND_SOCKET`, `XAUTHORITY` and
  `DBUS_SESSION_BUS_ADDRESS` are removed from the test command's environment,
  alongside corral's own model keys, for `certify --repo` and `corral review`.
  The workspace substrate runs the suite on the host once per mutant, so a
  suite with UI tests opened real windows on the operator's screen again and
  again. A suite that needs a display gets one in its own test command, for
  example `-- xvfb-run -a cargo nextest run`. If you ran corral itself under
  `xvfb-run`, move `xvfb-run` into the test command. The jail never passed
  these.

## [v1.0.0-rc.28] — 2026-10-21

### Fixed

- **Fixtures in a test tree reach the jail.** The jail's copy of a
  repository dropped every file that is not valid UTF-8 except under a
  `testdata` directory, which is Go's convention. Other languages keep their
  fixtures in their test trees, so pylint's
  `tests/regrtest_data/unicode/*_utf_32_bom.txt` were dropped and two of its
  tests failed inside the jail on unmutated code. A non-UTF-8 file in any
  directory a language names as a test directory (`tests/`, `spec/`,
  `__tests__/` and the rest) is now copied too; one outside every test tree
  still is not.
- **A failing instrumented suite says it failed.** When the per-test
  selection run exited non-zero, its last line, the test runner's own summary,
  was handed to the JSON reader: pylint's `2 failed, 440 passed` was reported
  as `invalid character 'f' after top-level value`. The note now gives the
  exit code and the line the run ended on.

## [v1.0.0-rc.27] — 2026-10-21

### Added

- **`certify --repo --jail bwrap|container|sandbox-exec`.** The scan always
  ran in the platform's default sandbox (bwrap on Linux), while
  `certify --local` and `doctor` could already run theirs in a container.
  `--jail container` runs every test command in `CORRALAI_EXEC_IMAGE`, so a
  project's test dependencies can live in an image instead of being installed
  under `/usr` on the host. Same container flags as `--local`: offline,
  read-only root, every capability dropped. `--jail` beside
  `--substrate workspace`, which runs no sandbox, is a usage error. Go
  repositories that need `go mod vendor` are still refused under the
  container backend, as they are on `--local`. `review` still takes the
  default sandbox.
- **Every scan entry names the jail it ran in.** `certify --repo` and
  `certify --local` entries record `SandboxBackend` (`bwrap`, `container`,
  `sandbox-exec`) and, for a container, `SandboxImage` as
  `<ref>@<image id>`: the toolchain the tests ran with, pinned to the ID the
  runtime resolved, since a tag can be rebuilt. `Substrate` said only
  `jail`, which could not answer which one. `certify --repo` refuses a
  container image the runtime cannot identify before any spend. Both fields
  reach the warehouse (`sandbox_backend`, `sandbox_image`, added to an
  existing `corral_scans` on the next push) and the record page. Entries
  written before this name no jail. The verdict cache keys on the image too,
  so a verdict graded in one image never answers for another; a jail with no
  image keeps the key it had, so cached bwrap verdicts stay valid.

### Fixed

- **Go 1.26.10, github.com/klauspost/compress 1.18.7.** The standard-library
  advisory in `crypto/x509` (GO-2026-6809, fixed in go1.26.10) is reachable
  from corral's own call paths, so the module floor and the Dockerfiles move
  up. `klauspost/compress` comes in through DuckDB's Arrow dependency; its
  advisory is in the `s2` package, which corral does not reach (govulncheck
  does not report it), and it moves to the patched release anyway.

## [v1.0.0-rc.26] — 2026-10-20

### Breaking

- **A ledger can now say who may sign it, and every entry corral writes is
  now `corral-ledger-5`.** `corral ledger signer add|revoke|list` registers
  and retires signing keys inside the chain itself: a registration or
  revocation is an entry, signed by the root key, and `verify` checks each
  later entry's signature against the key the chain registered for it rather
  than one key for the whole ledger. What breaks:
  - **An older corral cannot read a ledger this corral has appended to**,
    signers or not, because every new entry is format 5. That applies as soon
    as any entry is appended by this version, by anyone, to any ledger.
    corral's own `corral/ledger` receives none until a release carrying
    format 5 is tagged and the docs are pinned to it. Older entries verify
    unchanged here.
  - **`keyid` is the key's fingerprint** (`ed25519:` and 16 hex characters of
    its SHA-256) on new entries, not the name `corral-certify`; on older
    entries `corral-certify` still means the root.
  - **`verify` output changed for every signed entry, plain chains
    included:** it reads `signed by root (<keyid>)` or
    `signed by <name> (<keyid>)` where it used to print the bare key name.
    Anything parsing that line must change.
  - **`corral ledger checkpoint` on a chain that has signers** is a root
    operation: it needs `CORRALAI_LEDGER_ROOT_KEY` and `--expect-head <hash>`
    from outside the directory. A chain without signers checkpoints as before.

  Also new, not breaking: root operations read the root seed from
  `CORRALAI_LEDGER_ROOT_KEY` alone, never from a file corral finds, and never
  fall back to the machine's certify key. `--pub` and `CORRALAI_LEDGER_PUBKEY`
  name only the root; with neither, signatures are still not checked, and a
  keyless read believes any signer entry that names the root, so only a read
  under the root key authorizes anything.

- **`certify --local` takes its subject from `--repo-dir`, not from where you
  ran it, and a repository that cannot be derived is recorded as unknown.**
  - **The recorded repo and commit change for anyone who ran `--local` from a
    different directory than the one audited.** Before, repo came from the
    process's working directory (`git config --get remote.origin.url`) and
    commit from its HEAD, so a run was filed under whatever repository the
    shell sat in. Now both come from `--repo-dir`'s git, and the repo is
    `git remote get-url origin`, which applies `url.<base>.insteadOf` rewrites:
    the name can differ from the raw configured URL. A trailing `.git` is
    dropped, the rule `certify --repo` already used, so one checkout is filed
    under one name by both verbs (`https://github.com/o/r`, not
    `https://github.com/o/r.git`).
  - **A `--local` run with no `--repo-dir` used to record the working
    directory's origin and HEAD and now records `""` (identity `none`)**, since
    there is no repository to ask about.
  - **`--repo` and `--commit` on `certify --local` are now both-or-neither.**
    Passing exactly one exits 2.
  - **`""` (identity `none`) replaces the invented `"local"`** as the repo and
    commit when git cannot tell, in store rows and in ledger entries, and
    `certify --repo` no longer hands a tree without a HEAD the commit
    `"local"`. A reader that matched the string `local` must match the empty
    value. A typed `--repo` and `--commit` are recorded as identity `operator`.

### Added

- **`certify --local` writes a ledger entry.** One hash-linked entry per run
  (signed when a certify key is configured) goes to the machine ledger
  (`$STATE/ledger`; `CORRALAI_MACHINE_LEDGER` moves it), where before a
  `--local` run left no ledger entry. Not breaking: it is fail-open (a failure
  is printed and never changes an exit code) and `--no-ledger` opts out. A new
  `--ledger <dir>` writes it there as well; it is refused inside `--repo-dir`,
  because `--local` never writes into the checkout, and a machine ledger that
  resolves inside `--repo-dir` is skipped with the reason printed. `corral
  demo` runs `--local` and has no opt-out: its entries go to the machine ledger,
  so its measurements are not kept only in DuckDB. They record no repo identity
  (empty repo and commit, identity `none`), even when the demo directory is inside a git
  checkout.
- **Ledger entries carry what the run did, and `review` keeps a copy too.**
  A scan entry now records whether the audited tree differed from HEAD
  (`TreeDirty`: unknown when git could not say or when the recorded commit
  is not HEAD, never `false` by default), how the repository was identified
  (`git`, `operator` or `none`), and, per audited file, three row sets: the
  seats that ran with their scorecard counts, the critic's findings, and,
  only when a challenger writer ran (`--shadow-writer-model`), each writer
  seat's killed-or-survived outcome per surviving mutant. An entry is signed
  as a whole when a certify key is configured, rows included. On
  `certify --repo` an `--attest` statement also signs the three row sets as
  `telemetrySha256`; that digest covers only them, and the critic's evidence
  text is not in it. A `--local` entry has no statement, so its rows are
  covered by the entry's signature alone. Entries stay `corral-ledger-5` and
  a bundle without the new fields hashes as before. Two runs appending to one
  ledger directory at once (two certify runs sharing the machine ledger) now
  take turns under a lock instead of both linking to the same head, which
  had broken the chain for good. `certify --repo` and `corral review` write their entry where they
  did and also append a copy to the machine ledger, fail-open and never inside
  the audited checkout (skipped with the reason printed if it resolves there);
  `--no-ledger` skips it, a `--ledger` that already is the machine ledger is
  not written twice, and a `--repo` tree with no commit writes no entry. Where
  each verb writes is stated in `AGENTS.md` ("Where corral writes") and the
  configuration and trust-and-security pages.

### Changed

- **`corral ui` and the public record page now render the ledger with one
  renderer, `internal/recordview`, and agree on what it says.** They used to
  disagree on which verdicts count, how chain checks pair with entries, and
  what an unchecked signature means. Now chain checks pair by hash, a failed
  signature never shows as "unchecked", only live verdicts count, a retracted
  review is marked retracted on the chain page (the public record included),
  and an empty chain no longer calls itself intact. An entry signed by a
  registered signer names it. A target with no ledger says so instead of
  showing zero counts, and seal rows whose baseline failed sort after the
  graded ones instead of ranking by a 0.00 kill rate. The pages fit a 375px
  screen. What a script or proxy may notice:
  - `corral ui`'s single-page app is gone, along with `/api/ledger` and
    `/api/seal`; the record is served as pages.
  - In read-only mode a POST to a write route returns 405, not 404.
  - A seal that cannot be read shows an error banner instead of a 500.
- **`corral ui --write` keeps its launch token out of URLs.** The printed
  URL's `#t=` fragment moves into the tab's `sessionStorage`, leaves the
  address bar, and is sent only as a Bearer header; every page picks it up,
  so the URL the command prints works on any page. A tab that never opened
  that URL cannot write. This runs in a real headless browser in CI.

## [v1.0.0-rc.25] — 2026-10-18

- **JavaScript, TypeScript and Ruby critics must now judge every test.**
  The critic is handed the test file's tests by name, read from the source:
  describe/it titles for jest, vitest, mocha and node --test, rspec's full
  descriptions, and minitest's `Class#test_name`. Until now only Go, Python,
  PHP and Rust critics got the list; in these three languages a critic could
  skip tests or name ones that do not exist. A file whose tests cannot all be
  named exactly gets no list, as before: tests generated with `.each`, a loop
  or `define_method`, shared examples, a title built at run time, a test
  under a braceless `if`, a test function renamed on import
  (`import { test as check }`, or `node:test` imported or required under
  another name), and JSX text with an apostrophe in a React test file. The
  rename case was found by an outside review, which showed node running a
  test the list left out. The names were checked against what real jest, vitest, mocha, node,
  rspec and minitest list for captured fixtures, and three real audits
  (jest, mocha, rspec) handed their critics the list and got all four tests
  judged. Cached verdicts are re-measured once (VerdictGeneration 33).
- **Every 95% interval is now over distinct edits.** The certification gate
  already took its interval over distinct measured edits, but the interval
  printed by `certify --local` and `--repo`, signed into the audit statement
  and shown on the public record page was taken over every graded mutant,
  duplicates included, so eight copies of one edit printed as n=8 with a
  band far narrower than one observation supports. The verdict now carries
  the distinct sample (`mutants_distinct`, `mutants_distinct_survived`, also
  new nullable ledger and warehouse columns), and every interval uses it,
  printed as `(n=3 distinct)`. Ledger entries written before this keep their
  band, labelled "duplicates counted". Found by a claims review of the docs.
- **jest and mocha are recognized when run by their entry script.** The
  jail form the docs give, `node ./node_modules/jest/bin/jest.js` (or
  mocha's `bin/mocha.js` / `bin/_mocha`), was read as node's own test
  runner, so tests were never listed, no killing test was named, and the
  critic's claims went unchecked. It now gets the jest or mocha row, and its
  list commands were run against real jest 30.5 and mocha 11.8.
- **`scans show --evidence` and `corral brief` now say when the proven tests
  fail together.** Both hand back the stored authored test, and neither
  read the recorded result of running the tests together, so a test file
  the live report had flagged could be adopted later with no warning. Both
  now print the same warning and runner output as the live report. Found by
  an outside review.
- **`corral review --substrate workspace` runs each reproduction from a
  fresh copy of the commit.** One worktree served the whole review, and a
  script's own edits were never undone, so a later finding, or the
  verifier's refutation, could pass or fail on what an earlier script left
  behind instead of on the reviewed commit. Each script now gets its own
  worktree, which is removed afterward. The default sandbox already worked
  this way. Found by an outside review.
- **A critic whose model call fails no longer ends the audit.** The critic
  is advisory and never decides a verdict, yet a failed critic call stopped
  the run as COULD-NOT-GRADE, discarding mutants already scored. The audit
  now continues, and the verdict marks the critic review INCOMPLETE rather
  than clean. Found auditing Warp with an `antigravity` critic.
- **Proven tests are now also run together.** Each authored test is proven
  alone, so tests that each pass alone could still break each other in one
  suite. On Warp's `secret_redaction`, ten proven tests swapped one global
  regex and one failed when the file ran in parallel. A per-survivor run with
  two or more proven tests now runs the merged file once on the unmutated
  code. The verdict records `authored_together` (`pass`, `fail`, or absent
  when not checked), and both `certify --local` and `--repo` print a warning
  with the runner's output when it fails. Counts and status are unchanged.
- **Public claims now match what the record guarantees.** The docs and
  site now say that ledger entries are signed only when a key is present,
  that the key holder can re-sign the whole chain, that dropping the
  newest entries leaves a valid chain, and that `ledger verify` reports an
  unsigned entry among signed ones as a note; only an external anchor
  detects those. Corral attempts a test for each survivor and keeps the ones
  that prove, rather than writing one for every bug, and a fresh run can
  miss a gap an earlier run found. Prior work that plants faults and writes
  tests to kill them (Meta's ACH, LLMutantKiller) is cited, and dated field
  notes carry visible corrections. `corral ledger`'s help says the same.
- **One-time cache miss.** `reposcan.VerdictGeneration` is now 33 (rc.24
  shipped 28). Every verdict cached before this change is re-measured once.

## [v1.0.0-rc.24] — 2026-10-17

- **A verdict could certify on less evidence than its exam-size rule
  allows. Fixed.** Two ways, both found by an outside review and reproduced
  with real shell runs. First, a mutant whose grading command fails on the
  unmutated code is *unmeasured*, neither killed nor survived, but the
  verdict still counted it as graded: one measured kill beside seven
  unmeasured mutants printed "killed 8 of 8" and certified. Unmeasured
  mutants are now left out of `MutantsTotal`, counted in a new
  `mutants_unmeasured` field, and printed on the verdict. Second, duplicate
  mutants (byte-identical edits, graded once) still count toward the kill
  rate as before, but the interval that decides whether an exam is big
  enough to certify is now taken over *distinct* edits: eight copies of one
  fault are one observation and can no longer certify. `VerdictGeneration`
  is 28, so cached verdicts are re-measured once.
- **`corral review churn`: is the review loop draining the code or churning
  it?** For every review on the ledger, each finding's line is blamed at the
  reviewed commit. It is **drain** if the line was there when the previous
  review of its file looked, **churn** if it was written since, and a **first
  look** if no earlier review covered the file. Findings that fell (refuted by
  a person, or by execution) are not counted. This is the hand count from the
  "For every season, churn" field note, now a command: no model runs, nothing
  is written. One approximation, stated in its output: churn counts every
  line written since the previous review, not only the commits that fixed its
  findings, because the record does not say which those were. On corral's own
  ledger it reproduces the field note's churn column for the September gate
  and transparency rounds (5 and 5) and its drain within one finding each.
- **`corral review` shows an API seat the commit it reviews, not your
  working copy.** The entry is recorded against HEAD and its reproductions
  run on HEAD, but an API reviewer and verifier were shown the files as
  they stood in your checkout, uncommitted edits and untracked files
  included. A finding about uncommitted code was then graded against
  different bytes and demoted. Both seats now read the same detached
  worktree an agentic seat already did. Found by an outside review.
- **`corral review` runs its reproductions in the sandbox.** A reproduction
  is a model's shell script, and it ran in a plain git worktree on your
  machine, with the network on and only model keys removed from its
  environment — the one place corral ran model-written code outside the
  sandbox `certify` grades in. It now runs there by default: offline, a fresh
  copy of the tree at the reviewed commit per script, your installed
  dependency directories bound read-only, Go and Rust dependencies vendored
  as `certify` vendors them. With no sandbox on the machine, review (and
  `review recheck`) refuses before calling a model. `--substrate workspace`
  keeps the old worktree, for where the caller is the isolation boundary; the
  GitHub Action passes it, as it already does for the audit, and the review
  entry records which one ran. A script that needs the network, git history
  or a binary file outside `testdata` now fails, and its finding is demoted.
  The sandbox is prepared before any model is paid, so a repository it
  cannot seed costs nothing. The review entry records which dependency
  directories were mounted, and says which ones this machine's sandbox could
  not mount. The toolchain mounted is the scope language's own, never one a
  script names. A script killed by a signal (the sandbox reports it as exit
  128+N) is recorded as not run, in a review and in `review recheck` alike, so
  a crash is never read as a result. `review recheck` of a finding reproduced
  in a worktree (every entry before this, and every one the Action writes)
  does not read a failure in the sandbox as a fix, since the sandbox has no
  `.git`, network or binaries; it says to recheck with `--substrate workspace`.
- **Fixed: a dependency directory could mount a path outside itself.** An
  entry of `node_modules` (or any bound dependency directory) that is a
  symlink leading out of it was mounted into the sandbox at its target, for
  `certify` as well as `review`. Such an entry is now skipped; a link that
  stays inside, as pnpm's do, still mounts.

- **The killing test is named, and a partly failing authored test is
  salvaged, in every language but Rust.** jest, vitest, mocha, `node --test`,
  rspec and minitest now record which test killed each mutant
  (`killed_by`), read from the runner's own default output. And when a
  test corral writes fails some of its cases on the unmutated code, those
  eight runners (the six plus PHPUnit and `go test`) rerun it without the
  failing cases and keep what the rest prove, as pytest already did, instead
  of discarding the whole file. Each skips in its own way: `-t` with a
  negative lookahead (jest, vitest), `--grep --invert` (mocha),
  `--test-skip-pattern` (node 22.1 or later), `-E` (rspec-core 3.12 or
  later), `--exclude` (minitest, only for `ruby <file>`), `--exclude-filter`
  (PHPUnit 10 or later), `-skip` (Go 1.20 or later). An older runner rejects
  the flag, so nothing is salvaged; a skip can drop more than the failing
  cases (jest's `-t` ignores case; a failing Go subtest skips its parent;
  a failing PHPUnit data set skips its method) but can never keep a failing
  one in a passing rerun. A skip can also run MORE than the original
  command did: rspec's `-E` replaces the project's tag exclusions
  (`filter_run_excluding :slow` specs run again), and a command-line
  `--grep` or `-t` replaces one set in a config file. So every mutant a
  salvage kills is graded once more under the same command with the
  project's own tests alone, and a kill they make there is not credited to
  the authored test. Each was run against the real runner
  (`scripts/capture-runner-fixtures.sh`); `node --test` was also run on
  node 24, which prints its spec reporter where node 22 prints TAP, and
  both are read. The salvage rerun is also built the
  way a one-test run is, so it goes through `npm test`'s `--` and is refused
  where the operator's command already filters by name; a pytest id
  holding shell syntax is now quoted rather than handed to the shell. Not
  covered: Rust (Phase 5), a test title holding the runner's own separator
  (` › ` in jest, ` > ` in vitest), whose failure is then mislabelled and
  not skipped, and a reporter other than the runner's default (for node,
  other than TAP or spec).
- **Fixed: a one-test `node --test` run could run every test.** node reads
  its options only before the first file, and the flag that picks one test
  was placed after the operator's paths, so with `node --test tests/` it was
  ignored and every test ran; a kill was then credited to the one test a
  critic named. The flags now go right after `--test`.

- **A critic's "this test can never fail" claim is checked by execution on
  every run, and a refuted one leaves the count.** The named test runs alone
  against the run's mutants; if it kills one, the claim is false, and the
  verdict now records it under `refuted_findings` instead of counting it with
  the claims that stand. This check used to run only when a critic-score
  store was attached, which `certify --repo` (and so the GitHub Action) never
  does, so every claim reached the record unchecked. On Warp's
  `crates/fuzzy_match` a claude-haiku-4-5 critic called two tests vacuous
  that fail the moment the guard clause they cover is deleted.
- **Rust can run one test alone.** libtest gets `-- --exact <path>` and
  nextest `-E 'test(=<path>)'`, built from the operator's own command, so a
  package filter and the runner survive. The test's full path comes from the
  runner's own list (`-- --list`, or `cargo nextest list`), and a critic's
  name is mapped to the one listed test it names; a name that matches more
  than one (the same test name in two modules) is left unchecked, since
  running it would run the other test too. A nextest command carrying its own
  filterset is not narrowed, for the same reason.
- **Test runners are one table, and what each language supports is a
  generated page.** Everything corral knows about a runner — its fail-fast
  flag, how it lists tests, how it runs one, how it names a failure — is now
  one row per runner in `internal/lang/runner.go`, instead of a method per
  language that re-detected the runner each time (and, for jest, mocha and
  vitest, was written once for JavaScript and again for TypeScript). Nothing
  a run does changes, with two exceptions: a `python -m unittest` command no
  longer gets pytest's `--deselect` when an authored test is salvaged
  (unittest exits 2 on an option it does not know, so that rerun could never
  pass), and Rust says why `--matrix` is unavailable (its test ids are
  resolved through the runner's list, which is the whole crate rather than
  the test file; the critic still checks claims through that list). The new
  [language-support page](docs/corral/language-support.md) is rendered from
  the same table, so it cannot claim what the code does not do, and every gap
  it shows carries its reason.
- **A critic's claim is checked by execution in JavaScript, TypeScript, Ruby
  and PHP too.** jest, vitest, mocha, `node --test`, rspec and PHPUnit can
  each list a test file's tests and run one alone, built from the operator's
  own command. Until now a "this test can
  never fail" claim in those languages reached the record unchecked. A test
  is run alone only when nothing else would run with it: these runners pick
  a test by its describe/it titles joined by spaces, so `Calc > adds` and a
  top-level test named `Calc adds` are both left unchecked, and so is any
  test whose name node also matches as a leaf or suite name (jest also
  matches without regard to case). A test the runner reports but corral
  cannot run alone, such as a node test with subtests, still makes a name
  ambiguous. Each runner was
  run against a fixture suite (jest 30.5, vitest 3.2, mocha 11.8, node 22,
  rspec-core 3.13, PHPUnit 11.5;
  `scripts/capture-runner-fixtures.sh`). node has no collect-only mode, so
  listing for `node --test` runs the test file once, on unmutated code.
  Not covered: minitest (no list mode), commands that hide the runner behind
  a script such as `npm test` or `composer test`, a shell-wrapped command
  (so Ruby's stock command, which is one, still does not get this), and
  vitest majors other than 3. `--matrix` stays off for these runners, as for
  Rust: their list is what the command runs, which a directory argument or a
  `.mocharc` widens beyond the test file, so a matrix would offer other
  files' tests for deletion.
- **`npm test` is read through its script, and `--runner` names a runner
  corral cannot read.** A test command that runs a package script (`npm
  test`, `yarn test`, `pnpm test`, `composer test`, `npm run <name>`) is
  read statically from `package.json` or `composer.json`, once at run start,
  and the run says what it found (`npm test → jest (package.json
  scripts.test)`). When the script is one command, the runner gets its own
  arguments through the launcher: fail-fast, the test list and one-test
  runs, so the critic's claims are checked. A compound script (`jest &&
  eslint .`) is detected but gets no arguments, because npm and Composer
  append them to the end of the script, where `--bail` would reach eslint.
  Each launcher's separator was run: npm 10.9 and pnpm 9.15 after `--`,
  yarn 1.22 directly, Composer 2.9 after `--`. `certify --runner <id>`
  overrides detection (ids are on the language-support page); a typo, or a
  runner of another language, is refused before anything is spent, and
  corral still runs the command as given. The coverage wrappers now take
  their runner words from the runner table. A script corral did not read (no
  manifest, a Composer array of several commands, a workspace or
  `--working-dir` flag pointing at another package) or one naming a tool it
  does not know (`pest`, `php artisan test`) gets no arguments, and the run
  says so. jest through npm, yarn and pnpm and PHPUnit through Composer were
  each driven end to end (list, resolve, one test run). Not covered: yarn 2
  and later, vitest through a script (its list rewrites the subcommand),
  a script that calls another script, Composer scripts starting with `@`,
  Makefiles, and a `runner` input on the GitHub Action.
- **One-time cache miss.** `reposcan.VerdictGeneration` is now 28 (rc.23
  shipped 23). Every verdict cached before this change is re-measured once.

## [v1.0.0-rc.23] — 2026-10-15

- **PHP's test critic must judge every test in the file.** It used to get no
  list of the file's tests, so nothing checked that its answer covered them
  all. Replayed four times on webmozart/assert's `tests/AssertTest.php` (14
  tests), a claude-haiku-4-5 critic judged 5, 1, 6 and 0 of them, filed
  findings against data-provider cases it named as tests, and on the empty
  answer fell back to the tool loop (8 calls instead of 1). The critic is
  now handed PHPUnit's tests by name (`Class::method`: public `test*`
  methods and those marked `#[Test]` or `@test`), as Go and Python have
  been, and an answer that skips or invents one is refused. Checked against
  a lister built on PHP's own tokenizer over the test files of PHPUnit,
  symfony/console and laravel/framework: 2,512 files, 18,459 tests, no
  difference.
- **The run output says which path the critic took.** A line per file now
  reads `test-critic: typed critic judged N test(s), M flagged`, or names
  the fallback to the tool loop and the parse errors that caused it. That
  text was computed and read only for its "incomplete" marker, so a critic
  that made seven calls instead of one could not say why.
- **Rust's test critic must judge every test in the file, too.** The critic
  is now handed libtest's tests by their module path within the file
  (`tests::name`, or `name` at the top of a `#[path]` test file): functions
  at module level marked `#[test]`, `#[tokio::test]` or any attribute whose
  path ends in `test`, `#[rstest]`, `#[test_case]` or
  `#[wasm_bindgen_test]`, including those inside a `proptest!` block. That is
  a suffix of libtest's own path, which `cargo test` matches by substring.
  Checked against a lister built on tree-sitter's Rust grammar over Warp,
  django-bolt and two small crates: 4,175 files, 16,162 tests, the same
  list everywhere except 25 `proptest!` tests that tree-sitter cannot see
  inside a macro and this lister does.
- **One-time cache miss.** `reposcan.VerdictGeneration` is now 23 (rc.22
  shipped 21). Every verdict cached before this change is re-measured once.

## [v1.0.0-rc.22] — 2026-10-15

- **PHP grades mutants in parallel on `--substrate workspace`.** Two things
  kept a Composer project at one mutant at a time. A private tree linked
  only top-level dependency directories, so a PHPUnit installed under
  `tools/phpunit/vendor` was missing from every tree and the baseline failed
  in all of them; every git-ignored dependency directory is now linked at
  any depth (a monorepo's `packages/*/node_modules` has the same shape).
  Then, because PHP resolves `__DIR__` through a symlink, Composer's
  autoloader in a linked `vendor/` located the project in the ORIGINAL
  checkout, so a tree never ran its own mutant — the pool's canary caught it
  and refused to parallelise. `vendor/autoload.php` and `vendor/composer/`
  are now copied into each tree, and the rest of `vendor/` stays linked. On
  webmozart/assert's `src/Assert.php`: 6 trees instead of 1, and the dev pass
  over 40 mutants took 8s instead of 37s.

- **PHP: the writer can prove a survivor in a class that depends on
  another file.** The PHP compile check loaded the file without Composer's
  autoloader, so any class using a trait, parent or interface from another
  file failed it unmodified. The mutant side already switched such a check
  off; the writer's side did not, and refused every test it wrote as "does
  not compile". On webmozart/assert's `src/Assert.php` that made the one
  survivor unprovable. The check now loads `vendor/autoload.php` when the
  project has one, and a compile check the unmodified source fails no longer
  rejects an authored test (the test's own checks still do). Re-run on the
  same file: the survivor was proven on the writer's first attempt, by a
  test calling `Assert::notNegativeInteger(-1)` — the suite tests `-123`,
  never `-1`.

- **`--max-tokens` holds under parallel calls.** The cap was checked before
  each call and charged after it, so every call that started before the
  first one finished was admitted at a spend of zero. The test-writer sends
  one call per survivor at once, and `--max-tokens 1` on a Warp replay spent
  97.8k tokens over 8 calls. A call is now admitted only while the spend,
  plus every call in flight sized at the largest call the run has measured,
  leaves room for one more; otherwise it waits for room or, past the cap, is
  refused without dialling. Until one call has been measured, one call runs
  at a time, so a capped run's first parallel batch waits for its first
  call. The overshoot is now at most about one call, as documented. The cap
  line reports the run's real totals (`REACHED — N call(s) made, M tokens
  spent`); it said "after 1 call(s)" beside the tokens of eight.

## [v1.0.0-rc.21] — 2026-10-14

- **A compile check never costs a file its grade.** The check that a mutant
  still builds is first run on the unmutated file. That run used to be held
  to the per-mutant test timeout, and any failure made the whole file
  COULD-NOT-GRADE: on Warp, before rc.20 scoped Rust's check to one crate, it
  died 54 seconds in. The first run now gets the file's whole budget and is
  timed. If it cannot run, the gate is switched off for that file and every
  mutant is graded by execution. Each mutant's check is capped at three times
  the measured time, never below the test cap; a mutant whose build runs
  past that is invalid, with the reason. A check that fails to run on one
  mutant grades that mutant by execution and is counted. Only the run's own
  `--timeout` or an interrupt still stops it here. When the gate is off, or
  skipped some mutants, the run's progress output now says so; before, that
  note was computed and never printed. It is not yet part of the signed
  verdict.
- **The ledger-branch recipe's attestation step works.** The workflow in
  `docs/corral/github-action.md` used `actions/attest` with no predicate,
  which it refuses, over `scans/*.json`, which matches no entry (entries are
  `.json.gz`). It is now `actions/attest-build-provenance` over
  `scans/*.json.gz`, and the example names the permissions the job needs
  (`contents: write`, `id-token: write`, `attestations: write`). Found by
  running the recipe in a fork of Warp.

## [v1.0.0-rc.20] — 2026-10-14

- **Rust.** A Rust source whose test module is declared in a separate file
  (`#[path = "foo_tests.rs"] mod tests;`, or `mod tests;` and rustc's default
  file) can be audited by `certify --local --repo-dir` and `certify --repo`,
  with `cargo test` or `cargo nextest run`, on a stock rustup install under
  `--substrate workspace`. Measured on Warp's `crates/fuzzy_match` at
  714c51f1, from a clean Ubuntu container with rustup in `$HOME` and
  Gemini 3.8 Flash seats: 37 mutants graded, kill rate 0.14 (95% interval
  0.06–0.28), 29 survivors proven missing tests, 0.4M input tokens, 10m42s.
  The entries below say what is and is not covered.
- **`certify --repo --path <dir>` audits one part of a repository.** The
  checkout stays whole, so tests elsewhere still pair and the build still
  sees the root's files (a Cargo workspace manifest, a monorepo's
  `package.json`); candidates, exclusions, counts and the `--diff-base`
  change list are cut to the directory. A path that is absolute, outside the
  repository or not a directory is refused. The scope is recorded in the
  ledger entry, in a new `corral_scans.scope` warehouse column, and on the
  record page.
- **The Action takes a `path` input**, passed as `--path`.
- **A dry run names the files it would audit**, in rank order, each beside
  the test it is graded against. It said "jobs below" and listed only the
  exclusions; the jobs reached `--json` alone.
- **Rust's compile gate builds the crate the suite grades.** It was
  `cargo test --no-run` with no package, which from a workspace root built
  every member and could run past the per-mutant timeout before one mutant
  was scored. It now goes through the same crate scoping as the test command.
- **The record page shows audits, not just reviews.** Each scan entry on
  corralai.dev/ledger has its own page: the kill rate with its interval, the
  survivors as diffs, the proven gaps and where corral's test lands.

- **Agent seats in every seat, not only `review`.** A coding-agent CLI
  (`claude-code`, `codex`, `antigravity`, or one you define as
  `CORRALAI_AGENT_<NAME>`) can now fill any CLI seat: `certify --local` and
  `--repo` (`--writer-model`, `--mutant-model`, `--critic-model`,
  `--derive-model`), `demo`, `doctor`, goal derivation and the shadow seats,
  through one resolver, `agentbackend.ForSeat`. A seat runs on the agent's own
  login, so on a subscription it needs no API key. Run on 2026-10-13 with no
  API keys in the environment: a `demo` on `codex` and `claude-code` (5 of 11
  mutants killed by the dev suite, 6 of 6 survivors proved) and a real
  `review` round. `antigravity` was not measured. How to define your own agent,
  and what was measured, is in `docs/corral/agent-seats.md`.
- **Changed behaviour: review's built-in `claude-code` seat now starts cold.**
  It no longer sees your own `CLAUDE.md`, memory, skills or MCP servers; a
  one-line prompt measured about 94-108k input tokens without the cold flags
  and 561 with them. If a review relied on the seat knowing your conventions,
  put them in the brief or the repository. The `codex` seat now passes
  `--ignore-user-config --ignore-rules` explicitly; nothing measured shows it
  loaded your setup before.
- **A seat that reports no tokens is `not metered`, not free.** An agent seat
  on the `text` wire shows `not metered` on the cost line, and `--max-tokens`
  refuses it, because a cap cannot bound what it cannot see. The warehouse and
  ledger scan token totals are NULL (omitted from the ledger) when nothing in
  the scan was metered.
- **`--max-tokens` now also counts goal derivation.** A file whose derivation
  was stopped by the cap is excluded as `derive-budget-exhausted`.
- **`models rank` and the scorecard exclude unpinned agent seats.** A seat
  named `codex` rather than `codex:<model>` records a tool, not a model, and
  is no longer counted as evidence about one. Review seats included: they used
  to be counted.
- **A missing agent CLI is refused up front, by every verb.** `certify`,
  `review`, goal derivation and `demo` check that a seat's CLI is on `PATH`
  when they resolve it, before any jail, store or model call. Before, only
  `doctor` checked, and `certify` ran its jail first and then filed the
  missing writer as `writer-failed`.
- **The Action no longer refuses agent seats by name.** Its review step
  refused `claude-code` and `codex` and missed `antigravity` and every agent
  you define. It now keeps no list, and two new optional inputs pass a login
  through: `claude-code-oauth-token` (as `CLAUDE_CODE_OAUTH_TOKEN`) and
  `codex-api-key` (as `CODEX_API_KEY`). The CLI must be installed on the
  runner. Neither input has been run on a runner yet, and `codex-api-key` in
  particular is not yet run: the variable name comes from the codex binary's
  own error text, not from its `--help`.
- **An agent seat inherits corral's environment.** An API key you export for
  corral's API seats also reaches the agent CLI, and a CLI that prefers an API
  key over its subscription login will bill the key. Unset it for an
  agent-only run. `codex`'s read-only sandbox still lets it read the host, and
  a certify prompt carries the audited source; see
  `docs/corral/agent-seats.md`, "What a seat can reach".
- **`antigravity` in certify runs without `--dangerously-skip-permissions`.**
  The flag is now added only when the caller grants tools (review does). Not
  yet run in certify.
- **Smaller fixes.** certify and review now compare seats the same way,
  ignoring case. A pin starting with `-` is refused. A timed-out agent call
  kills the whole process group, and Ctrl-C (or SIGTERM) kills every live
  agent seat's group and starts no new agent call, in every verb. The
  `--version` probe has a 10s bound. `review` records the verifier's tokens
  when its reply does not parse. `certify` says when an operator agent's
  vendor cannot be told. The public warehouse's `bug_catches` extract leaves
  out unpinned agent seats.
- **An interrupted run records nothing**, for every seat type. Before,
  `certify --repo` stamped the stopped file `executor-error` and still wrote
  its ledger entry, attestation and push, and an interrupted `certify` could
  bank what it had measured as a timed-out verdict. Now `certify --repo`,
  `certify --local` and `review` write no ledger entry, verdict, attestation,
  `--record-mutants` set or push. They label nothing, and exit **130** (new,
  the shell's Ctrl-C status) with `interrupted — nothing recorded`.
  `--record-stream` is the exception: it streams events as they happen, by
  design. A `certify --local` verdict already signed when the signal lands is
  reported as recorded. A review interrupted between its ledger entry and its
  push says the entry was written and not pushed. A process started with
  SIGINT ignored keeps ignoring it.
- **One-time cache miss.** `reposcan.VerdictGeneration` is now 21 (it was 17
  earlier in this release; 19 and 20 were skipped). Every verdict cached before this change is
  re-measured once.

- **Breaking: corral's files move out of `~/.claude`.** corral no longer
  creates or reads that directory. Databases and signing keys live in the
  state directory (`$XDG_STATE_HOME/corral`, else `~/.local/state/corral`,
  or `~/Library/Application Support/corral` on macOS; `CORRALAI_STATE_DIR`
  moves it), and the verdict cache in the cache directory
  (`CORRALAI_CACHE_DIR`). Every per-file `CORRALAI_*_DB` / `_FILE` variable
  keeps its name and still wins. `corral state paths` prints every file, its
  override variable and where it resolves.

  **Run `corral state migrate` once**, with the daemon stopped, and
  `--dry-run` first (`--from DIR` names a different source). A store whose
  only copy is still in `~/.claude` is refused, not recreated: otherwise the
  first run would mint a new certify key, and every entry signed after it
  would fail against your `LEDGER_PUBKEY`. Setting `CORRALAI_STATE_DIR` to
  the old directory uses the files in place instead. Migrate moves each
  file with its `-wal`, `-shm` and `.wal` side files (side files first, the
  database last), refuses on any conflict without touching anything, refuses
  a symlinked source, a running daemon (and a file whose lock it cannot
  probe), and a source owned by another user, and prints each key's public
  key before and after the move, exiting 1 if a moved key does not match. A
  side file already at the destination while its database is still at the
  source is a conflict, because it may be stale; after a migrate that stopped
  partway, re-run it with `--resume` to adopt such files (each is listed), and
  do not start the daemon until a migrate completes. A `.migrate-*` temporary
  file left in the destination by a crash is reported as a conflict for you
  to remove. The refusal
  and the migrate command are planned for removal in a later release (the
  first release after 1.0 is proposed, not decided).

  `certify --local` now resolves the bug-catch store even when no shadow
  pool is configured, so it can hit the same refusal. The TLS autocert cache
  and the repo workspace directory were left where they were.

  Memory: with no `CORRALAI_MEMORY_DIR`, entries are written to and indexed
  from `$STATE/memory`. Before, they were written to
  `~/.claude/projects/default/memory` and never indexed; those entries are
  not migrated, so copy that directory by hand if it holds anything you
  want.

- **`certify --repo` now recognises `.rs` files and reports the ones it
  cannot audit yet.** A Rust file whose tests live inside it (an inline
  `mod tests { … }`), or that declares no test module, cannot be audited: a
  test for it would be written over the file itself. Each such `.rs` source
  is listed as `test-landing-not-built`, never as a candidate, and the language profile shows it as
  `N not auditable yet (test landing not built)`. It never takes a `--top`
  slot, never counts as a second language for the selection and coverage
  pre-flight passes (a Python repository with a Rust extension keeps its
  Python test selection), and never makes corral prepare a sandbox or
  download Rust dependencies.
- **On `--diff-base`, a changed `.rs` file does not fail the gate.** It is
  treated as a changed file in a language corral does not audit, and is
  named in the scan output:
  `changed but not audited: <path> (test-landing-not-built — …)`. A pull
  request that changes only such Rust code reports NOTHING IN SCOPE and
  exits 0 — that change was not audited, and the line above says so.
- **The `.rs` refusal is narrowed to files whose test would land on
  themselves.** A Rust source whose own declaration loads a separate test
  file — `#[cfg(test)] #[path = "foo_tests.rs"] mod tests;`, or a bare
  `mod tests;` and rustc's default module file — is no longer refused by
  `certify --local --repo-dir`, `certify --repo` or `corral doctor`.
  corral's authored test is appended to that test file as a
  `#[cfg(test)] mod corral_tests { use super::super::*; … }` block (named
  `corral_tests_2`, … if the file already has one); the audited source and
  the project's own tests are left as they are, and on
  `--substrate workspace` the write into the real test file is journaled
  and restored like every other. `certify --adversarial`, the brain and
  `certify --local` without `--repo-dir` still refuse every `.rs` file:
  they grade single-file and have no repository to read the declaration
  from, and the refusal now says so; `corral doctor` passes such a file
  only with a note that it needs `--repo-dir`. This is a narrowed refusal,
  not Rust support.
- **In a jail, a Rust audit runs the system-wide Rust toolchain, not
  yours.** Under the bwrap jail and `--jail container`, `RUSTUP_HOME` is
  pinned to `/usr/local/rustup` and `CARGO_HOME` to a writable directory
  inside the jail, and graded runs set `CARGO_NET_OFFLINE=true` (the
  `cargo vendor` step, which needs the network, does not). So a jailed Rust
  audit needs a system-wide install —
  `RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo rustup-init
  --no-modify-path --default-toolchain <version>`, with `/usr/local/cargo/bin`
  on `PATH` (plus `cargo install --locked cargo-nextest` for nextest) — and
  a `cargo` that is a rustup proxy under your home is refused before any
  spend when that install is missing. `certify --repo --substrate workspace`
  builds no jail and runs your own toolchain (`~/.cargo` is fine; see the
  next entry). The macOS sandbox does not apply these pins.
- **A requirement of the jail is checked only where a jail runs.** A
  language's toolchain check is now two halves: what every run needs (the
  test command's binary on `PATH`) and what only the jail needs. `certify
  --local`, `certify --repo` on the jail substrate, the brain and `corral
  doctor`'s sandbox check (when `--code` names the language) ask both;
  `certify --repo --substrate workspace` asks only the first. So:
  - a stock rustup install (`~/.cargo`, no sudo) is no longer refused on
    `--substrate workspace`; a jailed run's refusal now also names that
    substrate as the alternative;
  - a PHP run on `--substrate workspace` no longer probes the interpreter
    inside a sandbox, so it is no longer refused because the sandbox cannot
    follow `/etc/alternatives` (a run on the host never needs to), and no
    longer starts a sandbox at all just to check;
  - `corral doctor --code <file>` reports the jail-only refusal a jailed
    run would hit, instead of an inconclusive `--version` probe.
- **The tests a baseline passed only on a retry are recorded, not only
  printed.** `certify --repo` measured them twice — in the baseline
  stability runs and in the audit's own dev pass — and printed one list
  while the verdict kept the other. They are now one list on the verdict
  (de-duplicated), which the scan line prints and which reaches the ledger
  entry and a new warehouse column, `baseline_flaky` (one test per line),
  with or without `--push-source`.
- **The workspace pool's downgrade note no longer blames the suite.** When
  the baseline fails in several private trees at once, the note used to say
  "suite is not concurrency-safe". It may instead be that the copies lack
  something the suite needs — on a Rust crate, a Cargo workspace root
  outside `--repo` — so the note now says `baseline failed in N private
  trees at once (the suite may not be concurrency-safe, or the copies lack
  something it needs — …)`, followed by the run's own output as before.
- **Rust runs say how they differ from the project's own runner.** With a
  `.config/nextest.toml` in the repository and a `cargo test` command,
  `certify --local` and `certify --repo` say the project's nextest profiles
  (retries, timeouts, test groups) do not apply. Under `cargo nextest run`
  they say a test passed only on a retry (FLAKY) is graded as passed. Both
  name such tests from a file's unmutated baseline (`certify --repo` from
  its baseline stability runs and the audit's dev pass together, once); a mutant whose run passed only on
  a retry is counted as survived and is NOT named, and the disclosure says
  so. `nextest.toml` is a harness file, so changing it changes the verdict
  cache key.
- **Where a landed authored test goes is part of the record.** The verdict
  gains `authored_landing` — the file a Rust `corral_tests` block must be
  appended to, the only file it compiles in — and `baseline_flaky`. Both
  are columns in the warehouse and fields in the ledger entry (pushed with
  or without `--push-source`: neither is source), and
  `certify --repo` now prints "append it to <file>" exactly as `certify
  --local` does. Cached verdicts move to a new generation (now 21, after the agent-seat
  change below), so a verdict cached before this change is recomputed once
  rather than served without them.
- **A diff that deletes a Rust source's declared test file reports the
  source as not audited.** That source now lands on itself and is excluded
  as `test-landing-not-built`; the deleted-test check asks those sources
  too, so the deletion is named rather than silent.
- **`certify --local` and `certify --adversarial` print how they paired the
  test, for every language:** a new `  paired by <label>: <path>` line
  (`sibling`, `parallel-tree`, `flat-root`, … or `search`), where only a
  search used to be disclosed. `corral doctor`'s pairing line gains the
  same label in parentheses.
- **`certify --adversarial` and `corral doctor` refuse a `.rs` file up
  front.** `--adversarial` used to read the file and its test and send
  both to the brain, which then refused; it now refuses before reading
  anything. `doctor` used to report such a file `[ok]`; it now says it
  pairs but cannot be audited yet.
- **A source file cannot name a test outside the audit root.** A Rust
  `#[path = "…"]` test-module declaration is followed only while it stays
  inside the audit root: the repository for `certify --repo` and
  `--repo-dir`, but the WORKING DIRECTORY for a `--code` given on the
  command line without `--repo-dir` (absolute or not), checked after resolving symlinks.
  One that leaves it is never read, and the "no test found" message names
  it as refused rather than saying the file declares no test.
- **`go mod vendor` now runs inside the sandbox, never on the host.** When a
  Go module has no `vendor/`, corral vendors a throwaway copy before the
  audit. That step used to run directly on your machine; it now runs in the
  same isolation backend as the audit (bwrap by default), with network
  access for that one step only — the graded runs stay offline. What you
  will notice:
  - It needs a working sandbox. Without one, corral refuses and says to
    vendor the module yourself; it never falls back to the host.
  - It is refused under `--jail container`, which used to work: the
    container's root user can neither read the staging copy nor write
    `vendor/` into it. Commit `vendor/` yourself, or use `--jail bwrap`.
  - It starts from an empty module cache every time, so it downloads every
    dependency; your own `GOPATH`/`GOMODCACHE` are not used.
  - Passed through: the proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`,
    `NO_PROXY`, `ALL_PROXY`, in either case) and Go's module settings
    (`GOPROXY`, `GOSUMDB`, `GONOSUMDB`, `GONOSUMCHECK`, `GOPRIVATE`,
    `GONOPROXY`, `GOINSECURE`, `GOFLAGS`). Your git and netrc credentials
    are not, so a private module that needs them can no longer be vendored
    by corral: vendor it yourself first.
  - On Linux backends `GOTOOLCHAIN` is `local`, as in every graded run: a
    `go.mod` that needs a newer Go than the installed one fails here, with
    Go's own message, instead of downloading a toolchain. The macOS backend
    does not pin it, so there `go` may still download one.
  - What the vendoring step can reach, exactly: on Linux it shares the
    host's network, so services listening on the host's loopback and
    abstract unix sockets are reachable from it; proxy variables are passed
    as they are, including any credential inside a proxy URL; on macOS the
    step may also look up any mach service. Of your home directory it can
    read only its own staging copy and the toolchain it runs. With rustup
    older than 1.28, a repository's `rust-toolchain.toml` can still make
    rustup download and run the toolchain it names during this step
    (inside the sandbox); 1.28 and later refuse.
  - The throwaway copy lives under your user cache directory
    (`~/.cache/corral/vendor` on Linux), not the system temporary directory.
    This fixes a bug: a `go.work` file in the temporary directory — which
    any local user can create — made every Go vendoring step fail with
    "cannot be run in workspace mode".
- **The sandbox no longer exposes a toolchain's secrets.** A toolchain
  installed under `~/.cargo` (rustup, or anything `cargo install`ed) or
  `~/.gem` was mounted whole, read-only, into every sandboxed run — with
  `cargo login`'s `credentials.toml`, registry tokens in `config.toml`,
  private git checkouts and RubyGems' `credentials` inside it, where the
  audited code could read them and print them into the record. Now:
  - a cargo home is mounted as its `bin/` alone, whichever of its programs
    runs the suite. When `bin/` holds rustup, rustup's own home
    (`~/.rustup`, or your `RUSTUP_HOME`) is mounted too, read-only — but the
    jail does not run the toolchains in it: `RUSTUP_HOME` is pinned to the
    system-wide `/usr/local/rustup`, so the proxy in `bin/` dispatches to
    that install (see "In a jail, a Rust audit runs the system-wide Rust
    toolchain" above). This holds whether or not you have run
    `cargo login`. A cargo home is
    exactly what cargo defines: your `CARGO_HOME`, or `~/.cargo` when it is
    unset (followed through a symlink), whatever it holds. Nothing else is
    one, so `/usr/bin`, `/usr/local`, `~/.local/bin`, Debian's
    `/usr/share/cargo`, Homebrew's prefix and its kegs (the `rustup` keg
    included) and a standalone Rust install are mounted as before. The cost:
    a second cargo home that `CARGO_HOME` does not name gets none of this.
    Inside `/usr` (or `/opt/homebrew` on macOS) — an image's
    `/usr/local/cargo` with `CARGO_HOME` unset — **nothing** in it is
    hidden: not `credentials.toml`, not registry tokens in `config.toml`,
    not `git/` checkouts. Anywhere else it is mounted like any other
    toolchain directory, which hides only its credentials file. Set
    `CARGO_HOME` to the cargo home your suite uses;
  - of `~/.gem`, only the directory the runner lives in;
  - your `CARGO_HOME` inside `/usr` (and, on macOS, `/opt/homebrew`) — the
    official Rust container image sets it to `/usr/local/cargo` — is hidden
    in every sandbox except its `bin/`;
  - a `RUSTUP_HOME` that is not a toolchain directory (for example
    `~/.config`), or that is a cargo home, is refused with a message
    instead of mounted.
- **The directory walkers that skip dependency trees now share one
  definition, declared by the language plugins.** `corral review`'s scope
  loading and planning, the repository reader the brain and the gate use,
  the jail's read-only dependency binds, the workspace pool's dependency
  links and `certify --repo`'s scan all read it. `corral review` now also
  skips `venv`, `.bundle`, `site-packages`, `.tox` and `target`, and no
  longer skips a directory named `site` (that was corral's own website,
  applied to every repository). The repository reader now also skips
  `.corral`, `.venv`, `venv`, `.bundle`, `site-packages` and `.tox`. The
  jail now bind-mounts a `site-packages` directory at any depth read-only
  instead of copying it into the seed (under bwrap, and under the
  container backend when the directory is world-readable, the same rule as
  `.venv`), and the workspace pool links a top-level `site-packages` into
  every tree, like `.venv`. What `certify --repo` scans and accounts for
  is unchanged. The jail seed and the vendoring staging copy do not use
  the shared definition: apart from the binds above, neither skips any
  directory but `.git` and cargo's build output (a `target` directory with
  a `Cargo.toml` beside it).

- **The coverage pre-flight runs on mixed-language repositories when the
  `--` command names a language.** Go's coverage instrumentation used to
  claim every command, so a repository with Go and Python files given
  `-- pytest -q` declined the pre-flight as ambiguous; it now claims only
  commands that pass its flags to `go test` (`go test …`, `gotestsum -- …`)
  and the Python run goes ahead. A repository with both JavaScript and
  TypeScript files no longer declines either: both instrument the command
  identically, so it is one run. A Go suite run through `make test` or
  `gotestsum` without `--` was never instrumented (the appended flags do
  not reach `go test`); the pre-flight now declines with a reason instead
  of running a command that could not produce a profile. The same language choice drives test selection,
  which is the default: a repository with Go and Python files given
  `-- pytest -q` used to grade every mutant against the whole suite, and
  now runs Python's per-test selection and evidence-based candidacy
  widening, so its per-file kill rates come from the selected tests
  whenever that selection run succeeds. `--whole-suite` restores the old
  behaviour. (Python is the only language with test selection, so the
  JavaScript-and-TypeScript change affects the pre-flight alone.)
- **Repo-search indexing treats `.cjs` files as JavaScript**, as the
  JavaScript plugin already did when auditing them. Nothing else about which
  files are indexed or audited changes.
- **`certify --adversarial` finds the test `certify --local` finds.** With no
  `--test`, it used to take the language's first naming convention without
  checking the file existed, so on a repository whose tests live elsewhere
  (pallets/itsdangerous: `tests/test_itsdangerous/test_signer.py`) it failed
  reading a sibling that was never there. It now resolves the test the same
  way `--local` does: every convention, then a search of the test
  directories, disclosed as `paired by search:`. When nothing is found, it
  says everywhere it looked.
- **Go 1.26.9, golang.org/x/net 0.60.0.** Standard-library advisories in
  `net/http` (GO-2026-6617, -6613, -6612, fixed in go1.26.9) and the matching
  `x/net` HTTP/2 advisory are reachable from corral's own call paths, so the
  module floor and the Dockerfiles move up. `go install` on an older 1.26
  fetches the newer toolchain automatically unless `GOTOOLCHAIN=local`.
- **Signed build records stop claiming zero.** A build signed by `corral certify`
  or the brain's `report_build` with no duration no longer signs
  `"duration_s": 0` in its execution step (the statement already omitted it),
  and no record signs `startedOn: 0` / `finishedOn: 0` — a claim the build ran
  at the Unix epoch. Unrecorded values are omitted. A step's `ts` stays 0,
  meaning "not recorded": changing it would break the hash of every record
  already signed. A NaN or infinite duration is now an error, not a crash.
- **`--transparency` never creates a signing key.** A `CORRALAI_CERTIFY_KEY_FILE`
  naming a file that does not exist is refused (exit 2, naming the path)
  instead of creating a new key and signing a permanent public log entry with
  it. `corral certify` and `review --attest` still create the configured key
  on a first run, as before.
- **A failed `--transparency` log entry is raised on a runner, with its real
  cause.** Every way the upload can fail now emits a `::warning` annotation,
  like a failed statement write or push, and a signing failure is reported as
  itself rather than as a missing envelope file. If a stale envelope cannot be
  removed, no statement is left at the `--attest` path for the Action to attest.
- **A blank ledger key is refused.** `--pub ' '` or a whitespace-only
  `CORRALAI_LEDGER_PUBKEY` used to read as "no key" (signatures NOT CHECKED),
  and a blank `--pub` fell through to the environment's key. Both are now
  malformed-key errors (exit 2). An empty `CORRALAI_LEDGER_PUBKEY=` is still
  unset.
- **An environment key resolves with no `HOME`.** With no `CORRAL_CREDS_DIR`,
  `XDG_CONFIG_HOME` or `HOME`, the credential store refused to open at all, so
  a key in the environment could not be read. Now only a lookup that reaches
  the file store fails, and it says to set `CORRAL_CREDS_DIR`.
- **`corral ui` pairs each ledger entry with its own signature check.** Checks
  were matched by position across two reads of the directory; they are now
  matched by hash, and an entry the check did not cover is shown as a problem.
  A retracted legacy entry keeps its "signer name is self-reported" caveat.
- **`corral-admin`, `corral-observe` and `corral-agent` stop printing `dev` for an installed build.** Only `corral` and `corral-wrangler` knew to fall back to the module version Go embeds, so `go install …@v1.0.0-rc.19` of the other three still reported `dev`. All five now resolve their version through one function in `internal/buildinfo`, and a test fails if a binary that prints a version does not.
- **The missing-model message sees keys saved with `corral secret`.** The
  "credentials visible here" line probed environment variables only, so a key
  stored in the keyring or the encrypted file read as "none" to the person who
  had just saved it. It now resolves through the same store a run uses, from one
  provider table in `internal/creds` (which also gained `GOOGLE_API_KEY` in the
  env scrubs). It prints env-var names and vendors, never a value.
- **`corral matrix -h` prints usage without a brain, and `-h` exits 0 everywhere in `corral`.**
  `matrix -h` read `CORRAL_BRAIN` first, so with no brain it printed the
  missing-brain error, and the generated CLI reference published that error as
  matrix's flags. Every other subcommand that answered `-h` with exit 2 (or 1,
  for `secret`, `control` and `sign-console-bundle`) now exits 0, and
  `scripts/gen-cli-docs.sh` refuses any `-h` that exits non-zero.
  `corral-wrangler register -h` still exits 2. `corral matrix list help` and
  `corral matrix list -- -h` no longer crash: one help predicate now decides it.
- **`corral secret set|get|rm|list -h` print usage, and dash-led secret names are
  refused.** `secret list -h` used to list your stored secret names, and
  `set -h` stored a secret called `-h`. Names never begin with `-`, so one that
  does is a mistyped flag and is refused.
- **A `/bin/sh -c` or `dash -c` test command gets the toolchain preflight.** The
  command is checked against the language's stock toolchain (`go`, `node`,
  `ruby`, …), not against whatever the wrapped script itself runs, so a host
  without that toolchain is now refused up front rather than failing inside the
  jail.
- **The public ledger page includes plain `.json` entries, in chain order.** The
  page renderer now reads the ledger directory through `auditpush.ReadLedgerDir`,
  the same reader the CLI verifies with.
- **A credential-store error no longer hides a key that is set.** The
  missing-model message kept only what it had found before the first unreadable
  name; it now probes every provider and says "none found" with the store error,
  not "no provider credential is set".
- **`--attest` no longer leaves a mismatched envelope, and `review --attest`
  creates its directory.** A keyless `--attest` run after a keyed one used to
  leave a DSSE envelope signed for the previous statement beside the new one;
  it is now removed. `review --attest` into a directory that did not exist yet
  failed; it now creates it, as `certify --repo --attest` already did.
- **Breaking: `corral verify --ledger`, `corral ledger verify` and `corral ui` no longer
  verify against the local certify key implicitly.** Pass `--pub` or set
  `CORRALAI_LEDGER_PUBKEY`; with neither, signed entries read as signed,
  unverified, and signatures are not checked. The CLI and the public ledger
  page now resolve the key identically, so the machine's owner and a stranger
  reach the same verdict on the same record. A malformed key is refused (exit
  2), not downgraded. The site renderer no longer falls back to the committed
  `LEDGER_PUBKEY` file on its own; the deploy workflow passes it explicitly.
- **One-time cache miss.** `reposcan.VerdictGeneration` moves from 16 (rc.19)
  to 17. Every verdict cached before this change is re-measured once.
- **A Python test file with an escaped triple quote no longer loses tests
  from the critic's list.** A `\"""` inside a triple-quoted string closed the
  string early in the scanner, so every test defined after it was left off the
  list while the review still read as complete. The scanner now honours the
  backslash. It also lists nothing, instead of the tests found so far, when it
  ends inside a string or an open bracket: the critic then answers in the
  unkeyed shape rather than being held to a list known to be short.
- **A bad API key no longer costs the critic a second large prompt.** Only a
  400, 413 or 422 is read as "this provider will not take this schema" and
  answered with a plain call. A 401, 403 or 408 fails the plain call the same
  way, so it is returned as it is.

## [v1.0.0-rc.19] — 2026-10-07

Fixes from a review of the whole project: the critic's test list, its answer
schema under a rate limit, and a help screen that described the wrong tool.
The documentation was corrected separately, and lives on the site.

- **One-time cache miss.** `reposcan.VerdictGeneration` moves from 15 (rc.18)
  to 16. Every verdict cached before this release is re-measured once.
- **The critic's test list no longer leaves real tests unjudged.** Since rc.17
  the critic must judge exactly the tests listed for it, so a test missing from
  the list could not be flagged at all, while the review still read as
  complete. A project review found three ways that happened, and all three are
  fixed:
  - **Python:** the scanner lost track of a class whose bases span several
    lines, and of methods after a docstring with a line at column 0. It also
    listed `def test_*` lines inside module strings as tests. It now tracks
    strings and open brackets.
  - **Duplicates:** a test defined twice, which pytest collects once, was
    listed twice and flagged twice.
  - **Go:** a test file that imports `testing` under another name listed
    nothing.

  Separately, the critic read its list from the first copy of the list's
  header in its task, which the audited source itself can contain. It now
  reads the last.
- **A rate limit no longer drops the critic's answer schema.** Any provider
  error on a constrained call used to switch the schema off for the rest of
  the review and re-send the prompt without it. Now only a request the
  provider rejected (a 4xx other than 404 or 429) gets a plain call, and only
  for that batch. A rate limit, server error or timeout is returned as it is.
- **`corral -h` describes the audit tool.** It used to open by describing the
  optional daemon. It now leads with a "Start here" block: `demo`, `doctor`,
  `certify --repo . --dry-run`, then a capped `certify --repo`.

## [v1.0.0-rc.18] — 2026-10-06

rc.17 plus one fix, for a regression rc.17 introduced on large test files.

- **One-time cache miss.** `reposcan.VerdictGeneration` moves from 14 (rc.17)
  to 15. Every verdict cached before this release is re-measured once.
- **A test file with more than 20 tests no longer sends the critic to the
  loop.** rc.17's test list put every test's name in one answer schema, and
  providers cap how large such a schema may be: Claude Haiku refused 40 names
  ("compiled grammar is too large") and Gemini refused 90. On a real file
  (flask's `tests/test_basic.py`, about 90 tests) the critic's schema was
  refused, its unconstrained answer missed names, and it fell back to the
  tool loop: 9 calls and about 295k input tokens. The critic now judges such
  a file in batches of at most 20 named tests, each with its own schema and
  retry; on the same file it judged all 90 tests in 5 calls, on Claude Haiku
  and on Gemini. A batch that still will not answer sends the whole file to
  the loop, as before.

## [v1.0.0-rc.17] — 2026-10-06

- **One-time cache miss.** `reposcan.VerdictGeneration` moves from 12 (rc.16)
  to 14 across the two changes below, because a cached verdict's critic count
  may have come from a critic that answered off-shape or skipped tests. Every
  verdict cached before this release is re-measured once.
- **The test-critic asks for its answer shape, not just in its prompt.** The
  typed critic now sends its schema through each provider's own
  constrained-output field: `format` on Ollama, `response_format` on
  OpenAI-compatible endpoints (Gemini, OpenAI, OpenRouter), `text.format` on
  the Responses API, and `output_config.format` on Anthropic. A model that
  honours it cannot answer outside the three verdicts. A provider that
  refuses a constrained request (an older server, a model without the
  feature) gets one plain call instead, so no seat loses its critic. The
  critic stays advisory; no status changes.
- **The test-critic must judge every test, by name (Go and Python).** The
  critic's task now lists the tests the file declares, read from source
  (Go with its own parser, Python with a scanner that emits pytest node ids),
  and the answer schema requires one verdict per listed name. Under a
  provider that honours the schema the critic cannot skip a test; an answer
  that skips one, or names one that is not in the file, is retried and then
  handed to the loop, and the recorded result names what was missing. A
  flagged test's selector and file now come from the list, not from the
  model. Other languages keep the unkeyed answer. Measured on vacuous tests
  planted in seven of corral's own files (`docs/design/critic-bench.md` §8):
  gemini-3.8-flash still caught 63 of 63 with no false flags, on about 38%
  less output; a local qwen3.6:35b-a3b now judges every test but caught 47
  to 49 of 63, against 57 to 61 when it could list only what it flagged, and
  raised more false flags.

## [v1.0.0-rc.16] — 2026-10-06

- **One-time cache miss.** `reposcan.VerdictGeneration` moves from 10 (rc.15)
  to 12: the verdict gained `CriticIncomplete`, and the critic that produces
  a verdict's critic count changed (both below). Every verdict cached before
  this release is re-measured once.
- **The test-critic judges every test in one typed call.** It used to work
  through a tool-calling loop, filing one finding per call and re-sending the
  code and tests on every step. It now returns a verdict for every test
  (`sound`, `vacuous` or `dead_check`, with a reason) in one JSON answer. An
  answer that does not parse is asked for once more; if the second does not
  parse either, the old loop runs, and the critic's recorded result says so.
  Measured with `scripts/criticbench` against answer keys checked by
  execution: on 21 vacuous tests planted in 7 of corral's own test files
  (gemini-3.8-flash, 3 runs each, 63 per mode) the new critic caught 63 and
  the loop 36, neither flagged a test that was not planted, and the new one
  used 3.7 times less input. On small hand-written fixtures it caught 45 of
  45 on gemini-3.8-flash and on a local qwen3.6:35b-a3b, to the loop's 27 and
  40. The critic stays advisory; no status changes.
- **The test-critic's findings are no longer silently capped, and a review
  cut short says so.** The critic's loop read one tool call per reply and
  stopped after six replies, so a suite with more vacuous tests than that
  reported at most six findings, under a canned summary that read as a
  complete review. Every tool call in a reply now counts. A review that still
  runs out of steps before concluding, or a run that times out before the
  critic's findings are read, is marked `critic_incomplete` on the verdict,
  in the `--repo` ledger row and the warehouse, beside the findings count it
  qualifies. The CLI prints the critic line as INCOMPLETE, never as "no
  vacuous tests flagged". The critic stays advisory; no status changes.
- **A repo scan records what goal derivation spent.** The goal-deriver was
  the one seat in `certify --repo` whose token usage corral read past and
  dropped, so a scan's cost left out one call per candidate file, including
  every file the deriver answered NONE for, which is never audited and so
  never appears anywhere else. Each call that reached the provider is now a
  `goal-deriver` row in the scan's model calls, against the file it was asked
  about: in the signed ledger entry, in the warehouse with `--push`, and on
  the end-of-scan `cost:` line. A goal-cache hit makes no call and adds no
  row; a call that failed reported nothing and is not recorded as zero.
- **corral's own ledger can be verified by anyone.** The public key that
  signs the entries on the `corral/ledger` branch is published as
  `LEDGER_PUBKEY` at the repository root (the same key is anchored in Sigstore
  Rekor), so `corral verify --ledger <dir> --pub $(cat LEDGER_PUBKEY)` checks
  every signature without trusting us for the key.

## [v1.0.0-rc.15] — 2026-10-04

Same code as rc.14 plus the release-gate fix below. **rc.14 is tagged and on
the Go proxy but has no GitHub Release**: its publish step reached a path in
the release gate that had never worked, and a re-run replays the workflow
from the tag, so the fix could not reach it. rc.14 installs and is sound;
rc.15 is the one with a Release page.

- **The release gate's "contained in the default branch" path works.** It
  read the branch head through a lookup that always failed and captured the
  API's error body as the commit, so a release whose own commit was red (the
  normal case, between the tag and the pin bump) could never publish.
- **A tag on a commit that was never merged is refused**, even when that
  commit's own CI is green. It used to publish, while the workflow's comment
  promised otherwise.

## [v1.0.0-rc.14] — 2026-10-04

Everything since rc.13: four more rounds of cold review on the merge gate and
the transparency log, each finding an entry on the public `corral/ledger`
branch; challenger seats drawn from a pool; a write mode for `corral ui`; and
an Agent Skill. **Read "Breaking" before upgrading** — three changes can turn
a configured gate or a habitual command into a refusal, and one makes every
cached verdict miss once.

### Breaking

- **Gate policies are one per variable: `CORRALAI_GATE_POLICY_<NAME>`.**
  `CORRALAI_GATE_POLICIES` (several policies in one variable, separated by
  `;`) is retired and **refused** — not parsed and not ignored, because a
  silently ignored gate is no gate. Three successive guards against a `;`
  inside `cmd=` were each defeated by a cold reviewer, each time letting a
  truncated, weaker command post a success; with one policy per variable the
  operating system supplies the separator and the ambiguity is gone. A
  command is now one string from the parser to the jail, so newlines and
  quoting survive. Policies are applied in variable-name order.
  `gate.Policy.CheckCmd` is now a `string` (it was `[]string`), which breaks
  Go code that builds `brain.Options.GatePolicies` directly.
- **Two policies that would answer the same pull request under the same
  status are refused** (both omitting `context=` on one repo, for example).
  The second used to be skipped on every head while the first one's status
  stood in for it. Set `context=` to tell them apart.
- **`corral ui --write` prints its URL and no longer opens a browser unless
  asked: `--open` opts in, and `--no-open` is removed** (an unknown flag
  now). Opening put the launch token on a process command line.
- **The gate's policy parser is stricter** — see "The merge gate", below:
  `net=` must be exactly `true`, `false`, `1` or `0`, and a lowercase field
  assignment at the start of a line in `cmd=` refuses the policy.
- **One-time cache miss**: `reposcan.VerdictGeneration` is now 10 (see
  "Challenger seats"), so every verdict cached before this release is
  re-measured once.

### The merge gate: five findings from a recorded review, fixed

Ledger entry `8be2189163b0`, findings R3–R7, all adjudicated confirmed.

- **`net=` is exactly `true`, `false`, `1` or `0`, or the policy is refused.**
  It used to map every value but `true`/`1` to no-network, silently, so
  `net=yes` produced a gate that failed every network-needing check. Every
  other spelling — `yes`, `on`, an empty value, and also `TRUE`, `True`, `t` —
  is now refused with a reason, as `timeout=` already was. Nothing gains
  network access it did not have: those spellings used to mean *no network*,
  and they are refused rather than reinterpreted. **A policy that used one is
  now off, with a log line naming it.**
- **`cmd=` is found with the same whitespace tolerance as every other field**,
  so `repo=o/r, cmd=true` is a policy again rather than "no cmd=".
- **A field written after `cmd=` is caught in more spellings**: after a comma
  in any case (`,Base=release`), and at the start of a line in lowercase
  (`base=release`). Before, only `,base=` was caught, and the rest were
  swallowed into the command, so the policy gated every base. An uppercase
  shell variable on its own line (`BASE=origin/main`) is still part of the
  command. **A multi-line command with a lowercase `timeout=`, `base=`,
  `net=`, `repo=` or `context=` assignment at the start of a line is now
  refused** — rename the variable.
- **The gate store's one-time key migration is transactional**, and opening a
  store repairs either state an interrupted migration from an older binary can
  leave — one used to disable the gate, the other to lose its dedupe history.
- **`/api/gate/run` answers per check.** Every status's link — the merge
  gate's and the control gate's — now names its check (`&context=`). For a
  merge-gate check the endpoint answers for that one. The control gate's own
  results have no read endpoint yet, so its link now returns 404 — where it
  used to open the merge gate's answer for the head, which could read passed
  while the control gate had failed. Without a context it reports `passed` only
  when every check that has **reported** for the head passed, with a
  `contexts` list of each check's result, pull request and record; it used to
  report whichever check finished last. A check still running has no row and
  is not listed. `record_id` is omitted when several checks answer together
  and for a check that signed nothing.

### Challenger seats can be drawn from a pool

Built and unit-tested; not yet exercised end to end against a real repository.

- **The scorecard's store now records each row's language and whether its
  challenger was drawn from a pool** (`lang` and `shadow_drawn` columns of the
  bug-catching observations; rows from before carry no language). `corral
  scorecard` itself keeps its columns and gains a `test-writer-shadow` line
  wherever a challenger writer sat, so that record is kept apart from the seat
  that gates.
- **`--shadow-pool` and `--shadow-writer-pool`** take a comma-separated pool
  of challenger models. On `certify --local` and `certify --repo` each run
  draws one by Thompson sampling over the scorecard's record; `doctor` takes
  the same flags and only validates them — it never draws. `--shadow-seed`
  replays a recorded draw. The draw touches only the two shadow seats and
  never gates a verdict. The drawn model, the pool and the seed are signed
  into the record (`Verdict.ShadowSelection`, which the `--local` signature
  covers; `shadowSelection` in each `--repo` statement entry), a selection is signed only for a seat
  the signed roster names (a challenger drawn and never dispatched is in
  neither), and a stderr line names each drawn seat. On the `--local`
  signed record, `producedBy` also reads `(non-gating, drawn from a pool of
  N)`. `--repo` draws once per scan from history pooled across languages,
  writes no scorecard rows, and on `--dry-run` validates the pools without
  drawing.
- `doctor` gains `--shadow-writer-model` and now checks credentials for it.
- **One-time cache miss.** `reposcan.VerdictGeneration` moves from 9 to 10
  because the verdict gained a field, so verdicts cached before this change
  are re-measured once.

### The merge gate and the transparency log: four more review rounds

All of these were found by a model reviewing code it did not write and
survived a model from another vendor trying to refute them; several are in
fixes made the day before. Ledger entries carry each one.

- **The gate could post a result for a check it did not run** — a `;` or a
  misplaced field truncating or widening the command, an empty command
  exiting 0, a timeout overflowing to the sandbox's 60-second default. Each
  is refused now, at the door that acts on a policy as well as the one that
  parses it.
- **Verdicts that never reached the forge are tracked.** A failed status post
  used to be either lost or "retried" by re-running and **re-signing** the
  check on every tick; a signed verdict whose post failed is now re-posted
  from the stored row and signs nothing new.
- **Two policies on one repo each keep their own row and status** (the dedupe
  key includes the context, and the store migration that widens it is
  transactional and repairs an interrupted one).
- **`certify verify` refuses a Rekor record whose Signed Entry Timestamp is
  missing.** Without the SET, the log index and integration time a record
  claimed were unauthenticated and could be edited, and verify printed them
  as witnessed. Also: a malformed inclusion proof no longer panics the
  verifier; the logged envelope is bound by its hash and by set-equality of
  its signatures; an empty inclusion proof no longer records
  `anchored=true`.

### Measurement

- **A workspace restore that fails, or cannot be trusted, is no longer a
  pass.** A test command that replaced an overlaid file with a directory, or
  an overlay through a dangling symlink, left mutant bytes behind on
  `pass=true`; both are refused or reported now.
- **Identical edits graded by different commands are scored separately.**
  Collapsing them let a mutant inherit its twin's kill, which can only raise
  a kill rate.
- **`corral review` records whether a seat named a model or only the tool
  that ran one** (`ReviewerModelResolved`, `VerifierModelResolved`), and
  says so on stdout, so a per-model analysis can filter rather than average
  over rows that name a CLI.

### For people and agents reading the record

- **`corral ui --write`**: a write mode behind a launch token (loopback
  only, Host- and Origin-checked) that rechecks a finding's reproduction on
  `HEAD` with `corral review recheck` and records a verdict through the CLI.
  Plain `corral ui` is unchanged.
- **A `corral` Agent Skill** (`skills/corral/SKILL.md`) for coding agents:
  where the record is, why a critic's unverified finding is not a fact, and
  how to check one without touching the user's work. It installs from a
  Claude Code plugin marketplace in this repository, which also wires the
  read-only findings server, `corral mcp`.
- **The ledger renders as a public page** on the site, with each review's
  executed scripts, their output and exit codes, and the four execution
  outcomes kept apart.

## [v1.0.0-rc.13] — 2026-09-09

Same code as rc.12 plus the release-gate fix below. **rc.12 is tagged but has
no GitHub Release**: the validate gate added that morning demanded green on
the tag's own commit, which this project's pin order makes impossible, and a
re-run replays the workflow file from the tag — so the fix could not reach
it. rc.12 resolves on the Go proxy and is sound; rc.13 is the one to install.

- **The release gate accepts green on the tag's commit OR on a default-branch
  commit that contains it.** A gate no correct procedure can pass is an
  outage, not a gate.
- `CONTRIBUTING` documents the window where `main` is red by construction,
  between the tag push and the pin bump.

## [v1.0.0-rc.12] — 2026-09-09

Seven rounds of cold review on rc.11 in one day, by three vendors' models
(Claude Code, Codex, and an antigravity seat), every finding an entry on the
public `corral/ledger` branch. **Upgrade from rc.11**: two of these let a
merge gate pass on an audit that measured nothing.

### The gate could say pass when nothing was measured

- **`--max-proven-missed` passed a file whose every mutant the compile gate
  rejected.** Nothing was graded, so `ProvenMissed` is 0 — bit-identical to
  "measured, and nothing survived". `noGradableMutant` already existed and
  its own comment called itself "ONE predicate for the two places that must
  agree"; the gate was a third place that did not use it, and the weak-file
  line a fourth, printing a literal `0.00` kill rate for a file it graded
  nothing on.
- **A changed source excluded as `ambiguous-test` was never counted as
  unauditable**, because the gate matched `no-paired-test` alone — so a pull
  request touching a file whose pairing corral REFUSES (grading it would
  produce a confident, signed, wrong verdict) went green on exactly the
  change the gate exists to inspect. Now a derived property,
  `reposcan.UnauditableChangedSource`, classified once beside the reasons.

### The record could be edited without the verifier saying so

- **`corral ledger checkpoint` pruned without verifying** — the one verb that
  destroys evidence never looked at it, so a tampered entry could be deleted
  and the genesis left verifying clean.
- **`append` linked to the head's stored hash without recomputing it**, so a
  new entry extended and SIGNED a tampered predecessor; then it checked only
  the head, so an edited middle entry still passed.
- **`push` and `LoadDir` verified nothing at all** — the two doors that carry
  the record out, to a warehouse and to every reader. All five doors now call
  one `RequireIntactChain`, including `prior.Load`, which feeds a later
  audit's priors.
- **`keyid` — the name of who signed — sat outside the hash and was never
  compared to the verifying key**, so rewriting it left `HashOK`, `SigOK` and
  `Problem` untouched while `verify --ledger` printed the attacker's chosen
  name as the signer. **corral-ledger-4** puts it inside the hashed bytes and
  the signer names itself before hashing. Older entries verify unchanged
  under their own rules and now carry a note that their signer name is
  self-reported.
- **The warehouse rows hash was not injective**: numbers went through float64
  and empty array ELEMENTS were pruned, so two different row sets could share
  a signed hash. Closed at rows-hash **version 4**; versions 2 and 3 keep the
  old form byte for byte so every existing statement still verifies.
- **Retracting a retraction did nothing**, making a mistaken retraction a
  one-way door the record does not otherwise have.
- **A signed verdict omitted the challenger measurement** the returned
  verdict carried: `tickAggregate` signed and assigned it 41 lines later, and
  `Verdict` is passed by value. Found by an outside reviewer.

### Credentials and isolation

- **`CORRAL_TOKEN` was not scrubbed** from the environment the workspace
  substrate hands the audited repository's own test command. The provider
  keys were scrubbed after an earlier review; corral's own bearer token was
  left behind.
- **`fence.Untrusted` neutralized the sentinel in `content` only**, while
  `label` and `provenance` — which a caller derives from ingested corpus
  data — were interpolated verbatim inside the fence.

### Measurement

- **A mutant could anchor twice and be applied once.** The uniqueness guard
  resumed past the whole match, so a self-overlapping SEARCH ("aa" in "aaa")
  read as unique.
- **Ranking claimed `churn-x-size` while using neither.** `git log` names
  paths from the work-tree root and candidates from the scan root, so
  auditing a subdirectory matched no churn entry at all. Second cause found
  for that same false label, so an empty churn map now reports `size-only`
  with a reason.
- **A test-critic could earn leaderboard credit for a claim execution never
  established.** Auto-confirming "this test can never fail" because some
  OTHER test killed the mutants is not evidence about this test. The path is
  refute-only now.

### Also

- A third built-in review seat, `antigravity`, and `--expect-head` for
  `ledger verify` — a chain verifies against itself, so end-truncation needs
  an anchor from outside.
- Four npm advisories cleared including a critical in astro, and grpc bumped
  past GHSA-2v4p-qf9q-27wj.
- Documentation that was false: `AGENTS.md` claimed no Makefile and no
  SIGINT/SIGTERM handler (both exist), the ROADMAP called shipped
  coverage-derived pairing "the honest direction", and seven claims on the
  site — including a page telling readers every audit draws on the brain's
  corpus, four lines under its own banner saying no audit reads it.

## [v1.0.0-rc.11] — 2026-09-08

- **The receipt's path is made, and proved writable, before anything is
  spent.** `--attest .corral/statement.json` on a checkout with no
  `.corral/` yet ran the whole herd and then failed to write the statement
  (and so never reached `--transparency`). Seen on camera while recording
  the certify video. The directory is created like the ledger's, and a
  probe write refuses an unwritable path before the run.

## [v1.0.0-rc.10] — 2026-09-07

Twenty rounds of the review loop on corral itself in one day — 57 claims,
44 confirmed and fixed, the rest refuted on the record. The scorer proves
every door it grades through; the statement carries what the record says;
retraction reaches every reader; the Action runs the review.

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
- **Held by execution means an exit 0 on the record; a review's shape is
  its findings.** A REPRODUCED finding with no exit code recorded was
  credited to the reviewer as held (an entry written by hand or by an older
  writer); an object carrying only an `opinion` key passed the review-shape
  check, so a stray literal still replaced the payload. Both found by a
  Codex reviewer re-attacking the morning's fix (review `a906b2676dca`,
  Gemini verifying). Now: no exit code, no outcome; the shape is
  `findings` (or `sound`), the verifier's is `refutations`. The harness
  marker is a column of the findings grain (`unrun`, `refutation_unrun`),
  not a prefix of `demoted`, and `models rank` reads every input the
  outcome rule needs — exit codes, markers, the refutation's tier — from
  the grain, so a warehouse grades the same as the directory.
- **No phantom scan row; the schema migration reaches any handle.** A
  bundle with no scan header grew a `corral_scans` row because the writer
  stamped `pushed_by` on the empty row; the migration probe asked for a
  catalog named `warehouse`, so `EnsureSchema` on a plain DuckDB handle
  found no columns and every ALTER failed. Found by a Gemini reviewer
  (review `e00b52bab444`, Codex verifying). The probe names the current
  catalog; a table with no columns is no table, and nothing to migrate.
- **A tracked top-level dependency link no longer collapses the pool.** A
  repository that tracks `.venv` as a symlink has it in the git universe
  with no `/`; `copyTree`'s skip guard required one, tried to create the
  link it had just made, and the whole workspace pool fell to one tree.
  Found by a Gemini reviewer (review `4d83ed44bab5`, Claude Code verifying).
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
- **Pairing, the cache key, the landing hint and churn, at their doors.**
  Four defects in `internal/reposcan` from its first review (`28c4ae555cbc`,
  Claude Code reviewing, Codex verifying; a fifth claim was refuted). An
  operator-mapped `--tests` pairing was dropped from the collision grouping
  before it formed, so a convention pairing on the same test file was left
  alone and one test graded two sources — an explicit pairing is now a
  claimant that is never demoted, and the convention pairing that collides
  with it is. `CanonicalKV` — the form the model set and the audit config
  are hashed into a verdict's cache key as — was not injective: a model name
  carrying `,` or `=` rendered the same bytes as a different role map, and
  `CacheKey` served one herd's verdict for another. It escapes those bytes
  now; every key and record that never carried a delimiter is byte-identical
  to before, so nothing cached changes form — only the ambiguous strings,
  which had to. The most-covering test *file* is the file whose tests
  together executed the most lines, not the file of the single biggest test.
  Churn reads `git log … -z`, so a path git would C-quote (non-ASCII, a
  space) matches its own history instead of ranking as churn 1 under a
  signal that still said `churn-x-size`. The reviewer's two reproductions are
  regression tests, inverted (`reviewed_pairing_keys_test.go`).
- **A merged test file renames identifiers, never the inside of a string.**
  Two defects in `internal/lang`'s test concatenator from its first review
  (`9e3f89b00949`, Codex reviewing, Claude Code verifying). A collision
  repair was an unscoped word-boundary replace over the whole body, so a
  proven test asserting `equal(helper(), "helper")` came out asserting
  against `"helper_s0m1"` — a proven test with its meaning changed. Each
  language's spec now names its string and comment syntax and the rename
  skips those spans. And `idSuffix` dropped every non-alphanumeric byte, so
  mutant ids `a/b` and `a-b` both became `ab` and the merged file declared
  the repaired name twice; the suffix is injective now — `s0/m1` renders
  `s0_m1`, any other shape renders as an underscore plus its bytes in hex,
  which no clean id can. A suffix a reader has seen before therefore changes:
  `test_x_s0m1` is `test_x_s0_m1`. The reviewer's two reproductions are
  regression tests, inverted (`reviewed_concat_test.go`).

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
