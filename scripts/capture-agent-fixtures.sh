#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-agent-fixtures.sh [SCRATCH] — runs each coding agent's real
# MCP register / inspect / remove commands for the server `corral`
# (command: `<corral> mcp`) and writes what each printed into
# internal/agentsetup/testdata/agents/<id>/.
#
# Why: `corral setup` parses these CLIs' output. A parser tested against output
# we typed is tested against what we expect the agent to print. These fixtures
# are what the agents printed (the same rule as capture-runner-fixtures.sh).
# The argv below is the one internal/agentsetup's agent table must use; keep
# them equal, and see testdata/agents/FACTS.md for what each run established.
#
# SCRATCH HOME: every agent runs with HOME and XDG_CONFIG_HOME / XDG_DATA_HOME /
# XDG_STATE_HOME / XDG_CACHE_HOME pointed at a fresh directory under SCRATCH
# (default: a mktemp dir, removed on exit). This script never reads or writes
# the operator's real ~/.claude, ~/.codex, ~/.gemini, ~/.cursor,
# ~/.config/opencode, ~/.copilot, ~/.antigravity or VS Code profile. VS Code
# additionally gets --user-data-dir inside the scratch HOME, no DISPLAY, so no
# window can open.
#
# ENVIRONMENT: every agent call runs under `env -i` with an allowlist (PATH,
# HOME, XDG_* with XDG_RUNTIME_DIR in the scratch too -- the snap `code`
# wrapper needs one -- TERM, LANG), so no inherited agent override variable
# (CLAUDE_CONFIG_DIR, CODEX_HOME, GEMINI_CLI_HOME, COPILOT_HOME,
# OPENCODE_CONFIG, OPENCODE_CONFIG_DIR, ...) and no vendor API key can redirect
# a write to the operator's real config or spend their credentials. The one
# exception is the claude plugin marketplace clone, which gets SSH_AUTH_SOCK
# only if the https clone below is refused (see FACTS.md).
#
# Fixtures are RAW stdout+stderr, except two mechanical substitutions so they
# are machine independent: the OS user name becomes `__USER__`, the scratch directory becomes `__SCRATCH__` and the
# corral binary becomes `__CORRAL__` (the binary lives under the scratch dir,
# so it is substituted first). Nothing else is edited. Exit codes go beside
# each fixture as <name>.rc.
#
# Needs: go, and each agent CLI on PATH (claude codex gemini cursor-agent
# opencode agy copilot code). The claude plugin step needs the network
# (`claude plugin marketplace add pdbethke/corralai` clones from GitHub).
# An agent whose CLI is missing is skipped with a message, not an error.
set -uo pipefail
cd "$(dirname "$0")/.."
REPO="$PWD"
OUT="$REPO/internal/agentsetup/testdata/agents"

KEEP=0
SCRATCH="${1:-}"
if [ -z "$SCRATCH" ]; then SCRATCH="$(mktemp -d)"; trap 'rm -rf "$SCRATCH"' EXIT; else KEEP=1; mkdir -p "$SCRATCH"; fi
SCRATCH="$(cd "$SCRATCH" && pwd)"
mkdir -p "$SCRATCH/bin"
USER_NAME="$(id -un)"
CORRAL="$SCRATCH/bin/corral"
go build -o "$CORRAL" ./cmd/corral

# newhome ID: a fresh scratch HOME for one run; cwd is that HOME.
newhome() {
	H="$SCRATCH/home/$1"
	rm -rf "$H"
	mkdir -p "$H"/{config,data,state,cache}
	mkdir -m 700 "$H/run"
	export HOME="$H" XDG_CONFIG_HOME="$H/config" XDG_DATA_HOME="$H/data" \
		XDG_STATE_HOME="$H/state" XDG_CACHE_HOME="$H/cache"
	unset DISPLAY WAYLAND_DISPLAY
	ENVCMD=(env -i "PATH=$PATH" "HOME=$HOME" "XDG_CONFIG_HOME=$XDG_CONFIG_HOME" "XDG_DATA_HOME=$XDG_DATA_HOME" \
		"XDG_STATE_HOME=$XDG_STATE_HOME" "XDG_CACHE_HOME=$XDG_CACHE_HOME" "XDG_RUNTIME_DIR=$H/run" "TERM=${TERM:-dumb}" "LANG=${LANG:-C.UTF-8}")
	cd "$H"
}

# cap ID NAME cmd...: run cmd with a timeout, stdin closed, stdout+stderr to
# $OUT/ID/NAME.txt after the two substitutions, exit code to NAME.rc.
cap() {
	local id="$1" name="$2"
	shift 2
	mkdir -p "$OUT/$id"
	timeout 60 "${ENVCMD[@]}" "$@" </dev/null >"$OUT/$id/$name.txt" 2>&1
	echo $? >"$OUT/$id/$name.rc"
	# Codex warns on every call when its home is under /tmp (our scratch is);
	# real homes are not, so the warning is an artifact of the scratch, not of
	# codex's output. It is kept in the fixture (raw) and FACTS.md names it.
	sed -i -e "s#$CORRAL#__CORRAL__#g" -e "s#$SCRATCH#__SCRATCH__#g" -e "s#\"username\": \"$USER_NAME\"#\"username\": \"__USER__\"#" "$OUT/$id/$name.txt"
}

have() { command -v "$1" >/dev/null 2>&1 || { echo "skip $2: $1 not on PATH" >&2; return 1; }; }

# version ID cmd...: the CLI's own --version.
version() {
	local id="$1"
	shift
	mkdir -p "$OUT/$id"
	timeout 30 "${ENVCMD[@]}" "$@" --version </dev/null >"$OUT/$id/versions.txt" 2>&1
	sed -i -e "s#$CORRAL#__CORRAL__#g" -e "s#$SCRATCH#__SCRATCH__#g" "$OUT/$id/versions.txt"
}

# cycle ID INSPECT-CMD... — shape shared by every agent: the caller defines
# register_$ID and remove_$ID; inspect is the argv after "--".
cycle() {
	local id="$1"
	shift
	cap "$id" inspect-absent "$@"
	"register_$id"
	cap "$id" inspect-present "$@"
	"remove_$id"
	cap "$id" inspect-removed "$@"
	if cmp -s "$OUT/$id/inspect-absent.txt" "$OUT/$id/inspect-removed.txt"; then
		echo "$id: removed state == absent state"
	else
		echo "$id: removed state DIFFERS from absent state (see FACTS.md)"
	fi
}

# register_*/remove_* are not captured as fixtures except the register and
# remove messages themselves (register.txt / remove.txt), which setup also
# reads for its own error reporting.

# ---- claude_code ----------------------------------------------------------
if have claude claude_code; then
	newhome claude_code
	version claude_code claude
	register_claude_code() { cap claude_code register claude mcp add -s user corral -- "$CORRAL" mcp; }
	remove_claude_code() { cap claude_code remove claude mcp remove -s user corral; }
	cycle claude_code claude mcp get corral
	cap claude_code list-absent claude mcp list; register_claude_code; cap claude_code list-present claude mcp list
	remove_claude_code
	# duplicate registration: claude refuses (rc 1).
	register_claude_code; cap claude_code register-duplicate claude mcp add -s user corral -- "$CORRAL" mcp
	cap claude_code remove-missing claude mcp remove -s user nope

	# Step 1b: the plugin route. The plugin ships its own `corral` MCP server
	# (command `corral`, args `mcp`), so with it installed `claude mcp add` is
	# redundant and would duplicate it.
	newhome claude_plugin
	cap claude_code plugin-absent claude plugin list
	cap claude_code plugin-marketplace-add claude plugin marketplace add https://github.com/pdbethke/corralai.git
	if [ "$(cat "$OUT/claude_code/plugin-marketplace-add.rc")" != 0 ]; then
		echo "claude_code: https marketplace add refused; retrying with SSH_AUTH_SOCK for this step only" >&2
		ENVCMD+=("SSH_AUTH_SOCK=${SSH_AUTH_SOCK:-}")
		cap claude_code plugin-marketplace-add-ssh claude plugin marketplace add pdbethke/corralai
	fi
	cap claude_code plugin-install claude plugin install corral@corralai
	cap claude_code plugin-present claude plugin list
	cap claude_code plugin-present-json claude plugin list --json
	cap claude_code plugin-uninstall claude plugin uninstall corral@corralai
	cap claude_code plugin-removed claude plugin list
fi

# ---- codex ----------------------------------------------------------------
if have codex codex; then
	newhome codex
	version codex codex
	register_codex() { cap codex register codex mcp add corral -- "$CORRAL" mcp; }
	remove_codex() { cap codex remove codex mcp remove corral; }
	cycle codex codex mcp get corral
	cap codex list-absent codex mcp list; register_codex
	cap codex list-present codex mcp list
	cap codex list-present-json codex mcp list --json
	cat "$HOME/.codex/config.toml" | sed -e "s#$CORRAL#__CORRAL__#g" >"$OUT/codex/config-present.toml"
	remove_codex
	register_codex; cap codex register-duplicate codex mcp add corral -- "$CORRAL" mcp; remove_codex
	cap codex remove-missing codex mcp remove nope
fi

# ---- gemini_cli -----------------------------------------------------------
if have gemini gemini_cli; then
	newhome gemini_cli
	version gemini_cli gemini
	register_gemini_cli() { cap gemini_cli register gemini mcp add -s user corral "$CORRAL" mcp; }
	remove_gemini_cli() { cap gemini_cli remove gemini mcp remove -s user corral; }
	cycle gemini_cli gemini mcp list
	# `mcp list` marks servers Disabled in an untrusted folder (a fresh scratch
	# HOME is untrusted); a trusted folder shows Connected. Both are fixtures.
	register_gemini_cli
	cap gemini_cli inspect-present-trusted env GEMINI_CLI_TRUST_WORKSPACE=true gemini mcp list
	remove_gemini_cli
	register_gemini_cli; cap gemini_cli register-duplicate gemini mcp add -s user corral "$CORRAL" mcp; remove_gemini_cli
	cap gemini_cli remove-missing gemini mcp remove -s user nope
fi

# ---- cursor ---------------------------------------------------------------
# No `mcp add`: setup writes ~/.cursor/mcp.json itself, then `mcp enable`.
if have cursor-agent cursor; then
	newhome cursor
	version cursor cursor-agent
	register_cursor() {
		mkdir -p "$HOME/.cursor"
		printf '{\n  "mcpServers": {\n    "corral": {\n      "command": "%s",\n      "args": ["mcp"]\n    }\n  }\n}\n' "$CORRAL" >"$HOME/.cursor/mcp.json"
		cap cursor register cursor-agent mcp enable corral
	}
	remove_cursor() { echo '{}' >"$HOME/.cursor/mcp.json"; }
	cycle cursor cursor-agent mcp list
	register_cursor
	cap cursor list-tools cursor-agent mcp list-tools corral
	sed -e "s#$CORRAL#__CORRAL__#g" "$HOME/.cursor/mcp.json" >"$OUT/cursor/mcp.json"
	remove_cursor
	# the file written for an unapproved server: the state before `enable`.
	register_cursor_unapproved() {
		mkdir -p "$HOME/.cursor"
		printf '{\n  "mcpServers": {\n    "corral": {\n      "command": "%s",\n      "args": ["mcp"]\n    }\n  }\n}\n' "$CORRAL" >"$HOME/.cursor/mcp.json"
	}
	newhome cursor_unapproved
	register_cursor_unapproved
	cap cursor inspect-unapproved cursor-agent mcp list
	cap cursor disable cursor-agent mcp disable corral
	cap cursor inspect-disabled cursor-agent mcp list
fi

# ---- opencode -------------------------------------------------------------
# `mcp add` takes `-- <command...>` non-interactively; there is no `mcp remove`.
if have opencode opencode; then
	newhome opencode
	version opencode opencode
	register_opencode() { cap opencode register opencode mcp add corral -- "$CORRAL" mcp; }
	remove_opencode() {
		python3 - "$XDG_CONFIG_HOME/opencode/opencode.jsonc" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d.get("mcp", {}).pop("corral", None)
json.dump(d, open(p, "w"), indent=2)
PY
	}
	cycle opencode opencode mcp list
	register_opencode
	cap opencode config-present opencode debug config
	sed -e "s#$CORRAL#__CORRAL__#g" "$XDG_CONFIG_HOME/opencode/opencode.jsonc" >"$OUT/opencode/opencode.jsonc"
	cap opencode register-duplicate opencode mcp add corral -- /usr/bin/true mcp
	cap opencode inspect-after-duplicate opencode mcp list
	register_opencode
	cap opencode register-missing-command opencode mcp add corral2
	remove_opencode
	# an existing opencode.json (not .jsonc) is the file `mcp add` extends.
	newhome opencode_json
	mkdir -p "$XDG_CONFIG_HOME/opencode"
	echo '{"$schema":"https://opencode.ai/config.json","theme":"x","mcp":{"other":{"type":"local","command":["true"]}}}' >"$XDG_CONFIG_HOME/opencode/opencode.json"
	cap opencode register-into-json opencode mcp add corral -- "$CORRAL" mcp
	sed -e "s#$CORRAL#__CORRAL__#g" "$XDG_CONFIG_HOME/opencode/opencode.json" >"$OUT/opencode/opencode-existing.json"
fi

# ---- antigravity ----------------------------------------------------------
if have agy antigravity; then
	newhome antigravity
	version antigravity agy
	register_antigravity() { cap antigravity register agy mcp add corral "$CORRAL" mcp; }
	remove_antigravity() { cap antigravity remove agy mcp remove corral; }
	cycle antigravity agy mcp list
	register_antigravity
	cap antigravity register-duplicate agy mcp add corral /usr/bin/true mcp
	cap antigravity inspect-after-duplicate agy mcp list
	register_antigravity
	sed -e "s#$CORRAL#__CORRAL__#g" "$HOME/.gemini/config/mcp_config.json" >"$OUT/antigravity/mcp_config.json"
	remove_antigravity
	cap antigravity remove-missing agy mcp remove nope
fi

# ---- copilot_cli ----------------------------------------------------------
if have copilot copilot_cli; then
	newhome copilot_cli
	version copilot_cli copilot
	register_copilot_cli() { cap copilot_cli register copilot mcp add corral -- "$CORRAL" mcp; }
	remove_copilot_cli() { cap copilot_cli remove copilot mcp remove corral; }
	cycle copilot_cli copilot mcp get corral
	cap copilot_cli list-absent copilot mcp list
	register_copilot_cli
	cap copilot_cli list-present copilot mcp list
	sed -e "s#$CORRAL#__CORRAL__#g" "$HOME/.copilot/mcp-config.json" >"$OUT/copilot_cli/mcp-config.json"
	cap copilot_cli register-duplicate copilot mcp add corral -- "$CORRAL" mcp
	remove_copilot_cli
	cap copilot_cli remove-missing copilot mcp remove nope
fi

# ---- vscode ---------------------------------------------------------------
# `code --add-mcp` writes <user-data-dir>/User/mcp.json from the CLI process
# (no window). There is no inspect or remove command: inspect = read the file,
# remove = edit it. Real location on Linux: ~/.config/Code/User/mcp.json.
if have code vscode; then
	newhome vscode
	version vscode code
	UD="$HOME/ud"
	register_vscode() {
		cap vscode register code --user-data-dir "$UD" --extensions-dir "$HOME/ext" \
			--add-mcp "{\"name\":\"corral\",\"command\":\"$CORRAL\",\"args\":[\"mcp\"]}"
	}
	remove_vscode() {
		python3 - "$UD/User/mcp.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d.get("servers", {}).pop("corral", None)
json.dump(d, open(p, "w"), indent="\t")
PY
	}
	cycle vscode cat "$UD/User/mcp.json"
	register_vscode
	cap vscode register-duplicate code --user-data-dir "$UD" --extensions-dir "$HOME/ext" \
		--add-mcp '{"name":"corral","command":"/usr/bin/true","args":["mcp"]}'
	cat "$UD/User/mcp.json" >"$OUT/vscode/mcp-after-duplicate.json"
	register_vscode
	sed -e "s#$CORRAL#__CORRAL__#g" "$UD/User/mcp.json" >"$OUT/vscode/mcp.json"
fi

echo "fixtures written under $OUT (scratch: $SCRATCH)"
