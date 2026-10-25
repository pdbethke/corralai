# Agent setup: wire corral into your coding agents

`corral setup` registers corral's read-only findings server (`corral mcp`) in
the coding agents you pick, and copies the `corral` skill
([`skills/corral/`](../../skills/corral/SKILL.md)) to where each agent loads
skills. After it, you ask your agent about your tests and the agent reads
corral's record, checks findings by execution, and leaves verdicts to you.

```bash
corral setup                    # on a terminal: pick agents from a list
corral setup --client codex --client cursor
corral setup --all-detected     # every agent whose CLI is installed
corral setup --dry-run --all-detected   # print the plan, write nothing
corral setup --uninstall --all-detected # remove only what setup added
```

Nothing is selected for you: name agents with `--client`, take every installed
one with `--all-detected`, or pick on a terminal (detected agents start
unticked; agents already wired start ticked, so on `--uninstall` every wired
agent starts selected). The plan is printed, every command it will run and
every file it will write or back up, before you approve it; `--yes` approves it
for scripts. Ctrl-C stops it: no new step starts, a running agent command is
stopped, and the result lines say what was done.

## What it does, per agent

| Agent | id | Registers with | Skill copied to |
|---|---|---|---|
| Claude Code | `claude_code` | `claude mcp add -s user corral -- <corral> mcp` | `~/.claude/skills/corral` |
| Codex CLI | `codex` | `codex mcp add corral -- <corral> mcp` | `~/.codex/skills/corral` |
| Gemini CLI | `gemini_cli` | `gemini mcp add -s user corral <corral> mcp` | `~/.gemini/skills/corral` |
| Cursor | `cursor` | edits `~/.cursor/mcp.json`, then `cursor-agent mcp enable corral` | `~/.cursor/skills/corral` |
| OpenCode | `opencode` | `opencode mcp add corral -- <corral> mcp` | `$XDG_CONFIG_HOME/opencode/skills/corral` |
| Antigravity | `antigravity` | `agy mcp add corral <corral> mcp` | `~/.gemini/config/skills/corral` |
| GitHub Copilot CLI | `copilot_cli` | `copilot mcp add corral -- <corral> mcp` | `~/.copilot/skills/corral` |
| VS Code | `vscode` | `code --add-mcp '{"name":"corral",…}'` | none (no skill channel found) |

`<corral>` is the absolute path of the binary you ran, so an agent whose PATH
differs from your shell's still finds it. Setup refuses a binary under the
temp directory (a `go run` build) and refuses when `corral` on your PATH is a
different file, naming both.

Every agent command runs with your home directory as its working directory,
not the directory you ran setup from: the agents' list commands also read
project-level config (a repository's `.mcp.json`, `.cursor/mcp.json`, workspace
settings), and setup registers and inspects at user level only.

Where an agent has its own command for a step, setup runs that command. Where
it has none, setup edits the config file itself, backed up first to
`<file>.corral.backup` (the first backup is kept; later ones are
`.corral.backup.<hash>`; the plan names the exact backup file), preserving
every other key, and refusing a file it
cannot parse or a symlinked one. That is Cursor's registration and removal,
OpenCode's removal and VS Code's inspect and removal.

Each skill copy carries a `.corral-setup.json` marker with the hash of every
file setup wrote. A copy without the marker, or with files you edited, is
reported `unmanaged` and never touched. Setup records what it registered in
its state file (`corral state paths` prints it; the result names it too), and
`--uninstall` removes a registration only when that file says setup made it
and the agent still shows the same command. A registration you already removed
by hand has its record dropped, and setup's skill copy is removed with it.
`corral doctor` lists each agent's corral state.

## Limits

- `--uninstall` does not restore a registration of your own that setup
  replaced (you approved a `will replace:` line): it removes setup's, and the
  agent is left with no `corral` server. For the config-file agents the
  pre-edit file is still in its `.corral.backup`.
- Agent home-override variables (`CODEX_HOME`, `CLAUDE_CONFIG_DIR` and the
  like) are inherited by the agent commands, so a registration lands where the
  agent reads it, but setup does not follow them for skill directories or for
  Cursor's `~/.cursor/mcp.json`: with one set, the skill is copied under your
  home directory, where that agent may not look.
- OpenCode is inspected through `opencode debug config`, which prints its whole
  resolved configuration. Setup reads that output in memory to find the
  `corral` entry and never prints or stores it, but it can include provider API
  keys and other servers' environment.

## Support

An agent is **proven** when a headless prompt through it, on this workstation,
loaded the corral skill and called `list_audit_findings`. Setup labels every
other agent `(not yet proven)`. Linux only; nothing here was run on macOS or
Windows.

| Agent | id | Version tested | Proven | Notes |
|---|---|---|---|---|
| Claude Code | `claude_code` | 2.1.309 | yes | loaded the skill unprompted |
| Codex CLI | `codex` | 0.163.0 | yes | loaded the skill unprompted; headless `exec` needs the tool approved (below) |
| Gemini CLI | `gemini_cli` | 0.60.0 | yes | loaded the skill unprompted, on an API key: Google refuses the personal-tier login ("This client is no longer supported for Gemini Code Assist for individuals"), and a user setting of `oauth-personal` wins over `GEMINI_API_KEY`, so the run used a clean home (below) |
| Cursor | `cursor` | 2026.10.22-27dcf5f | yes | loaded the skill unprompted; headless needs `--force` for the MCP call |
| OpenCode | `opencode` | 1.18.25 | yes | loaded the skill unprompted |
| Antigravity | `antigravity` | 1.4.7 | yes | loaded the skill unprompted; headless needs the MCP permission (below) |
| GitHub Copilot CLI | `copilot_cli` | 1.0.103 | yes | loaded the skill only when the prompt named it; two prompts that did not name it called the MCP tool but not the skill |
| VS Code | `vscode` | 1.143.1 | no | setup and verify pass; `code chat` opens a window and has no headless output, so the prompt was not run |

Tested 2026-10-25.

## Evidence

**Setup.** `corral setup --all-detected --yes` on the workstation printed this
plan, then one verified line per agent:

```text
Plan:
  claude_code  register     claude mcp add -s user corral -- /home/<user>/go/bin/corral mcp
  claude_code  write-skill  /home/<user>/.claude/skills/corral
  codex        register     codex mcp add corral -- /home/<user>/go/bin/corral mcp
  codex        write-skill  /home/<user>/.codex/skills/corral
  gemini_cli   register     gemini mcp add -s user corral /home/<user>/go/bin/corral mcp
  gemini_cli   write-skill  /home/<user>/.gemini/skills/corral
  cursor       write-file   /home/<user>/.cursor/mcp.json  add corral to the config file
  cursor       enable       cursor-agent mcp enable corral
  cursor       write-skill  /home/<user>/.cursor/skills/corral
  opencode     register     opencode mcp add corral -- /home/<user>/go/bin/corral mcp
  opencode     write-skill  /home/<user>/.config/opencode/skills/corral
  antigravity  register     agy mcp add corral /home/<user>/go/bin/corral mcp
  antigravity  write-skill  /home/<user>/.gemini/config/skills/corral
  copilot_cli  register     copilot mcp add corral -- /home/<user>/go/bin/corral mcp
  copilot_cli  write-skill  /home/<user>/.copilot/skills/corral
  vscode       register     code --add-mcp {"name":"corral","command":"/home/<user>/go/bin/corral","args":["mcp"]}
  vscode       skip-skill    skill: n/a

Claude Code: verified: registered (inspect shows /home/<user>/go/bin/corral mcp), skill managed, mcp answered
… the same line for Codex CLI, Gemini CLI, Cursor, opencode, Antigravity and GitHub Copilot CLI …
VS Code: verified: registered (inspect shows /home/<user>/go/bin/corral mcp), skill n/a, mcp answered
```

(At the time every line also ended `(not yet proven)`, and a `write-file` line
did not yet name its backup file.)

**The prompt.** Run in an empty scratch git repository, so no project skill or
instruction file was in scope:

```text
A corral test-critic finding says one of my tests "cannot fail".
1. Before I change that test, exactly what should I do to check the claim? Be specific about the steps.
2. Use the corral MCP server's list_audit_findings tool and tell me the id and test_selector of one finding whose test_file is internal/sandbox/container_test.go.
Do not edit any files, and do not run anything that changes files.
```

The first round's last line was "Do not run shell commands and do not edit any
files." Part 1 can only be answered from the skill: it prescribes three
specific bugs to plant (skip the core step, change what the test pins, invert
a condition), and that text exists on the machine only in the installed skill
copies. Part 2 can only be answered by calling the tool: the finding ids and
selectors live in this machine's findings store. A pass needs both, and the
transcript must show the tool call.

| Agent | Command | Result |
|---|---|---|
| Claude Code | `claude -p --output-format stream-json --verbose --no-session-persistence --allowedTools mcp__corral__list_audit_findings < prompt` | pass: `Skill(corral)`, then `mcp__corral__list_audit_findings`; named the three bugs and finding `89:1` / `TestContainerTmpfsAllowsExec` |
| Codex CLI | `codex exec --json --skip-git-repo-check -s read-only -c 'mcp_servers.corral.tools.list_audit_findings.approval_mode="approve"' - < prompt` | pass: read `~/.codex/skills/corral/SKILL.md`, `mcp_tool_call` `list_audit_findings` completed; three bugs, `89:1`. Without the `-c` approval the call fails: "MCP tool call requires approval, but approval policy is never". With shell forbidden it could not open the skill file at all |
| Gemini CLI | in a clean home (`env -i HOME=<scratch>`, scratch `XDG_CONFIG_HOME`/`XDG_CACHE_HOME`): `corral setup --client gemini_cli --yes`, then `security.auth.selectedType` set to `gemini-api-key` in that home's `~/.gemini/settings.json`; then `GEMINI_API_KEY=… gemini -p <prompt> -o stream-json --skip-trust --approval-mode yolo`, with `XDG_STATE_HOME` pointed at the real state directory so `corral mcp` read the real findings store | pass: `activate_skill` (corral), `mcp_corral_list_audit_findings` success; three bugs, `89:1`. Model `auto` (gemini-3.6-flash), 49,602 tokens. On the workstation's own home the prompt could not run: `IneligibleTierError` at login, with the key exported and with `GEMINI_DEFAULT_AUTH_TYPE=gemini-api-key` |
| Cursor | `cursor-agent -p --output-format stream-json --approve-mcps --trust --force <prompt>` | pass: read `~/.cursor/skills/corral/SKILL.md`, `corral-list_audit_findings` succeeded; three bugs, `89:1`. Without `--force` the skill loaded but both MCP calls were rejected |
| OpenCode | `opencode run --format json <prompt>` | pass: `skill` (corral), `corral_list_audit_findings` completed; three bugs, `89:1` |
| Antigravity | `agy -p <prompt> --output-format stream-json --dangerously-skip-permissions` | pass: read `~/.gemini/config/skills/corral/SKILL.md`, `call_mcp_tool` corral/`list_audit_findings` done; three bugs, `89:1`. Without the flag the skill loaded and the MCP call was auto-denied ("headless mode cannot prompt for" the `mcp` permission) |
| GitHub Copilot CLI | `copilot -p "Use the corral skill." + <prompt> --allow-tool corral --allow-tool skill` | pass: `skill(corral)`, `list_audit_findings (MCP: corral)`; three bugs. The same prompt without its first sentence, twice (`--allow-tool corral`, then also `skill` and `shell(cat:*)`), called the tool and returned `85:1` / `85:2` but did not load the skill, and recommended `git checkout`/`git stash` to undo a planted bug, which the skill forbids |
| VS Code | — | not run |

**Uninstall round trip.** `corral setup --uninstall --all-detected --yes`
removed every registration and every skill copy; afterwards each agent's own
inspect command no longer showed corral, both edited JSON files had no
`corral` entry, and `corral doctor` reported every agent "not registered", with
its skill absent (VS Code: `skill n/a`, as it has no skill channel). Cursor's `mcp.json` came back byte-identical. Gemini CLI's
`settings.json` and OpenCode's `opencode.jsonc` were left with an empty
`mcpServers: {}` / `mcp: {}` object. Antigravity's own `agy mcp add`/`remove`
rewrite `~/.gemini/config/mcp_config.json`: keys come back sorted and empty
`args: []` / `env: {}` fields are dropped; every server and value was kept.
Setup was then run again, and the workstation stays wired.

## Adding an agent

An agent is one row in `internal/agentsetup/agents.go`: its CLI, register,
inspect and remove argv, the parser for its inspect output, its skill
directory and restart note. Captured output of each command lives under
`internal/agentsetup/testdata/agents/<id>/` (`scripts/capture-agent-fixtures.sh`).
A row's `Proven` is set only after the headless prompt above passes through
it, and `TestAgentsProvenMatchesDocs` holds the table here to the same set.
