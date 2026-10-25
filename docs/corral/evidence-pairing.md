# Evidence pairing: what ran on real repositories

Evidence pairing lists the test units the operator's command collects (a test
file for JavaScript, TypeScript, Ruby and PHP; a package for Go; a Cargo target
for Rust), runs each unit alone under coverage, and makes a candidate of any
source file a green unit executed and can land a test beside. This page records
what that pass did on real third-party repositories. Every number below is what
the run printed. Nothing here is an estimate.

Every run below was a free dry run, `certify --repo . --dry-run --evidence`,
with no model and no key. Each target was a scratch clone at the commit named.
The binary was built from branch `feat/evidence-pairing` at `22f2719e`. The
runs marked **re-run** used the same branch after its fixes for what the first
runs found (commits `54e33acf` to `bad88e83`). The first results stay on this
page beside them. The
workstation ran Linux with bubblewrap 0.11.1, node 22.23.4, go 1.26.10,
ruby 3.3.8 with rspec-core 3.13.6, PHP 8.5.4 with pcov and phpunit 11.5.50,
and cargo 1.90.0 with `llvm-tools` from the user's rustup toolchain. Every run
also passed `--no-ledger` and a scratch `--cache-db`. Times are wall clock
for the whole dry run, measured with `/usr/bin/time`.

The **substrate** column matters. `jail` is the default bwrap sandbox. Where
a run says `workspace`, the jail run failed for the reason given in its
section, and the same command was run again with `--substrate workspace` on
the scratch clone, where the clone was the isolation boundary.

## Summary

| language | repository | substrate | units run / failed | evidence-paired | name-paired | time |
|---|---|---|---|---|---|---|
| Go | gin-gonic/gin | workspace | 6 / 0 | 19 | 29 | 4.31 s |
| Go | gin-gonic/gin (re-run) | jail | 6 / 0 | 19 | 29 | 42.4 s |
| JavaScript | tj/commander.js | jail | 109 / 4 | 7 | 0 | 19.33 s |
| JavaScript | tj/commander.js | workspace | 109 / 0 | 7 | 0 | 25.08 s |
| JavaScript | expressjs/express | jail | no unit form (mocha) | 0 | 0 | 0.05 s |
| TypeScript | morluto/rea | workspace | 25 / 25 | 0 | 564 | 110.55 s |
| Ruby | ku1ik/rainbow | workspace | 11 / 4 | 10 | 0 | 2.50 s |
| PHP | webmozart/assert | jail | 2 / 0 | 1 | 1 | 1.53 s |
| Rust | indexmap-rs/indexmap | workspace | 5 / 0 | 0 | 2 | 12.88 s |
| Rust | indexmap-rs/indexmap (re-run) | jail | 5 / 5 (coverage unavailable) | 0 | 2 | 8.15 s |

## Go: gin-gonic/gin

- Commit `34dac209ffb6ef85cc78c5d217bbb7ad001d68fd`.
- Command: `corral certify --repo . --substrate workspace --dry-run --evidence -- go test ./...`
- **Jail run:** the unit listing failed with
  `go: listing the test units of `go test ./...` exited 127 … exec: go: not found`.
  On this workstation `go` is installed under the home directory
  (`~/sdk/go`). The jail binds the toolchain of each interpreter its command
  names, and for the listing command it found only the words of the `sh -c`
  prelude (`printf`, `pwd`), not `go`. So the listing ran with no Go on its
  PATH. The scan stayed name-paired and printed that reason.
- **Workspace run:** 6 units listed, 6 run, 0 failed; +19 candidates.
- **Jail re-run** (commit `54e33acf`). The jail now binds the program the
  prelude runs through `exec "$@"`, and the quoted interpreter in each unit's
  coverage command. Before that fix, the unit runs failed next with
  `sh: 1: go: not found`. The result now matches the workspace run (the time
  includes `go mod vendor` in the sandbox):

```
  units: running 6 of 6 listed test unit(s) alone under coverage, 8 at a time, within 15m0s for the whole pass…
  units: 6 run, 0 failed (8 at a time)
  widened by evidence: +19 candidate(s); re-ranked by size-only under the same bound, auditing 25 of 48 candidate(s)
  131 file(s) walked; 48 candidate(s); 25 job(s); 106 file(s) excluded from the audit
  evidence-paired 19 · name-paired 29 · uncovered 0 · import-only 0
```

The workspace run's output:

```
  units: running 6 of 6 listed test unit(s) alone under coverage, 1 at a time, within 15m0s for the whole pass…
  units: 6 run, 0 failed (1 at a time)
  widened by evidence: +19 candidate(s); re-ranked by size-only under the same bound, auditing 25 of 48 candidate(s)
  131 file(s) walked; 48 candidate(s); 25 job(s); 106 file(s) excluded from the audit
  evidence-paired 19 · name-paired 29 · uncovered 0 · import-only 0
```

The 19 files evidence pairing added are `binding/{bson,form,header,plain,protobuf,query,uri}.go`
(landing beside `binding/binding_msgpack_test.go`) and
`render/{bson,data,html,json,msgpack,pdf,protobuf,redirect,text,toml,xml,yaml}.go`
(landing beside `render/reader_test.go`). Each one is executed by its own
package's unit. `render/json.go`, `render/html.go` and `binding/form.go`
reached the 25-file audit bound.

## JavaScript: tj/commander.js

- Commit `ba6d13ddb4243e5913367734f8c159089ffe7834`; `npm ci` before the run.
- Command: `corral certify --repo . --dry-run --evidence -- node --test`
- **Jail run:** 109 units listed and run, 4 failed (`run failed: exit 1`):
  `tests/command.default.test.js`, `tests/command.executableSubcommand.lookup.test.js`,
  `tests/command.executableSubcommand.signals.test.js` and
  `tests/command.exitOverride.test.js`. Each of those files passes when run
  alone on the host (`node --test <file>`), and all four ran green in the
  workspace run below. So they fail only inside the jail. Their output was not
  shown on the scan line.
- **Jail re-run** (commit `bad88e83`): the same 4 units failed, and each line
  now carries the tail of what the unit printed. The tails show stack frames
  from spawned child processes (`ChildProcess.exithandler`, the CJS module
  loader) and, in `command.executableSubcommand.signals.test.js`, the
  assertion `null !== 42` on a child's exit code. The other numbers were
  unchanged: 109 run, 7 promoted, 19.15 s.

```
  units: running 109 of 109 listed test unit(s) alone under coverage, 8 at a time, within 15m0s for the whole pass…
  units: 109 run, 4 failed (8 at a time)
  widened by evidence: +7 candidate(s); re-ranked by size-only under the same bound, auditing 7 of 7 candidate(s)
  219 file(s) walked; 7 candidate(s); 7 job(s); 212 file(s) excluded from the audit
  evidence-paired 7 · name-paired 0 · uncovered 0 · import-only 0 — unit evidence is partial: 4 of 109 unit(s) contributed none
```

commander's tests are named after behaviour (`tests/command.help.test.js`,
`tests/options.implies.test.js`), so name pairing pairs none of its sources.
Evidence pairing added all seven: `lib/command.js` (105 covering units),
`lib/option.js` (84), `lib/argument.js` (50), `lib/help.js` (49),
`lib/error.js` (30), `lib/suggestSimilar.js` (10) and `index.js` (2).

- **Workspace run** (same command, `--substrate workspace`): 109 run,
  0 failed, the same 7 files promoted, 25.08 s.

## JavaScript: expressjs/express

- Commit `a3714473feb3d2908add734d340e7755fd85e0a3`; `npm install` before the run, since the repository has no lockfile.
- Command: `corral certify --repo . --dry-run --evidence -- node ./node_modules/mocha/bin/mocha.js --require test/support/env --check-leaks test/ test/acceptance/`
- No unit pass ran. express uses mocha, and mocha has no one-file run: it adds
  its configured spec list to the files named on its command line. The runner
  table's mocha row therefore has no unit form, and the scan says so:

```
  units: javascript: `node ./node_modules/mocha/bin/mocha.js --require test/support/env --check-leaks test/ test/acceptance/` has no one-unit form corral can list and run, so no unit evidence
  213 file(s) walked; 0 candidate(s); 0 job(s); 213 file(s) excluded from the audit
  evidence-paired 0 · name-paired 0 · uncovered 0 · import-only 0 — pairing-only candidacy (no selection evidence: javascript: `node ./node_modules/mocha/bin/mocha.js …` has no one-unit form corral can list and run, so no unit evidence)
```

## TypeScript: morluto/rea

- Commit `87a5be3000b7cf4c3038277a7e809319c13cb2cd`; `npm ci` and
  `npm run test:prepare` before the run. The repository's `engines` field
  accepts node 22.23.4, the node installed here.
- Command: `corral certify --repo . --path src --substrate workspace --dry-run --evidence -- node ./node_modules/vitest/vitest.mjs run tests/composition`
  (vitest 5.0.3). The command is scoped to `tests/composition`, the directory
  where an authored test for `src/application/reverse/ReverseEngineeringService.ts`
  would land. The whole suite has 2,186 test files; a host run of it was
  stopped after more than ten minutes.
- **Without `--path src`**, the scan spans Python and TypeScript, and an
  explicit test command is refused for a multi-language scan (exit 2).
- **Jail run** (with `--path src`): the jail could not be prepared:
  `repo has more than 64 MiB of text — too large to seed the jail workspace`.
  The checkout includes a 46 MB `dist/` produced by `test:prepare`.
- **Workspace run:** 25 units listed and run, **25 failed**. Every failure was
  `unparseable: … coverage report has a header but no file entries`, and each
  one listed the files its coverage reducer could not count. vitest executes
  transformed code under each source file's URL. For example,
  `src/application/reverse/ReverseEngineeringService.ts` was reported 9 times as
  `V8 ran 111533 characters under its URL and the file on disk has 41460 (transformed code, or a file changed since it ran)`.
  The JavaScript reporter refuses to count such a file, so no unit produced
  evidence:

```
  units: running 25 of 25 listed test unit(s) alone under coverage, 1 at a time, within 15m0s for the whole pass…
  units: 25 run, 25 failed (1 at a time)
  2812 file(s) walked; 564 candidate(s); 25 job(s); 2787 file(s) excluded from the audit
  evidence-paired 0 · name-paired 564 · uncovered 0 · import-only 0 — pairing-only candidacy (no selection evidence: no unit of 25 contributed evidence)
```

`ReverseEngineeringService.ts` was **not** evidence-paired. The scan's output
was 1.4 MB, because each failed unit's line carries its full list of uncounted
files.

- **Re-run** (commit `bad88e83`, same command, workspace): the same 25 of 25
  units failed. Each line now gives a count, for example
  `392 file(s) measured and not counted (listed in --dry-run --json)`, and
  the full lists are in the `--dry-run --json` inventory's `units` field. The
  scan output was 18,129 bytes; the first run's was 1,422,138. 1 m 51.9 s.
- Remapping vitest's transformed ranges through Node's own source-map cache
  (`--enable-source-maps`) was tried and does not work. On node 22.23.4 the
  coverage files' `source-map-cache` held only `node_modules` files Node
  loaded itself, never a project source. vitest 5.0.3 runs each project module
  through `vm.runInThisContext`, and Node did not cache a map for code compiled
  that way, even with an inline `sourceMappingURL`; a minimal `vm` script
  showed the same. vitest-run files therefore stay uncounted.
- **Jail re-run** with `dist/` moved out of the clone: the jail still could not
  be prepared (`more than 64 MiB of text`).

## Ruby: ku1ik/rainbow

- Commit `c6fcabf39c2475dbdb219c23c24a1d8c9dbd4a28`. rainbow was chosen
  because it has no runtime dependencies and its spec helper needs only
  rspec, which this workstation has. rubocop, the sweep's Ruby repository,
  needs gems that are not installed here.
- Command: `corral certify --repo . --substrate workspace --dry-run --evidence -- ruby -S rspec`
- **Jail run:** the listing failed with `can't find gem rspec-core (>= 0.a) with executable rspec`.
  rspec-core is installed under `~/.local/share/gem`, which the jail does not
  bind. AGENTS.md asks for test frameworks under `/usr`.
- **Workspace run:** 11 units, 4 failed (`run failed: exit 1`). Run alone on
  the host, those spec files fail too. For example,
  `spec/unit/color_spec.rb` raises
  `uninitialized constant Rainbow::Color::X11Named::X11ColorNames`, because
  it depends on another spec file loading first. Their coverage was not used:

```
  units: running 11 of 11 listed test unit(s) alone under coverage, 1 at a time, within 15m0s for the whole pass…
  units: 11 run, 4 failed (1 at a time)
    spec/integration/rainbow_spec.rb: run failed: exit 1
    spec/unit/color_spec.rb: run failed: exit 1
    spec/unit/null_presenter_spec.rb: run failed: exit 1
    spec/unit/string_utils_spec.rb: run failed: exit 1
  widened by evidence: +10 candidate(s); re-ranked by size-only under the same bound, auditing 10 of 10 candidate(s)
  35 file(s) walked; 10 candidate(s); 10 job(s); 25 file(s) excluded from the audit
  evidence-paired 10 · name-paired 0 · uncovered 0 · import-only 0 — unit evidence is partial: 4 of 11 unit(s) contributed none
```

rainbow's specs live under `spec/unit/` and `spec/integration/`, a layout the
Ruby naming rule does not mirror, so name pairing pairs none of its 11 sources.
Evidence pairing added 10. Among them, `lib/rainbow/ext/string.rb` and
`lib/rainbow/refinement.rb` are executed only by integration specs.

## PHP: webmozart/assert

- Commit `2ccb7c2e821038c03a3e6e1700c570c158c55f70`;
  `composer --working-dir=tools/phpunit install` before the run (the project
  keeps phpunit under `tools/phpunit`).
- Command: `corral certify --repo . --dry-run --evidence -- php tools/phpunit/vendor/bin/phpunit`
- **Jail run:** 2 units, 0 failed; +1 candidate.

```
  units: running 2 of 2 listed test unit(s) alone under coverage, 8 at a time, within 15m0s for the whole pass…
  units: 2 run, 0 failed (8 at a time)
  widened by evidence: +1 candidate(s); re-ranked by size-only under the same bound, auditing 2 of 2 candidate(s)
  242 file(s) walked; 2 candidate(s); 2 job(s); 240 file(s) excluded from the audit
  evidence-paired 1 · name-paired 1 · uncovered 0 · import-only 0
```

The file evidence pairing added is `src/Mixin.php` (2 covering units, landing
beside `tests/AssertTest.php`).

## Rust: indexmap-rs/indexmap

- Commit `41a870887c4c77adf665886e63df08f406bfe37a`. indexmap was chosen
  because it is a pure-Rust crate with integration tests in `tests/` and two
  sources whose tests sit in separate files (`src/map.rs` declares
  `mod tests;` → `src/map/tests.rs`, and `src/set.rs` likewise). A Rust source
  is auditable only when its test lands in a separate file. The first
  candidate tried, `dtolnay/semver` (`280ebcb6`), has none. All 13 of its
  sources are excluded as `test-landing-not-built` before pairing, so it gives
  the unit pass nothing to run for (its scan printed no `units:` line). On the
  re-run (commit `65f13a47`) its summary line says why:
  `pairing-only candidacy (no selection evidence: no source file can be audited yet (13 rust source(s) test landing not built), so no evidence pass ran)`.
- Command: `corral certify --repo . --substrate workspace --dry-run --evidence -- cargo test`
- **Jail run:** the listing failed with `exec: cargo: not found`, for the same
  reason as Go: `cargo` lives in `~/.cargo/bin`. A second jail run named
  the system toolchain's `/usr/local/cargo/bin/cargo`, and its listing failed
  with ``could not execute process `rustc -vV` ``. The system toolchain also
  has no `llvm-tools`.
- **Jail re-run** (commits `54e33acf`, `9aa2be49`): the listing now runs. The
  jail binds the program behind the prelude, and cargo's listing is read
  although its progress lines share the jail's one output stream. All 5 units
  then failed with the reason the instrument gives. The toolchain the jail
  resolved (`/usr/local/rustup/toolchains/1.92.0-x86_64-unknown-linux-gnu`)
  has no `llvm-tools`:
  `coverage unavailable: llvm-profdata and llvm-cov are in neither the toolchain's llvm-tools (…) nor PATH: install them with 'rustup component add llvm-tools' for the toolchain the suite builds with`.
  In the first re-run the remedy read `install them with  for`, because the
  shell executed the backquoted command; `9aa2be49` prints it as text. A
  workspace run against that same system toolchain printed the remedy, and
  the toolchain's `lib/rustlib/x86_64-unknown-linux-gnu/bin` held the same
  four entries before and after the run.
- **Workspace run:** 5 units (`indexmap/lib`, `indexmap/test:equivalent_trait`,
  `indexmap/test:macros_full_path`, `indexmap/test:quick`,
  `indexmap/test:tests`), 0 failed. Each unit's coverage carried real
  per-file line counts. For example, `test:quick` executed 13 files under
  `src/`, including `src/inner/extract.rs` (62 of 62 executable lines).

```
  units: running 5 of 5 listed test unit(s) alone under coverage, 1 at a time, within 15m0s for the whole pass…
  units: 5 run, 0 failed (1 at a time)
  47 file(s) walked; 2 candidate(s); 2 job(s); 45 file(s) excluded from the audit
  evidence-paired 0 · name-paired 2 · uncovered 0 · import-only 0
```

No file was promoted. The 28 sources without a separate test file are excluded
as `test-landing-not-built` before pairing, so the evidence never reaches
them. That holds even for `src/inner/extract.rs`, which `test:quick` executed
in full and whose integration-test landing (`tests/quick.rs`) exists. The two
auditable files were already name-paired. Promoting such a file on unit
evidence is not built. `certify --local --repo-dir . --code src/inner/extract.rs`
refuses the file before any spend, because the authored test's landing is
derived from the source and lands on the source itself. A promoted file would
therefore be listed by the scan and then refused by the audit.
