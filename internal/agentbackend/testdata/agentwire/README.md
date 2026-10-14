<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Agent-wire fixtures

Real output from the real CLIs, captured 2026-10-13 on the development
workstation, so the wire parsers in `agentwire.go` are tested against what the
CLIs actually print rather than against what we assume they print. Every
capture ran in an empty temp directory (never a repository), on subscription
(OAuth) auth, with a one-line prompt.

CLI versions: `claude --version` → `2.1.296 (Claude Code)`;
`codex --version` → `codex-cli 0.160.0`.

## Commands

The claude lean flags (the `claude-code` built-in definition, certify form):

    LEAN='--setting-sources "" --strict-mcp-config --disable-slash-commands --no-session-persistence --system-prompt "Follow the instructions in the user'"'"'s message exactly."'

| fixture | command | exit |
|---|---|---|
| `claude-json-ok.json` | `echo 'Reply with the single word: ok' \| claude -p --output-format json $LEAN --tools ""` | 0 |
| `claude-json-schema.json` | `echo 'Reply with the single word ok as the answer.' \| claude -p --output-format json $LEAN --tools "" --json-schema '{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}'` | 0 |
| `claude-json-error.json` | `echo 'Reply with the single word: ok' \| claude -p --output-format json $LEAN --tools "" --model no-such-model` | **1** |
| `codex-ok.jsonl` | `echo 'Reply with the single word: ok' \| codex exec --json --ignore-user-config --ignore-rules --sandbox read-only --ephemeral --skip-git-repo-check -C <tmp> -o <out> -` | 0 |
| `codex-schema.jsonl` | the same, prompt `Reply with the single word ok as the answer.`, plus `--output-schema <file holding the schema above>` | 0 |
| `codex-error.jsonl` | the same as ok, plus `-m no-such-model` | **1** |

The `-o` file's content, which the parser reads in preference to the event
stream:

- `codex-ok.jsonl` → `ok`
- `codex-schema.jsonl` → `{"answer":"ok"}`
- `codex-error.jsonl` → the file was never written (corral pre-creates it empty).

stderr on the error runs: claude printed
`[claude-code:unrecognized_model] {"model":"no-such-model","query_source":"sdk"}`;
codex printed nothing.

## What was observed

- **claude, schema:** the structured value is in BOTH `.structured_output` (an
  object) and `.result` (its JSON text). The parser prefers
  `.structured_output`. `stop_reason` is `tool_use` and `num_turns` is 2 even
  with `--tools ""`: the schema rides an internal tool.
- **claude, error:** `is_error: true`, `terminal_reason: "api_error"`,
  `api_error_status: 404`, and the error text in `.result` — while `subtype`
  still says `"success"`. So `subtype` is NOT a success signal; `is_error` and
  `terminal_reason` are. The CLI exited 1 here. An `is_error` document arriving
  with exit 0 has not been observed; the tests feed this same captured document
  through the exit-0 path, so the classifier's refusal to read `.result` as a
  reply does not depend on the exit status.
- **codex, error:** an `item.completed` whose item is `{"type":"error"}` is a
  WARNING (it said "Defaulting to fallback metadata" and the turn went on); the
  failure is the top-level `{"type":"error"}` event and the `turn.failed` event
  that follows it. Exit status 1. The same exit-0 rule applies: the tests feed
  the captured stream through the exit-0 path too.
- **codex usage:** `input_tokens` already contains `cached_input_tokens` (14,728
  total, 12,288 cached on the ok run), the OpenAI convention — no normalising.

## Lean-seat measurements (input tokens for a one-word reply)

These are the figures measured at fixture capture. The current ones are held by
`TestBuiltinSeatsStartCold` (`agentseat_real_test.go`).

| seat form | input tokens (input + cache write + cache read) |
|---|---|
| claude, lean flags, `--tools ""`, NO `--system-prompt` | 2,972 (2 + 2,435 + 535) |
| claude, lean flags, `--tools ""`, `--system-prompt` (the chosen form) | **620** (2 + 618 + 0) |
| claude, chosen form with `--tools Read,Grep,Glob` (review), a 3-turn read of one file | 10,178 over 3 turns; it found and read the file |
| codex, lean flags | **14,728** (12,288 cached) |
| codex, lean flags + `--output-schema` | 14,277 (0 cached) |

For comparison, the spec's unflagged `claude -p` carried 108,740.

## Scrubbed

`session_id` and `uuid` (claude) and `thread_id` (codex) were replaced with
`00000000-0000-0000-0000-000000000000`. Nothing else in the captures named the
machine or the account: no home path, cwd, email, account or organisation id
appeared in any of them (checked by grep before committing). Everything else is
byte-for-byte as the CLI printed it.
