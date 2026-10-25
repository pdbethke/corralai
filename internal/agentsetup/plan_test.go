// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tbin = "/usr/local/bin/corral"

func st(t *testing.T, id string, mod func(*Status)) Status {
	t.Helper()
	a, ok := ByID(id)
	if !ok {
		t.Fatal(id)
	}
	s := Status{Agent: a, Installed: true, Skill: "absent"}
	if mod != nil {
		mod(&s)
	}
	return s
}

func plan(t *testing.T, ss []Status, ids []string, skill bool) Plan {
	t.Helper()
	p, err := BuildSetupPlan(ss, ids, SetupOptions{Bin: tbin, Skill: skill, Home: "/h", Version: "v"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func kinds(p Plan) []ActionKind {
	var k []ActionKind
	for _, a := range p.Actions {
		k = append(k, a.Kind)
	}
	return k
}

func wantKinds(t *testing.T, p Plan, want ...ActionKind) {
	t.Helper()
	got := kinds(p)
	if len(got) != len(want) {
		t.Fatalf("kinds %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kinds %v, want %v", got, want)
		}
	}
}

func TestPlanUnwiredAgentRegisters(t *testing.T) {
	p := plan(t, []Status{st(t, "codex", nil)}, []string{"codex"}, false)
	wantKinds(t, p, ActionRegister, ActionSkipSkill)
	if got := strings.Join(p.Actions[0].Argv, " "); got != "codex mcp add corral -- "+tbin+" mcp" {
		t.Errorf("argv %q", got)
	}
}

func TestPlanSameCommandIsNoop(t *testing.T) {
	p := plan(t, []Status{st(t, "codex", func(s *Status) { s.Registered, s.Command = true, []string{tbin, "mcp"} })}, []string{"codex"}, false)
	wantKinds(t, p, ActionNoop, ActionSkipSkill)
	// a path with spaces split by a text parser still counts as the same
	spaced := "/My Apps/corral"
	p2, _ := BuildSetupPlan([]Status{st(t, "codex", func(s *Status) { s.Registered, s.Command = true, []string{"/My", "Apps/corral", "mcp"} })}, []string{"codex"}, SetupOptions{Bin: spaced, Home: "/h"})
	if p2.Actions[0].Kind != ActionNoop {
		t.Errorf("split path: %v", kinds(p2))
	}
}

func TestPlanDifferentCommandReplaces(t *testing.T) {
	old := func(s *Status) { s.Registered, s.Command = true, []string{"/old/corral", "mcp"} }
	p := plan(t, []Status{st(t, "codex", old)}, []string{"codex"}, false)
	wantKinds(t, p, ActionReplace, ActionSkipSkill)
	if !strings.HasPrefix(p.Actions[0].Note, "will replace: /old/corral mcp → "+tbin+" mcp") {
		t.Errorf("note %q", p.Actions[0].Note)
	}
	// claude's add refuses a duplicate: remove first
	p = plan(t, []Status{st(t, "claude_code", old)}, []string{"claude_code"}, false)
	wantKinds(t, p, ActionRemove, ActionReplace, ActionSkipSkill)
	if strings.Join(p.Actions[0].Argv, " ") != "claude mcp remove -s user corral" {
		t.Errorf("remove argv %v", p.Actions[0].Argv)
	}
}

func TestPlanCursorWritesFileThenEnables(t *testing.T) {
	p := plan(t, []Status{st(t, "cursor", nil)}, []string{"cursor"}, false)
	wantKinds(t, p, ActionWriteFile, ActionEnable, ActionSkipSkill)
	if p.Actions[0].Path != filepath.Join("/h", ".cursor", "mcp.json") {
		t.Errorf("path %q", p.Actions[0].Path)
	}
	if strings.Join(p.Actions[1].Argv, " ") != "cursor-agent mcp enable corral" {
		t.Errorf("enable %v", p.Actions[1].Argv)
	}
	p = plan(t, []Status{st(t, "cursor", func(s *Status) { s.Registered, s.Command = true, []string{"/old", "mcp"} })}, []string{"cursor"}, false)
	wantKinds(t, p, ActionWriteFile, ActionEnable, ActionSkipSkill)
	if !strings.Contains(p.Actions[0].Note, "will replace") {
		t.Errorf("note %q", p.Actions[0].Note)
	}
}

func TestPlanSkillFalseSkips(t *testing.T) {
	p := plan(t, []Status{st(t, "codex", nil)}, []string{"codex"}, false)
	if a := p.Actions[1]; a.Kind != ActionSkipSkill || !strings.Contains(a.Note, "not requested") {
		t.Errorf("%+v", a)
	}
	p = plan(t, []Status{st(t, "codex", nil)}, []string{"codex"}, true)
	if a := p.Actions[1]; a.Kind != ActionWriteSkill || a.Path != filepath.Join("/h", ".codex", "skills", "corral") {
		t.Errorf("%+v", a)
	}
}

func TestPlanSkillNoneIsNA(t *testing.T) {
	p := plan(t, []Status{st(t, "vscode", func(s *Status) { s.Skill = "n/a" })}, []string{"vscode"}, true)
	last := p.Actions[len(p.Actions)-1]
	if last.Kind != ActionSkipSkill || !strings.Contains(last.Note, "skill: n/a") {
		t.Errorf("%+v", last)
	}
}

func TestPlanSkillStatesAreRespected(t *testing.T) {
	for state, want := range map[SkillState]ActionKind{"unmanaged": ActionSkipSkill, "refused-symlink": ActionSkipSkill, "managed": ActionWriteSkill} {
		p := plan(t, []Status{st(t, "codex", func(s *Status) { s.Skill = state })}, []string{"codex"}, true)
		if got := p.Actions[len(p.Actions)-1].Kind; got != want {
			t.Errorf("%s: %s", state, got)
		}
	}
}

func TestPlanSharedSkillWrittenOnce(t *testing.T) {
	shared := func(id string) Status {
		a := Agent{ID: id, Name: id, CLI: id, SkillMode: SkillShared,
			Register: func(bin string) [][]string { return [][]string{{id, "add", bin}} },
			SkillDir: func(h string) string { return filepath.Join(h, ".agents", "skills", "corral") }}
		return Status{Agent: a, Installed: true, Skill: "absent"}
	}
	// BuildSetupPlan validates IDs against the table, so reuse two real IDs
	// with their rows replaced by shared-skill rows.
	a, b := shared("codex"), shared("gemini_cli")
	p := plan(t, []Status{a, b}, []string{"codex", "gemini_cli"}, true)
	n := 0
	for _, act := range p.Actions {
		if act.Kind == ActionWriteSkill {
			n++
			if act.Path != filepath.Join("/h", ".agents", "skills", "corral") {
				t.Errorf("path %q", act.Path)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d write-skill actions: %v", n, kinds(p))
	}
}

func TestPlanClaudePluginLeftAlone(t *testing.T) {
	p := plan(t, []Status{st(t, "claude_code", func(s *Status) { s.ViaPlugin = true })}, []string{"claude_code"}, true)
	wantKinds(t, p, ActionNoop)
	if !strings.HasPrefix(p.Actions[0].Note, "wired by the corral plugin") {
		t.Errorf("note %q", p.Actions[0].Note)
	}
}

func TestPlanUnknownIsNotActedOn(t *testing.T) {
	p := plan(t, []Status{st(t, "codex", func(s *Status) { s.Unknown = "codex mcp get corral timed out" })}, []string{"codex"}, true)
	wantKinds(t, p, ActionNoop)
	if !strings.Contains(p.Actions[0].Note, "could not be read") || !strings.Contains(p.Actions[0].Note, "timed out") {
		t.Errorf("note %q", p.Actions[0].Note)
	}
}

func TestPlanUnprovenLabelled(t *testing.T) {
	u := st(t, "codex", nil)
	u.Agent.Proven = false // the label follows the flag, whatever the table says today
	p := plan(t, []Status{u}, []string{"codex"}, true)
	var buf bytes.Buffer
	p.Render(&buf)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%q", buf.String())
	}
	for _, l := range lines {
		if !strings.Contains(l, "(not yet proven)") {
			t.Errorf("line %q", l)
		}
	}
	a := st(t, "codex", nil)
	a.Agent.Proven = true
	p = plan(t, []Status{a}, []string{"codex"}, false)
	buf.Reset()
	p.Render(&buf)
	if strings.Contains(buf.String(), "not yet proven") {
		t.Errorf("proven agent labelled: %q", buf.String())
	}
}

func TestPlanOnlySelectedAndUnknownIDErrors(t *testing.T) {
	ss := []Status{st(t, "codex", nil), st(t, "gemini_cli", nil)}
	p := plan(t, ss, []string{"gemini_cli"}, false)
	for _, a := range p.Actions {
		if a.AgentID != "gemini_cli" {
			t.Errorf("unselected agent planned: %+v", a)
		}
	}
	if _, err := BuildSetupPlan(ss, []string{"nope"}, SetupOptions{Bin: tbin, Home: "/h"}); err == nil {
		t.Error("unknown ID accepted")
	}
}

func TestPlanNotInstalledIsNoop(t *testing.T) {
	p := plan(t, []Status{st(t, "codex", func(s *Status) { s.Installed = false })}, []string{"codex"}, true)
	wantKinds(t, p, ActionNoop)
}

// The printed plan names every file it will write or back up: a write-file or
// unwrite-file step names the exact backup it will make (the first backup is
// kept, so a later one carries the old bytes' hash), or says none is made.
func TestPlanNamesTheBackupFile(t *testing.T) {
	home := t.TempDir()
	cur := mustAgent(t, "cursor")
	path := cur.File.Path(home)
	note := func() string {
		t.Helper()
		p, err := BuildSetupPlan([]Status{st(t, "cursor", nil)}, []string{"cursor"}, SetupOptions{Bin: tbin, Home: home})
		if err != nil {
			t.Fatal(err)
		}
		return p.Actions[0].Note
	}
	if n := note(); !strings.Contains(n, "no backup") {
		t.Errorf("missing file: %q", n)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := note(); !strings.Contains(n, "backup: "+path+".corral.backup") || strings.Contains(n, ".corral.backup.") {
		t.Errorf("first backup: %q", n)
	}
	if err := os.WriteFile(path+".corral.backup", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, _ := BackupPath(path)
	if !strings.HasPrefix(want, path+".corral.backup.") || len(want) != len(path+".corral.backup.")+12 {
		t.Fatalf("BackupPath = %q", want)
	}
	if n := note(); !strings.Contains(n, "backup: "+want) {
		t.Errorf("later backup: %q, want %q", n, want)
	}
	// Apply makes exactly the backup the plan named.
	if err := editConfigFile(path, func([]byte) ([]byte, error) { return []byte("{}"), nil }); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != `{"mcpServers":{}}` {
		t.Errorf("the named backup %s holds %q (%v)", want, b, err)
	}
	// Uninstall's unwrite names its backup too.
	u := BuildUninstallPlan([]Status{st(t, "cursor", reg(tbin, "mcp"))}, []string{"cursor"}, recorded("cursor"), home)
	next, _ := BackupPath(path)
	if u.Actions[0].Kind != ActionUnwriteFile || !strings.Contains(u.Actions[0].Note, "backup: "+next) {
		t.Errorf("unwrite note %q, want backup %s", u.Actions[0].Note, next)
	}
}

func TestPlanRenderLines(t *testing.T) {
	p := Plan{Actions: []Action{
		{AgentID: "codex", Kind: ActionRegister, Argv: []string{"codex", "mcp", "add", "corral"}},
		{AgentID: "cursor", Kind: ActionWriteFile, Path: "/h/.cursor/mcp.json", Note: "add corral"},
		{AgentID: "gemini_cli", Kind: ActionNoop, Note: "already registered"},
		{AgentID: "codex", Kind: ActionSkipSkill},
	}}
	var buf bytes.Buffer
	p.Render(&buf)
	want := "" +
		"  codex        register     codex mcp add corral\n" +
		"  cursor       write-file   /h/.cursor/mcp.json  add corral\n" +
		"  gemini_cli   noop          already registered\n" +
		"  codex        skip-skill  \n"
	if buf.String() != want {
		t.Errorf("render:\n%q\nwant\n%q", buf.String(), want)
	}
}
