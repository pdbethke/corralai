// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var wantIDs = []string{"claude_code", "codex", "gemini_cli", "cursor", "opencode", "antigravity", "copilot_cli", "vscode"}

func fixture(t *testing.T, id, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "agents", id, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAgentsAreTheEightInOrder(t *testing.T) {
	var got []string
	for _, a := range Agents() {
		got = append(got, a.ID)
	}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("agents = %v, want %v", got, wantIDs)
	}
	if _, ok := ByID("codex"); !ok {
		t.Error("ByID(codex) not found")
	}
	if _, ok := ByID("nope"); ok {
		t.Error("ByID(nope) found")
	}
}

func TestEveryAgentRowIsComplete(t *testing.T) {
	for _, a := range Agents() {
		if a.Name == "" || a.CLI == "" || a.ParseInspect == nil || a.Restart == "" || a.Register == nil {
			t.Errorf("%s: incomplete row %+v", a.ID, a)
		}
		if a.Proven {
			t.Errorf("%s: Proven must be false until Task 8", a.ID)
		}
		if a.Inspect == nil && a.File == nil {
			t.Errorf("%s: no inspect command and no config file", a.ID)
		}
		if a.Remove == nil && a.File == nil {
			t.Errorf("%s: no remove command and no config file", a.ID)
		}
		if (a.SkillMode == SkillNone) != (a.SkillDir("/h") == "") {
			t.Errorf("%s: SkillMode and SkillDir disagree", a.ID)
		}
		if (a.ID == "cursor") != (a.FileConfig == ".cursor/mcp.json") {
			t.Errorf("%s: FileConfig = %q", a.ID, a.FileConfig)
		}
		argvs := a.Register("/abs/corral")
		if len(argvs) == 0 {
			t.Errorf("%s: Register returned nothing", a.ID)
			continue
		}
		last := argvs[len(argvs)-1]
		if a.ID == "cursor" || a.ID == "vscode" {
			continue // cursor: post-write enable; vscode: JSON argument
		}
		if n := len(last); n < 2 || last[n-2] != "/abs/corral" || last[n-1] != "mcp" {
			t.Errorf("%s: register argv %v does not end <bin> mcp", a.ID, last)
		}
	}
}

func TestRegisterArgvs(t *testing.T) {
	want := map[string][]string{
		"claude_code": {"claude", "mcp", "add", "-s", "user", "corral", "--", "/b", "mcp"},
		"codex":       {"codex", "mcp", "add", "corral", "--", "/b", "mcp"},
		"gemini_cli":  {"gemini", "mcp", "add", "-s", "user", "corral", "/b", "mcp"},
		"cursor":      {"cursor-agent", "mcp", "enable", "corral"},
		"opencode":    {"opencode", "mcp", "add", "corral", "--", "/b", "mcp"},
		"antigravity": {"agy", "mcp", "add", "corral", "/b", "mcp"},
		"copilot_cli": {"copilot", "mcp", "add", "corral", "--", "/b", "mcp"},
		"vscode":      {"code", "--add-mcp", `{"name":"corral","command":"/b","args":["mcp"]}`},
	}
	for id, w := range want {
		a, _ := ByID(id)
		got := a.Register("/b")
		if len(got) != 1 || !reflect.DeepEqual(got[0], w) {
			t.Errorf("%s: register = %v, want %v", id, got, w)
		}
	}
}

func TestParseInspectFixtures(t *testing.T) {
	for _, a := range Agents() {
		if _, found := a.ParseInspect(fixture(t, a.ID, "inspect-absent.txt")); found {
			t.Errorf("%s: absent fixture parsed as found", a.ID)
		}
		cmd, found := a.ParseInspect(fixture(t, a.ID, "inspect-present.txt"))
		if !found {
			t.Errorf("%s: present fixture parsed as not found", a.ID)
			continue
		}
		want := []string{"__CORRAL__", "mcp"}
		if a.ID == "cursor" {
			want = nil // `cursor-agent mcp list` names no command
		}
		if !reflect.DeepEqual(cmd, want) {
			t.Errorf("%s: command = %v, want %v", a.ID, cmd, want)
		}
	}
}

func TestParseInspectToleratesVariants(t *testing.T) {
	cases := map[string][]string{
		"gemini_cli/inspect-present-trusted.txt": {"gemini_cli"},
		"opencode/config-present.txt":            {"opencode"},
		"codex/list-present-json.txt":            nil,
	}
	for f, ids := range cases {
		for _, id := range ids {
			a, _ := ByID(id)
			parts := strings.SplitN(f, "/", 2)
			cmd, found := a.ParseInspect(fixture(t, parts[0], parts[1]))
			if !found || !reflect.DeepEqual(cmd, []string{"__CORRAL__", "mcp"}) {
				t.Errorf("%s: got %v %v", f, cmd, found)
			}
		}
	}
	for _, name := range []string{"inspect-unapproved.txt", "inspect-disabled.txt"} {
		a, _ := ByID("cursor")
		if _, found := a.ParseInspect(fixture(t, "cursor", name)); !found {
			t.Errorf("cursor %s: configured-but-not-ready must still be registered", name)
		}
	}
	for _, id := range wantIDs {
		a, _ := ByID(id)
		if _, found := a.ParseInspect(fixture(t, id, "inspect-removed.txt")); found {
			t.Errorf("%s: removed fixture parsed as found", id)
		}
	}
}

func TestConfigFilePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	cur, _ := ByID("cursor")
	if got := cur.File.Path(home); got != filepath.Join(home, ".cursor", "mcp.json") || cur.File.ServersKey != "mcpServers" {
		t.Errorf("cursor file = %q %q", got, cur.File.ServersKey)
	}
	vs, _ := ByID("vscode")
	if vs.File.ServersKey != "servers" || !strings.HasSuffix(vs.File.Path(home), filepath.Join("Code", "User", "mcp.json")) {
		t.Errorf("vscode file = %q %q", vs.File.Path(home), vs.File.ServersKey)
	}
	oc, _ := ByID("opencode")
	dir := filepath.Join(home, ".config", "opencode")
	if oc.File.ServersKey != "mcp" || oc.File.Path(home) != filepath.Join(dir, "opencode.jsonc") {
		t.Errorf("opencode default = %q", oc.File.Path(home))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(n string) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("opencode.jsonc")
	if got := oc.File.Path(home); got != filepath.Join(dir, "opencode.jsonc") {
		t.Errorf("jsonc only: %q", got)
	}
	write("opencode.json")
	if got := oc.File.Path(home); got != filepath.Join(dir, "opencode.json") {
		t.Errorf("both exist must prefer .json: %q", got)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got := oc.File.Path(home); got != filepath.Join(xdg, "opencode", "opencode.jsonc") {
		t.Errorf("XDG_CONFIG_HOME ignored: %q", got)
	}
}
