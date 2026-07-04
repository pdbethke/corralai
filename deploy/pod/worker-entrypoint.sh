#!/bin/sh
# SPDX-License-Identifier: Elastic-2.0
#
# Pod worker entrypoint. Before handing off to corral-harness it:
#   1. copies read-only seed creds (/seed/<vendor>) into writable homes so the
#      vendor CLI can refresh its own tokens without touching the box's creds;
#   2. writes the per-vendor MCP config that points the "corral" server at the
#      auth-proxy ($CORRAL_BRAIN) — tokenless, because the proxy adds the bearer.
#
# CORRAL_BRAIN is the proxy (e.g. http://auth-proxy:9019). corral-harness turns
# it into <brain>/mcp/ for the {mcp_config} it hands Claude/Copilot; Gemini and
# Codex read their own config files, written here.
set -eu

seed() {  # seed <src-under-/seed> <dest-home-dir>
  if [ -d "/seed/$1" ]; then
    mkdir -p "$2"
    cp -a "/seed/$1/." "$2/" 2>/dev/null || true
  fi
}

seed claude   "$HOME/.claude"
seed gemini   "$HOME/.gemini"
seed codex    "$HOME/.codex"
seed copilot  "$HOME/.copilot"

brain_mcp="$(printf '%s' "${CORRAL_BRAIN%/}/mcp/")"

# Gemini: reads MCP servers from ~/.gemini/settings.json.
if command -v gemini >/dev/null 2>&1; then
  mkdir -p "$HOME/.gemini"
  cat > "$HOME/.gemini/settings.json" <<EOF
{ "mcpServers": { "corral": { "httpUrl": "$brain_mcp" } } }
EOF
fi

# Codex: reads MCP servers from ~/.codex/config.toml. Append the corral entry if
# absent (don't clobber a copied auth config).
if command -v codex >/dev/null 2>&1; then
  mkdir -p "$HOME/.codex"
  if ! grep -q 'mcp_servers.corral' "$HOME/.codex/config.toml" 2>/dev/null; then
    cat >> "$HOME/.codex/config.toml" <<EOF

[mcp_servers.corral]
url = "$brain_mcp"
EOF
  fi
fi

exec corral-harness
