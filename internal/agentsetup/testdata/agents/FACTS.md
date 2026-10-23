# Agent facts, established by execution

Captured 2026-10-23 on the workstation by `scripts/capture-agent-fixtures.sh`
(re-runnable; header explains). Every command ran in a scratch HOME with
`HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`
under it; cwd = that HOME. Never the operator's real configs.

Fixtures are raw stdout+stderr with two mechanical substitutions: the scratch
dir -> `__SCRATCH__`, the corral binary path -> `__CORRAL__`. Each fixture
`<name>.txt` has `<name>.rc` (exit code) beside it. Fixture files per agent:
`inspect-absent` (before), `register`, `inspect-present`, `remove`,
`inspect-removed` (after remove), plus extras named below.

Registered server: name `corral`, command `__CORRAL__`, args `mcp`.
No CLI below needed login or network for mcp add/list/get/remove, and none
prompted when stdin was closed (`</dev/null`) -- except where noted.

Cross-cutting facts the parsers must respect:
- `get`/`list` on claude, gemini, opencode and cursor START the server
  (`corral mcp`) to health-check it, so inspect is not free and its status text
  depends on the server actually starting. Presence is the name line; do not
  key on "Connected".
- Duplicate `add`: claude and copilot REFUSE (rc 1); codex, gemini, agy,
  opencode and vscode silently overwrite (rc 0). So setup must inspect first.
- `remove` of a missing server: rc 1 on claude, agy, copilot; rc 0 on codex and
  gemini. Inspect rc is 1 when absent on claude and copilot (`get`), 0 on every
  `list`-style inspect.
- ANSI escapes are emitted by opencode even with stdout not a tty (fixtures
  keep them; strip `\x1b\[[0-9;]*m` before parsing).

| id | version (`--version`) |
|---|---|
| claude_code | 2.1.309 (Claude Code) |
| codex | codex-cli 0.163.0 |
| gemini_cli | 0.60.0 |
| cursor | 2026.08.31-4057e58 |
| opencode | 1.18.25 |
| antigravity | 1.4.2 (`agy`) |
| copilot_cli | GitHub Copilot CLI 1.0.68 (prints a 2nd line "Run 'copilot update'...") |
| vscode | 1.143.0 (3 lines: version, commit, arch) |

## claude_code
- Register: `claude mcp add -s user corral -- <corral> mcp` (default scope is `local`, so `-s user` is required). rc 0.
- Inspect: `claude mcp get corral` (absent: rc 1, `No MCP server named "corral". Run ...`). `claude mcp list` also works (`list-absent`, `list-present`).
- Remove: `claude mcp remove -s user corral`. Afterwards inspect == absent (byte-identical).
- Config file it edits: `~/.claude.json` key `mcpServers.corral` = `{type:"stdio",command,args:["mcp"],env:{}}`.
- Plugin route (Step 1b, `plugin-*` fixtures): `claude plugin marketplace add pdbethke/corralai` (clones `git@github.com:pdbethke/corralai.git` over SSH -- needs a working GitHub SSH key or it fails; recorded in `plugin-marketplace-add.txt`), `claude plugin install corral@corralai` (scope user), inspect `claude plugin list` / `claude plugin list --json`, remove `claude plugin uninstall corral@corralai`. Absent: `No plugins installed. Use \`claude plugin install\` to install a plugin.` Present: block with `❯ corral@corralai`, `Version: <12-hex>`, `Scope: user`, `Status: ✔ enabled`. The `--json` form has `id`, `enabled`, and `mcpServers.corral = {command:"corral",args:["mcp"]}` -- the plugin registers its OWN `corral` MCP server (bare `corral mcp`), so with the plugin installed `claude mcp add corral` is redundant and duplicates it.
- Skill directory: `~/.claude/skills/<name>/SKILL.md`. Source: Claude Code's documented location; NOT executed here (no `claude skills list` verb in 2.1.309). OpenCode's `debug skill` independently lists `~/.claude/skills` as a root.
- Restart: running sessions do not pick up a new server; start a new session.

## codex
- Register: `codex mcp add corral -- <corral> mcp`. rc 0, `Added global MCP server 'corral'.`
- Inspect: `codex mcp get corral` (absent: rc 1 `Error: No MCP server named 'corral' found.`); `codex mcp list` and `codex mcp list --json` (`list-present-json.txt`: `[{name,enabled,transport:{type:"stdio",command,args,...},auth_status}]`; absent list prints `No MCP servers configured yet. ...`, not `[]`, when not --json).
- Remove: `codex mcp remove corral` (`Removed global MCP server 'corral'.`; missing -> rc 0 `No MCP server named 'nope' found.`). Inspect afterwards == absent.
- File: `~/.codex/config.toml` `[mcp_servers.corral]` `command`, `args` (`config-present.toml`).
- Every codex call prints a `WARNING: proceeding, even though we could not create PATH aliases: Refusing to create helper binaries under temporary dir "/tmp" ...` first line. It is an artifact of the scratch HOME being under /tmp (a real home is not); kept in the fixtures because they are raw. Parsers must tolerate a leading WARNING line.
- Skill directory: BOTH `~/.agents/skills/<name>/SKILL.md` and `~/.codex/skills/<name>/SKILL.md` are scope `user`. Executed: `codex app-server` JSON-RPC `skills/list` in the scratch HOME returned a skill from each. Prefer `~/.agents/skills` (shared).
- Restart: new session.

## gemini_cli
- Register: `gemini mcp add -s user corral <corral> mcp` (default scope `project`; `-s user` required). rc 0.
- Inspect: `gemini mcp list` (no `get`). Absent: `No MCP servers configured.` Present in an UNTRUSTED folder (a fresh scratch HOME is untrusted): a 2-line warning, then `○ corral: <cmd> (stdio) - Disabled`. In a trusted folder (`GEMINI_CLI_TRUST_WORKSPACE=true`, `inspect-present-trusted.txt`): `✓ corral: <cmd> (stdio) - Connected`. Presence = a line `corral: ` after the glyph.
- Remove: `gemini mcp remove -s user corral`. Afterwards inspect == absent. File keeps `{"mcpServers":{}}`.
- Duplicate add: rc 0, prints both `already configured` and `updated`.
- File: `~/.gemini/settings.json` `mcpServers.corral = {command,args}`.
- Skill directory: `~/.gemini/skills/<name>/SKILL.md` and `~/.agents/skills/<name>/SKILL.md` (executed: `gemini skills list` listed both; it did NOT list `~/.claude/skills`). Prefer `~/.agents/skills`.
- Restart: new session.

## cursor
- No `mcp add`. Setup writes `~/.cursor/mcp.json` itself: `{"mcpServers":{"corral":{"command":"<corral>","args":["mcp"]}}}` (`mcp.json`), then runs `cursor-agent mcp enable corral` (rc 0, `✓ Enabled and approved MCP server: corral`; no login needed).
- Inspect: `cursor-agent mcp list`. Absent/no file or `{}` or `{"mcpServers":{}}`: `No MCP servers configured (expected in .cursor/mcp.json or ~/.cursor/mcp.json)`. File present, not yet enabled: `corral: not loaded (needs approval)` (`inspect-unapproved`). After enable: `corral: ready`. After `cursor-agent mcp disable corral`: `corral: disabled`. `mcp list-tools corral` prints `Tools for corral (2):` (`list-tools.txt`).
- Remove: edit `mcp.json` (drop the key; Cursor has no remove). Afterwards inspect == absent. `enable` approval is stored by the agent at `~/.cursor/projects/<cwd-slug>/mcp-approvals.json` as `corral-<16hex>`; `disable` writes `mcp-disabled.json` there. Observed: a different cwd under the same HOME also showed `ready`, so do not assume approval is per directory, but do not assume it is global either.
- Skill directory: `~/.cursor/skills/<name>/SKILL.md`. Source: strings in the cursor-agent bundle (roots `.cursor/skills/`, `.claude/skills/`, `.codex/skills/`, `.agents/skills/`); NOT executed (no list verb).
- Restart: new Cursor session / agent run.

## opencode
- Register: `opencode mcp add corral -- <corral> mcp` (non-interactive when the command follows `--`; with neither `--url` nor a command it errors `Provide either --url <url> or a command after --`, rc 1, `register-missing-command.txt`). rc 0.
- Inspect: `opencode mcp list` (aliases `ls`). Present: `● ✓ corral connected` + command line; absent: `▲ No MCP servers configured`. `opencode debug config` prints the resolved config as JSON with `mcp.corral = {type:"local",command:[<corral>,"mcp"]}` (`config-present.txt`) -- the machine-readable inspect.
- Remove: NO remove command (`mcp` has add, list, auth, logout, debug only). Setup must edit the config file; Inspect afterwards == absent.
- File it writes: `$XDG_CONFIG_HOME/opencode/opencode.jsonc` (default `~/.config/opencode/opencode.jsonc`) when none exists; if `opencode.json` already exists it extends THAT file and keeps other keys (`register-into-json.txt`, `opencode-existing.json`). The output is JSON even in `.jsonc`; hand edits there may contain comments, so removal must preserve them or refuse.
- Skill directory: executed with `opencode debug skill`: `~/.agents/skills`, `~/.claude/skills`, `$XDG_CONFIG_HOME/opencode/skills` and `.../opencode/skill` are all discovered (user). Prefer `~/.agents/skills`.
- Restart: new session.

## antigravity (`agy`)
- Register: `agy mcp add corral <corral> mcp` (flags must precede name; no prompt). rc 0, `Added MCP server "corral" (stdio)`. "Add or update": duplicate overwrites.
- Inspect: `agy mcp list` -> table `NAME TYPE STATUS COMMAND/URL`, row `corral  stdio  enabled  <cmd> mcp`; absent: `No MCP servers configured.`
- Remove: `agy mcp remove corral` (`Removed MCP server "corral"`; missing -> rc 1 `Error: MCP server "nope" not found`). Inspect afterwards == absent. Also has `enable` / `disable`.
- File: `~/.gemini/config/mcp_config.json` `mcpServers.corral = {command,args,disabled:false}` (`mcp_config.json`).
- Skill directory: `~/.gemini/config/skills/<name>/SKILL.md` -- CONFIRMED by the agy 1.4.2 binary's own embedded changelog (migration table: global workflows -> `~/.gemini/config/skills/<name>/SKILL.md`; workspace -> `<workspace>/.agents/skills/<name>/SKILL.md`). Matches REA. Not executed (no non-interactive list verb); `~/.agents/skills` as a USER root was not established.
- Restart: new session.

## copilot_cli
- Register: `copilot mcp add corral -- <corral> mcp`. rc 0, prints `Added server "corral"` then a detail block. Duplicate: rc 1 `Error: Server "corral" already exists. To update it, remove it first`.
- Inspect: `copilot mcp get corral` (absent rc 1: `Error: Server "corral" not found.` + `Available servers:` list); `copilot mcp list` (`User servers:` / `  corral (local)`; absent: `No MCP servers configured.` + hints).
- Remove: `copilot mcp remove corral` (`Removed server "corral"`; missing rc 1). Inspect afterwards == absent.
- File: `~/.copilot/mcp-config.json` `mcpServers.corral = {tools:["*"],type:"local",command,args}`.
- Skill directory: `~/.copilot/skills/<name>/SKILL.md` or `~/.agents/skills/<name>/SKILL.md` ("Personal" in copilot's own `/skills` help text, from its bundle); project `.github/skills`, `.agents/skills`, `.claude/skills`. Not executed.
- Restart: new session.

## vscode
- Register: `code --user-data-dir <dir> --add-mcp '{"name":"corral","command":"<corral>","args":["mcp"]}'` (drop `--user-data-dir` for the real profile). Runs in the CLI process: no window, works with DISPLAY unset; rc 0, `Added MCP servers: corral`. Note `code` is the snap here.
- Inspect: NO command (`code --help` has only `--add-mcp`). Read `<user-data-dir>/User/mcp.json` (real Linux path `~/.config/Code/User/mcp.json`; macOS `~/Library/Application Support/Code/User/mcp.json`). Absent state is the file not existing (`cat: ...: No such file or directory`, rc 1).
- File written (tab-indented): `{"servers":{"corral":{"command":...,"args":["mcp"]}},"inputs":[]}` -- no `type` key (`mcp.json`). Duplicate add overwrites.
- Remove: NO command; edit the file. After removal the file stays as `{"servers":{},"inputs":[]}`, so "removed" differs from "absent" textually (`inspect-removed.txt` vs `inspect-absent.txt`) though equivalent; compare parsed state, not bytes.
- Skill directory: none found by execution. VS Code is an MCP host here; treat as no skill channel (`n/a`) unless later established.
- Restart: reload the window / start the server from the MCP list.
