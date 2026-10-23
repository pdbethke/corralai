// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// SkillMode says where an agent reads a corral skill from.
type SkillMode int

const (
	SkillOwn    SkillMode = iota // the agent's own skills directory
	SkillShared                  // no directory of its own: ~/.agents/skills
	SkillNone                    // no skill channel; shown as n/a
)

// ConfigFile describes an agent's user-level config file. It is used ONLY for
// the steps the agent has no command for (FACTS.md): cursor register and
// remove, opencode remove, vscode inspect and remove.
type ConfigFile struct {
	Path       func(home string) string // absolute path of the config file
	ServersKey string                   // JSON object holding servers
}

// Agent is one coding agent corral can wire itself into. Every fact in a row
// was established by execution; see testdata/agents/FACTS.md.
type Agent struct {
	ID, Name, CLI string // CLI = binary name for LookPath

	// Register returns the argvs that register `<bin> mcp` as server "corral",
	// run in order. For cursor it returns only the POST-write step
	// (`cursor-agent mcp enable corral`): the JSON write itself is Task 4's
	// MergeCursorConfig, and the enable must follow it.
	Register func(bin string) [][]string

	// Inspect is the agent's own read verb; nil when the agent has none
	// (vscode), in which case File is read instead.
	Inspect []string
	// ParseInspect extracts the launch command argv for server "corral" from
	// Inspect's output (or, for vscode, from its config file). It decides on
	// the presence of the server, never on status text such as Connected or
	// Disabled: those depend on folder trust and on the server starting.
	//
	// err is non-nil when the output is neither a listing naming corral nor
	// the agent's own recognised "not registered" message: setup could not see,
	// so Detect reports Unknown. A recognised absent message is (nil, false,
	// nil) whatever the exit code, so a get verb's exit 1 is absent only when
	// it says so in its own words.
	ParseInspect func(out []byte) (command []string, found bool, err error)
	// FileFallback: when Inspect cannot run (not a timeout), read File instead
	// (cursor only). Every other agent's failed inspect stays Unknown.
	FileFallback bool

	Remove [][]string // nil when the agent has no remove command (cursor, opencode, vscode)

	SkillMode SkillMode
	// SkillDir returns the corral skill directory (<root>/corral) under home;
	// "" when SkillNone.
	SkillDir func(home string) string

	FileConfig string      // non-empty only for cursor: path relative to home, ".cursor/mcp.json"
	File       *ConfigFile // see ConfigFile
	Proven     bool        // a headless load of corral through this agent has been executed; false for all eight until Task 8
	Restart    string
}

func xdgConfigHome(home string) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	return filepath.Join(home, ".config")
}

func skillDirUnder(elem ...string) func(string) string {
	return func(home string) string {
		return filepath.Join(append([]string{home}, append(elem, "corral")...)...)
	}
}

func mcpAdd(cli string, flags []string, dashdash bool) func(string) [][]string {
	return func(bin string) [][]string {
		argv := append([]string{cli, "mcp", "add"}, flags...)
		argv = append(argv, "corral")
		if dashdash {
			argv = append(argv, "--")
		}
		return [][]string{append(argv, bin, "mcp")}
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Agents returns exactly the eight agents, in the order claude_code, codex,
// gemini_cli, cursor, opencode, antigravity, copilot_cli, vscode.
//
// Skill directories (SkillMode choices): where an agent has both its own
// directory and the shared ~/.agents/skills, the row uses its OWN, so removing
// one agent's skill never affects another's (controller ruling); FACTS.md
// records which were executed.
func Agents() []Agent {
	return []Agent{
		{
			ID: "claude_code", Name: "Claude Code", CLI: "claude",
			Register:     mcpAdd("claude", []string{"-s", "user"}, true),
			Inspect:      []string{"claude", "mcp", "get", "corral"},
			ParseInspect: parseClaude,
			Remove:       [][]string{{"claude", "mcp", "remove", "-s", "user", "corral"}},
			// FACTS: Claude Code's documented location, not executed.
			SkillMode: SkillOwn, SkillDir: skillDirUnder(".claude", "skills"),
			Restart: "Start a new Claude Code session; running sessions do not pick up a new server.",
		},
		{
			ID: "codex", Name: "Codex CLI", CLI: "codex",
			Register:     mcpAdd("codex", nil, true),
			Inspect:      []string{"codex", "mcp", "get", "corral"},
			ParseInspect: parseCodex,
			Remove:       [][]string{{"codex", "mcp", "remove", "corral"}},
			// FACTS: ~/.codex/skills executed (skills/list, scope user), as is the shared dir.
			SkillMode: SkillOwn, SkillDir: skillDirUnder(".codex", "skills"),
			Restart: "Start a new Codex session.",
		},
		{
			ID: "gemini_cli", Name: "Gemini CLI", CLI: "gemini",
			Register:     mcpAdd("gemini", []string{"-s", "user"}, false),
			Inspect:      []string{"gemini", "mcp", "list"},
			ParseInspect: parseGemini,
			Remove:       [][]string{{"gemini", "mcp", "remove", "-s", "user", "corral"}},
			// FACTS: ~/.gemini/skills executed (gemini skills list), as is the shared dir.
			SkillMode: SkillOwn, SkillDir: skillDirUnder(".gemini", "skills"),
			Restart: "Start a new Gemini CLI session.",
		},
		{
			ID: "cursor", Name: "Cursor", CLI: "cursor-agent",
			// The JSON write is MergeCursorConfig (Task 4); this is the step after it.
			Register:     func(string) [][]string { return [][]string{{"cursor-agent", "mcp", "enable", "corral"}} },
			Inspect:      []string{"cursor-agent", "mcp", "list"},
			ParseInspect: parseCursor,
			FileConfig:   ".cursor/mcp.json",
			FileFallback: true,
			File: &ConfigFile{
				Path:       func(home string) string { return filepath.Join(home, ".cursor", "mcp.json") },
				ServersKey: "mcpServers",
			},
			// FACTS: ~/.cursor/skills from strings in the cursor-agent bundle, not executed.
			SkillMode: SkillOwn, SkillDir: skillDirUnder(".cursor", "skills"),
			Restart: "Start a new Cursor session or agent run.",
		},
		{
			ID: "opencode", Name: "opencode", CLI: "opencode",
			Register:     mcpAdd("opencode", nil, true),
			Inspect:      []string{"opencode", "debug", "config"},
			ParseInspect: parseOpencode,
			File: &ConfigFile{
				Path: func(home string) string {
					dir := filepath.Join(xdgConfigHome(home), "opencode")
					if _, err := os.Stat(filepath.Join(dir, "opencode.json")); err == nil {
						return filepath.Join(dir, "opencode.json")
					}
					return filepath.Join(dir, "opencode.jsonc")
				},
				ServersKey: "mcp",
			},
			// FACTS: $XDG_CONFIG_HOME/opencode/skills executed (debug skill), as is the shared dir.
			SkillMode: SkillOwn,
			SkillDir:  func(home string) string { return filepath.Join(xdgConfigHome(home), "opencode", "skills", "corral") },
			Restart:   "Start a new opencode session.",
		},
		{
			ID: "antigravity", Name: "Antigravity", CLI: "agy",
			Register:     mcpAdd("agy", nil, false),
			Inspect:      []string{"agy", "mcp", "list"},
			ParseInspect: parseAntigravity,
			Remove:       [][]string{{"agy", "mcp", "remove", "corral"}},
			// FACTS: ~/.gemini/config/skills confirmed by agy's embedded changelog; the shared dir was not established.
			SkillMode: SkillOwn, SkillDir: skillDirUnder(".gemini", "config", "skills"),
			Restart: "Start a new Antigravity session.",
		},
		{
			ID: "copilot_cli", Name: "GitHub Copilot CLI", CLI: "copilot",
			Register:     mcpAdd("copilot", nil, true),
			Inspect:      []string{"copilot", "mcp", "get", "corral"},
			ParseInspect: parseCopilot,
			Remove:       [][]string{{"copilot", "mcp", "remove", "corral"}},
			// FACTS: ~/.copilot/skills and the shared dir are both "Personal" per copilot's own help; not executed.
			SkillMode: SkillOwn, SkillDir: skillDirUnder(".copilot", "skills"),
			Restart: "Start a new Copilot session.",
		},
		{
			ID: "vscode", Name: "VS Code", CLI: "code",
			Register: func(bin string) [][]string {
				spec, _ := json.Marshal(struct {
					Name    string   `json:"name"`
					Command string   `json:"command"`
					Args    []string `json:"args"`
				}{"corral", bin, []string{"mcp"}})
				return [][]string{{"code", "--add-mcp", string(spec)}}
			},
			ParseInspect: func(out []byte) ([]string, bool, error) {
				return parseServersJSON(out, "servers")
			},
			File: &ConfigFile{
				Path: func(home string) string {
					if runtime.GOOS == "darwin" {
						return filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json")
					}
					return filepath.Join(home, ".config", "Code", "User", "mcp.json")
				},
				ServersKey: "servers",
			},
			// FACTS: no skill channel found by execution; VS Code is an MCP host here.
			SkillMode: SkillNone, SkillDir: func(string) string { return "" },
			Restart: "Reload the VS Code window, or start the server from the MCP list.",
		},
	}
}

// ByID returns the agent with the given ID.
func ByID(id string) (Agent, bool) {
	for _, a := range Agents() {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// lineAfter returns the text after prefix on the first trimmed line that has it.
func lineAfter(out []byte, prefix string) (string, bool) {
	for _, l := range strings.Split(string(out), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(t, prefix)), true
		}
	}
	return "", false
}

// hasLine reports whether some line, trimmed, equals s.
func hasLine(out []byte, s string) bool {
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == s {
			return true
		}
	}
	return false
}

// commandArgs joins a "Command:" value and an "Args:" value into one argv.
func commandArgs(out []byte, cmdKey, argsKey string) []string {
	c, ok := lineAfter(out, cmdKey)
	if !ok || c == "" {
		return nil
	}
	a, _ := lineAfter(out, argsKey)
	return append([]string{c}, strings.Fields(a)...)
}

// unrecognised is the "could not see" answer: output that neither names corral
// nor says, in the agent's own words, that it is not registered.
func unrecognised(out []byte) error {
	t := strings.TrimSpace(string(out))
	if len(t) > 200 {
		t = t[:200] + "..."
	}
	return fmt.Errorf("unrecognised output: %q", t)
}

// absentOr returns (nil, false, nil) when out matches an absent marker, else
// an unrecognised-output error.
func absentOr(out []byte, markers ...*regexp.Regexp) ([]string, bool, error) {
	for _, m := range markers {
		if m.Match(out) {
			return nil, false, nil
		}
	}
	return nil, false, unrecognised(out)
}

func parseClaude(out []byte) ([]string, bool, error) {
	if hasLine(out, "corral:") {
		return commandArgs(out, "Command:", "Args:"), true, nil
	}
	return absentOr(out, regexp.MustCompile(`No MCP server named "corral"`))
}

func parseCodex(out []byte) ([]string, bool, error) {
	// Not `corral:`-prefixed: codex prints the bare name, then indented fields.
	// A leading WARNING line is tolerated because only the exact name line counts.
	if hasLine(out, "corral") {
		return commandArgs(out, "command:", "args:"), true, nil
	}
	return absentOr(out, regexp.MustCompile(`No MCP server named 'corral' found`))
}

var geminiRow = regexp.MustCompile(`(?m)^\S+ corral: (.+?) \((?:stdio|sse|http)\)`)

func parseGemini(out []byte) ([]string, bool, error) {
	if m := geminiRow.FindSubmatch(out); m != nil {
		return strings.Fields(string(m[1])), true, nil
	}
	return absentOr(out, regexp.MustCompile(`No MCP servers configured|Configured MCP servers:`))
}

var cursorRow = regexp.MustCompile(`(?m)^corral: `)

// parseCursor: `cursor-agent mcp list` names no command, so none is returned;
// Detect fills it from the config file.
func parseCursor(out []byte) ([]string, bool, error) {
	if cursorRow.Match(out) {
		return nil, true, nil
	}
	return absentOr(out, regexp.MustCompile(`No MCP servers configured|(?m)^[^\s:]+: (?:ready|not loaded|disabled)\b`))
}

var opencodeRow = regexp.MustCompile(`(?m)^\S+\s+\S+ corral\b`)

// opencodeConfig finds the resolved-config object in `debug config` output:
// the first '{' from which a JSON object decodes and that carries the config's
// own keys, so a preamble containing a brace cannot defeat the search.
func opencodeConfig(out []byte) (map[string]json.RawMessage, bool) {
	for i := 0; i < len(out); i++ {
		if out[i] != '{' {
			continue
		}
		var doc map[string]json.RawMessage
		if json.NewDecoder(bytes.NewReader(out[i:])).Decode(&doc) != nil {
			continue
		}
		_, hasMCP := doc["mcp"]
		_, hasSchema := doc["$schema"]
		if hasMCP || hasSchema {
			return doc, true
		}
	}
	return nil, false
}

func parseOpencode(out []byte) ([]string, bool, error) {
	out = []byte(ansi.ReplaceAllString(string(out), ""))
	// `debug config` prints the resolved config as JSON: the machine-readable form.
	if doc, ok := opencodeConfig(out); ok {
		var servers map[string]struct {
			Command []string `json:"command"`
		}
		if raw, ok := doc["mcp"]; ok {
			if err := json.Unmarshal(raw, &servers); err != nil {
				return nil, false, fmt.Errorf("opencode mcp config does not parse: %w", err)
			}
		}
		s, found := servers["corral"]
		return s.Command, found, nil
	}
	// `mcp list`: "● ✓ corral connected", then "│ <command line>".
	lines := strings.Split(string(out), "\n")
	for i, l := range lines {
		if opencodeRow.MatchString(l) {
			var cmd []string
			if i+1 < len(lines) {
				cmd = strings.Fields(strings.TrimLeft(strings.TrimSpace(lines[i+1]), "│ "))
			}
			return cmd, true, nil
		}
	}
	return absentOr(out, regexp.MustCompile(`MCP Servers`))
}

var agyRow = regexp.MustCompile(`(?m)^corral\s+\S+\s+\S+\s+(.+)$`)

func parseAntigravity(out []byte) ([]string, bool, error) {
	if m := agyRow.FindSubmatch(out); m != nil {
		return strings.Fields(string(m[1])), true, nil
	}
	return absentOr(out, regexp.MustCompile(`No MCP servers configured|(?m)^NAME\s+TYPE`))
}

func parseCopilot(out []byte) ([]string, bool, error) {
	lines := strings.Split(string(out), "\n")
	for i, l := range lines {
		if strings.TrimRight(l, " \r") == "corral" {
			c, _ := lineAfter([]byte(strings.Join(lines[i+1:], "\n")), "Command:")
			return strings.Fields(c), true, nil
		}
	}
	return absentOr(out, regexp.MustCompile(`Server "corral" not found`))
}

// parseServersJSON reads a config file whose `key` object maps server names to
// {command, args}. err is non-nil when data is not JSON.
func parseServersJSON(data []byte, key string) (command []string, found bool, err error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	var servers map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if raw, ok := doc[key]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, false, err
		}
	}
	s, ok := servers["corral"]
	if !ok {
		return nil, false, nil
	}
	return append([]string{s.Command}, s.Args...), true, nil
}
