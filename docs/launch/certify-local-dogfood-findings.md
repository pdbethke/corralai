# `corral certify --local` — first-run dogfooding findings (2026-07-18)

First real end-to-end run of `certify --local` "as a user." Ran on the Hetzner
prod box (has the anthropic key in credstore + a working bwrap jail); target was
a **Python** password validator with a deliberately gappy test. Everything below
is real friction we hit, in order — this is the raw material for (a) the setup
docs and (b) a couple of genuine product fixes.

## The command (what a user actually types)

```
export ANTHROPIC_API_KEY=sk-ant-...
corral certify --local \
  --code passwd.py \
  --test test_passwd.py \
  --goal "valid() returns True only for passwords of length >= 12 that contain an uppercase letter, a lowercase letter, a digit, and a symbol" \
  --jail bwrap
```

Decorrelation defaults (good): `mutant-generator=claude-sonnet-5
test-writer=claude-sonnet-5 test-critic=claude-haiku-4-5` — the critic is a
different model from the writer, off one ANTHROPIC_API_KEY.

## Friction hit, in order

1. **Stale binary.** The `corral` on disk predated `--local` (`flag provided but
   not defined: -local`). Rebuild from main. → *Docs: say `go install
   github.com/pdbethke/corralai/cmd/corral@latest` gets the `--local` build.*

2. **`--out` is NOT a `--local` flag.** It exists on daemon-mode `certify` but
   not `certify --local`. The signed record goes to the local build ledger +
   stdout. → *Docs: don't show `--out` in the `--local` quickstart.*

3. **Ubuntu 24.04 blocks the bwrap jail by default.** On a stock 24.04 laptop,
   `apparmor_restrict_unprivileged_userns=1` → `bwrap: setting up uid map:
   Permission denied`. `--local` fails closed with an actionable error (it never
   runs unsandboxed). Two fixes for a user:
   - `--jail container` (Docker/Podman) + `export CORRALAI_EXEC_IMAGE=golang:1.26`
     (or a lang-appropriate image with the test toolchain). No sudo.
   - OR install the apparmor userns profile for bwrap (needs sudo). See the
     brain-on-Ubuntu bwrap setup note.
   → *This is THE first-user wall on Ubuntu. Docs MUST cover it up front. The
   prod box works because we applied the apparmor profile on 2026-07-08.*
   → *Product idea: when bwrap is blocked, `--local` could auto-suggest (or
   auto-fall-back to) `--jail container` and default CORRALAI_EXEC_IMAGE per
   `--lang`. Right now the container path needs the image env set by hand.*

4. **Build-ledger lock clash with a running brain (DuckDB single-writer).**
   `--local` opens the SAME ledger as `corral certify` at
   `$HOME/.claude/corralai_build.duckdb`. If a brain daemon (or another corral)
   holds it, you get `Conflicting lock is held ... See duckdb concurrency`.
   Workaround: run under a fresh `HOME` so the ledger path differs. → *Docs +
   product: a `--build-db PATH` flag (or a clear "another corral holds the
   ledger" error) would beat "set HOME". On a normal user laptop with no daemon
   this won't bite — only on a box already running a brain.*

5. **BUG — absolute `--code`/`--test` paths break the scorer.** With absolute
   paths, the run retries 20× then fails:
   `adequacy: refusing to write file outside workspace:
   "/tmp/corral-py-demo/test_passwd.py"`.
   Root cause: `internal/advpool/gate.go` (`JailScorer.Score`,
   `JailValidator.CompileTest`) use the raw `codePath` as the jail-workspace map
   KEY, and `advPoolTestPath(codePath)` derives the test key in the code's own
   dir. Absolute codePath → absolute keys → `internal/adequacy/jail.go:64`
   correctly refuses (its guard rejects abs/`..` keys, an anti-traversal
   measure). It only ever worked because the Go site run used a repo-RELATIVE
   path (`eval/corpus/passwd/passwd.go`).
   - **Workaround (used to get the first verdict):** `cd` into the target dir and
     pass basenames (`--code passwd.py --test test_passwd.py`).
   - **Real fix:** `cmd/corral/certify_local.go` must normalize CodePath /
     DevTestPath to workspace-relative (basename for a single-file `--local`
     audit) before building the RunSpec — a user WILL pass an absolute path, and
     the article CTA literally shows `--code path/to/file`.

## The Python target used (the gappy demo)

`passwd.py` — `valid()` = len>=12 AND has upper/lower/digit/symbol.
`test_passwd.py` — two tests, both feeding passwords that already satisfy EVERY
rule, so nothing notices when a character-class requirement is dropped. Passes
green under system pytest 7.4.4 as-is (2 passed). Same blind spot as the Go
site demo, now in Python (Monty Python payoff intact).

## Verdict (real, signed — converged 20:06:47, ~2 min)

```
adversarial verdict — passwd.py @ local
  language:      python
  status:        NEEDS-REVIEW (dev suite killed 1/5 mutants)
  dev_kill_rate: 0.20
  survivors:     4
  proven_missed: 4          <- the herd's authored test PROVES all 4 are real, catchable bugs
  vacuous tests: 5 flagged  <- the critic (claude-haiku-4-5) pans test_accepts_a_valid_password
  models:        mutant-generator=claude-sonnet-5  test-critic=claude-haiku-4-5  test-writer=claude-sonnet-5
  signed:        record 1  (verify offline: corral certify verify <record>)
```

The critic's pan, verbatim: *"test_accepts_a_valid_password passes a single password
satisfying all four character-class requirements simultaneously. If any one requirement
… were deleted from the implementation, this test would still pass and would not catch
the regression."* Exactly the Frampton flaw — obvious, and nobody on the dev's team said it.

The test-writer's authored fix (kills the survivors), which the tool hands back:
`test_missing_symbol_with_all_other_classes_len12_is_invalid` +
`test_short_password_with_all_classes_is_invalid`.

**It works.** Same blind spot as the Go site demo, now proven + signed in Python.

## Two more doc/product gaps found at the finish line

6. **`certify verify` takes a FILE, but `--local` writes no file.** The verdict says
   "verify offline: corral certify verify <record>", but `verify` reads a signed-statement
   file (+ `--pubkey`/`--brain`), and `--local` has no `--out`. So a user can't actually
   verify their own `--local` record without spelunking the DuckDB ledger. → *Product: add
   `--out` to `--local` (write the signed statement too), or make `certify verify <id>` read
   the local ledger. Until then, don't tell users to "verify offline" after a `--local` run.*

7. **The 10-min soft `--timeout` overshoots quietly.** Convergence here was ~2 min, but the
   help warns (correctly) it's not a hard cap. Fine — just note it in docs so a slow run
   doesn't read as a hang.
