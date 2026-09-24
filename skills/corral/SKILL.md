---
name: corral
description: Use when a user asks whether their tests are any good or actually catch bugs, mentions corral, corral findings, test-critic findings, survivors, proven gaps, kill rate or mutation testing, wants weak or vacuous tests found, or wants an agent to read corral's audit results directly.
compatibility: Needs the corral CLI on PATH (Linux or macOS). Audits need a sandbox (bwrap on Linux) and at least one model provider credential; reading the record needs neither.
---

# corral

corral is an auditing engine for test suites. It plants deliberate faults in the
code, runs the project's real tests against each one inside a sandbox, and
reports what the tests failed to catch. **The number is what execution
measured.** Any model's opinion about a test, including the critic's and
yours, is a claim until something ran it.

Your job is to read the record, check claims by running things, and operate
audits the user asked for. You never grade the tests yourself.

## Step 1: read what corral already found (free, read-only)

Run `corral -h` if unsure of a flag. The record lives in corral, not in git
history, PRs or memory.

| Question | Command |
|---|---|
| What is still open on these files? | `corral brief --scope <path>` (`--changed main` for a branch; `--json` for you) |
| Critic findings awaiting a human | `corral criticscore list`, then `corral criticscore show <id>` |
| Past repo scans, and why a gap count is what it is | `corral scans list`, `corral scans show <id> --evidence` |
| Give an MCP client the findings | `corral mcp` (stdio, read-only) — see below |

## Step 2: a finding is a claim — check it by execution

The test-critic's `evidence` is **UNVERIFIED**; it is sometimes wrong. For each
finding, before changing any test, check the claim it actually makes by running
the test against a temporary bug in the code it calls:

**A `dead-check` finding (one check misses a named fault).** Plant exactly that
fault. If the test stays green, the finding **holds**. If it goes red, it
**does not hold**.

**A `whole-test` finding (the test "cannot fail" / "passes whatever the code
does").** Plant these three bugs, one at a time, in the function the test calls,
and record red or green for each:

1. **Skip the core step.** Return the input or intermediate value without the
   main computation (drop the multiplication, the lookup, the transform).
2. **Change what the test pins.** Alter the constant, table entry or branch
   the test's inputs select (the rate, the threshold, the mapping for that key).
3. **Invert a condition** on the test's path.

Any red means the finding **does not hold**. Only three greens mean it **holds**.
Your report lists all three bugs with red or green.

Judge the claim **as written**. When a test goes red against a "cannot fail"
finding, the check is over: **does not hold**. Do not go on looking for a bug
it misses, and do not restate the finding as a milder claim ("it doesn't test
the non-zero path") that you can confirm. If the test is weak but *can* fail,
report the finding as **does not hold**, and give the weakness separately as
your own observation. A bug the test misses shows only that it misses *that*
bug.

**Planting safely:**
1. Run `git status --short` first and note what is already modified. That is
   the user's work.
2. Plant each bug with your file-edit tool, and undo it with the same tool by
   swapping old and new. Do not use sed, scripts or `git checkout`/`restore`/
   `stash`. Restoring a file also wipes the user's uncommitted edits in it.
3. Afterwards, `git status --short` must match step 1 apart from test changes
   you intend to keep, and no scratch files may be left in the repo.

Report per finding: **holds**, **does not hold** or **could not check**, with
each bug you planted and the result.

Then strengthen tests only for findings that held. Never delete or weaken a
test because a finding called it useless — a disproved finding means the test
stays.

**Adjudication is the human's keystroke.** `corral criticscore confirm|refute
<id> --why "…"` feeds a precision score for the critic model; if you adjudicate,
that score measures you, not a person. Hand the user the exact commands with
your evidence as the `--why`. Run one yourself only when the user has named the
verdict for that specific finding. "Deal with them" or "clean up the list" is
not a verdict.

Never start `corral ui --write`, and never open or use its URL: its launch
token is how the page knows a person started it, and an agent holding it is
exactly what the token exists to prevent.

## Step 3: running an audit (spends money — only when asked)

1. **Free checks first, in this order:** run the project's test command
   yourself on unmodified code, then `corral doctor --code <file>
   --writer-model <m> --mutant-model <m> --critic-model <m> -- <test cmd>`.
   Fix the first FAIL before anything else.
2. **Models.** corral has no defaults. Use the models the user names. If they
   delegate the choice, pick and say what you picked: the critic must differ
   from the writer, and a different **vendor** for the critic is better.
3. **Credentials.** `corral doctor` names the missing key. Stop there and tell
   the user to run `corral secret set <NAME>` (value on stdin) or export the
   variable. **Do not look for keys yourself** in env dumps, dotfiles or other
   tools' config.
4. **Pick the shape:**
   - One file in a real project (anything with imports or config): 
     `corral certify --local --repo-dir . --code <file> --goal "<what it must guarantee>" --writer-model <m> --mutant-model <m> --critic-model <m> --max-tokens <n> -- <test cmd>`
     The bare `--code <file> -- <cmd>` form, without `--repo-dir`, only works on
     a lone file.
   - Whole repo: `corral certify --repo . --dry-run` first (free, lists what
     would be audited), then add `--derive-model <m>` (or `--goals <file>`) plus
     the three seats. `--repo` has **no** `--max-tokens`: bound it with a small
     `--top` and tell the user it is uncapped.
5. **Test command rules:**
   - Scope it to a directory (`python3 -m pytest tests`), never one test file.
     corral adds its own test file beside yours, and a command pinned to one
     file never runs it, so `proven_missed` reads 0 forever.
   - Start it with an interpreter plus a workspace-relative script
     (`node ./node_modules/vitest/vitest.mjs run`), never `./node_modules/.bin/x`.
   - The sandbox sees toolchains installed under `/usr`, not `--user` installs.
     It has no network.
6. **Never pass `--substrate workspace`** unless the user explicitly asks and
   the checkout is disposable (a CI runner). It mutates the real tree.

## Reporting results

Quote corral's words: the verdict (`certified` / `needs-review`), the kill rate,
survivors, and proven gaps (with the authored test that closes each). If corral
could not grade a file, say that. It is not a zero.

## MCP setup (any agent that reads `.mcp.json`)

```json
{ "mcpServers": { "corral": { "command": "corral", "args": ["mcp"] } } }
```

Tools: `list_audit_findings`, `get_audit_finding`. Both are read-only, and
there is deliberately no adjudication tool.

## Red flags — stop

| Thought | Reality |
|---|---|
| "The finding says the test can't fail, so remove it" | Try to make it fail first; findings are sometimes wrong |
| "I found a bug the test misses, so the 'cannot fail' claim holds" | Missing one bug is not "cannot fail"; look for a bug that turns it red |
| "It went red, but that bug isn't the discriminating case — the finding's real point is…" | Red ends the check: does not hold. Report the weakness as your own note |
| "They said clean up the list, so refute the noise" | Hand them the commands; the verdict is theirs |
| "`git checkout --` the file, quickest way to undo my bug" | It also wipes the user's uncommitted edits. Undo with the inverse edit |
| "No key in env — let me look around for one" | Stop and ask |
| "I'll pin the command to the test file to be fast" | The authored test never runs; gaps read 0 |
| "Can't find the findings — I'll ask where they are" | `corral brief`, `corral criticscore list` |
| "I'll start `corral ui --write` so they can click through the verdicts" | The token is the person's. Tell them to run it themselves |
