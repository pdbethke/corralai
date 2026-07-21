<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Eval corpus

Known-adequacy fixtures corral audits itself against. Two uses:

- **`manifest.json`** drives `corral eval` — a versioned set of Go targets, each
  labelled `thorough` or `gappy` with its known gap, so the harness can validate
  that the pool's kill-rate tracks reality (and report *do-not-publish* if the
  metric is miscalibrated).
- The **`passwd_*` fixture dirs** are self-contained `certify --local` demos:
  the same password validator and the same deliberately-weak test, in five
  languages. They are not `corral eval` targets (that harness is Go-only today);
  they exist so anyone can reproduce the multi-language blind-spot result.

## The password blind spot, in five languages

Each `passwd_<lang>/` holds a validator — *valid iff length ≥ 12 AND it contains
an uppercase letter, a lowercase letter, a digit, and a symbol* — guarded by a
**length-only "gappy" test** that only ever feeds one valid password and one
too-short one. That test never exercises the four character-class rules, so a
mutant that quietly drops "must contain a digit" sails straight past it. The
suite is green; the suite is nearly blind.

| Dir | Language | Code | Gappy test |
|-----|----------|------|------------|
| `passwd/` | Go | `passwd.go` | `passwd_gappy_test.go` (+ a `thorough` counterpart) |
| `passwd_py/` | Python | `passwd.py` | `test_passwd.py` |
| `passwd_rb/` | Ruby (minitest) | `passwd.rb` | `passwd_test.rb` |
| `passwd_js/` | JavaScript (node:test) | `passwd.js` | `passwd.test.js` |
| `passwd_ts/` | TypeScript (tsc + node:test) | `passwd.ts` | `passwd.test.ts` |

## Reproduce it yourself

One command per language, off your own key, in a sandbox (bwrap or `--jail
container`). The language is inferred from the code file's extension:

```bash
export ANTHROPIC_API_KEY=sk-ant-...

corral certify --local --code eval/corpus/passwd_ts/passwd.ts \
  --goal "valid iff length >= 12 AND it contains an uppercase letter, a lowercase letter, a digit, and a symbol" \
  --max-shards 1 --shadow-model off
```

Swap `passwd_ts/passwd.ts` for any of `passwd/passwd.go`, `passwd_py/passwd.py`,
`passwd_rb/passwd.rb`, `passwd_js/passwd.js`. Each returns **needs-review** — the
gappy suite catches only a fraction of the planted faults — and the herd hands
back the test the suite was missing.

**The numbers move.** Which faults get planted is non-deterministic, so the exact
kill-count changes run to run — quote the shape, not the decimal. A reference
sweep on 2026-07-20 (Claude-default: Sonnet mutant+writer, Haiku critic; single
generator, 5 mutants each) caught: **Go 2/5 · Python 1/5 · Ruby 2/5 ·
JavaScript 2/5 · TypeScript 0/5**. Same blind spot, every language.

**Toolchain must be jail-visible.** Install the runner system-wide (under `/usr`,
not `--user`): `python3-pytest` / `ruby` / a global `node` + `typescript`. A
`--user` or project-local install is invisible inside the sandbox and the run
fails closed. See the docs for the per-language requirements.
