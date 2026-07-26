# Handoff — corralai

> Session working note. Snapshot of where things stand so the next session (or a
> terminal pickup) can continue without re-deriving state. Not a permanent doc —
> delete or update freely.

_Last updated: 2026-07-24 · base: `main` @ `a77cd35`_

## State

- **Branch:** `main`, working tree clean, synced with `origin/main`.
- **Open PRs / issues:** none.
- **Feature branch:** `claude/corral-6pn66m` (this note lives here); its prior
  commit is already in `main` via the #22 merge.

## Landed this session (all on `origin/main`)

| SHA | What |
|---|---|
| `ccda8e5` | docs sync — ROADMAP / README / `skills/using-corralai/SKILL.md` brought in line with shipped code (Rekor witness anchoring, MCP gateway, tests×mutants matrix, scorecard/criticscore, binary tables) |
| `5978d48` | CI: `validate` skips the ~15-min build for **docs-only** changes (fail-safe change detector); `deploy` gated the same way so a docs-only push to `main` doesn't rebuild/restart the brain |
| `a77cd35` | CI: CLA gate no longer red on Claude-authored PRs — added `Claude` to the allowlist in `.github/workflows/cla.yml` |

PRs #20, #21, #22 all merged & closed.

## Pick up in the terminal

```bash
git checkout main
git pull origin main      # lands at a77cd35 (or later)
git log --oneline -5
```

## Caveats

- **CLA fix** applies to PRs opened **after** `a77cd35`. `pull_request_target`
  runs the base-branch workflow, so it can't fix its own PR — #22's historical
  CLA check stays red; ignore it.
- **Docs-only CI skip** fails safe: anything not confidently enumerated as
  Markdown-only (any non-`.md` file, an undetermined diff, `workflow_dispatch`)
  runs the full build. Logic lives in the `validate` job's "Detect whether
  anything but docs changed" step in `.github/workflows/deploy.yml`.

## Candidate next work (scanned, nothing started)

- **Matrix → JS/TS/Ruby** — small, well-specified: implement `ListTestsCmd` /
  `ParseTestList` / `SingleTestCmd` for those `internal/lang` plugins (matrix is
  go + python only today).
- **C language support** — README/ROADMAP say "C is next"; needs a
  test-convention decision (no standard runner).
- **Brain-gate `--repo-dir` dep binding** — the ROADMAP's one "remaining
  follow-up" (hosted `start_adversarial_run`).
- **`internal/memory/store.go:263`** — the lone code TODO (recency ordering is an
  alphabetical proxy).
- **`corral hooks install`** — biggest strategic item: one-command Claude Code
  onboarding via deterministic hooks telemetry.
