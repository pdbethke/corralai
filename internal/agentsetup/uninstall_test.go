// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uninstall(t *testing.T, ss []Status, ids []string, state State) Plan {
	t.Helper()
	return BuildUninstallPlan(ss, ids, state, "/h")
}

func recorded(ids ...string) State {
	s := State{Agents: map[string]AgentRecord{}}
	for _, id := range ids {
		s.Agents[id] = AgentRecord{Command: []string{tbin, "mcp"}}
	}
	return s
}

func reg(cmd ...string) func(*Status) {
	return func(s *Status) { s.Registered, s.Command = true, cmd }
}

func TestUninstallRemovesWhatSetupAdded(t *testing.T) {
	p := uninstall(t, []Status{st(t, "codex", reg(tbin, "mcp"))}, []string{"codex"}, recorded("codex"))
	if len(p.Actions) != 1 || p.Actions[0].Kind != ActionRemove ||
		strings.Join(p.Actions[0].Argv, " ") != "codex mcp remove corral" {
		t.Fatalf("%+v", p.Actions)
	}
	// A path with spaces, split by a text parser, still matches.
	p = uninstall(t, []Status{st(t, "codex", reg("/a", "b/corral", "mcp"))}, []string{"codex"},
		State{Agents: map[string]AgentRecord{"codex": {Command: []string{"/a b/corral", "mcp"}}}})
	if p.Actions[0].Kind != ActionRemove {
		t.Fatalf("%+v", p.Actions)
	}
}

func TestUninstallOnlyWhatSetupAdded(t *testing.T) {
	p := uninstall(t, []Status{st(t, "codex", reg(tbin, "mcp"))}, []string{"codex"}, recorded())
	if len(p.Actions) != 1 || p.Actions[0].Kind != ActionNoop || !strings.Contains(p.Actions[0].Note, "not added by") {
		t.Fatalf("%+v", p.Actions)
	}
}

func TestUninstallSkipsChangedCommand(t *testing.T) {
	p := uninstall(t, []Status{st(t, "codex", reg("/other/corral", "mcp"))}, []string{"codex"}, recorded("codex"))
	if len(p.Actions) != 1 || p.Actions[0].Kind != ActionNoop || !strings.Contains(p.Actions[0].Note, "changed") {
		t.Fatalf("%+v", p.Actions)
	}
}

func TestUninstallUnknownStatusIsLeftAlone(t *testing.T) {
	p := uninstall(t, []Status{st(t, "codex", func(s *Status) { s.Unknown = "timed out" })}, []string{"codex"}, recorded("codex"))
	if p.Actions[0].Kind != ActionNoop || !strings.Contains(p.Actions[0].Note, "could not read; nothing removed") {
		t.Fatalf("%+v", p.Actions)
	}
}

func TestUninstallFileAgentsUnwrite(t *testing.T) {
	for _, id := range []string{"cursor", "opencode", "vscode"} {
		p := uninstall(t, []Status{st(t, id, reg(tbin, "mcp"))}, []string{id}, recorded(id))
		a, _ := ByID(id)
		if len(p.Actions) != 1 || p.Actions[0].Kind != ActionUnwriteFile || p.Actions[0].Path != a.File.Path("/h") {
			t.Errorf("%s: %+v", id, p.Actions)
		}
	}
}

func sharedAgent(id string) Status {
	a := Agent{ID: id, Name: id, CLI: id, SkillMode: SkillShared,
		Remove:   [][]string{{id, "rm"}},
		SkillDir: func(h string) string { return filepath.Join(h, ".agents", "skills", "corral") }}
	return Status{Agent: a, Installed: true, Registered: true, Command: []string{tbin, "mcp"}, Skill: "managed"}
}

func sharedState(ids ...string) State {
	s := State{Agents: map[string]AgentRecord{}}
	for _, id := range ids {
		s.Agents[id] = AgentRecord{Command: []string{tbin, "mcp"}, SkillDir: "/h/.agents/skills/corral"}
	}
	return s
}

func hasKind(p Plan, k ActionKind) bool {
	for _, a := range p.Actions {
		if a.Kind == k {
			return true
		}
	}
	return false
}

func TestUninstallSharedSkillKeptForOtherUser(t *testing.T) {
	ss := []Status{sharedAgent("a"), sharedAgent("b")}
	state := sharedState("a", "b")
	p := uninstall(t, ss, []string{"a"}, state)
	if hasKind(p, ActionRemoveSkill) {
		t.Fatalf("the skill is still b's: %+v", p.Actions)
	}
	if !hasKind(p, ActionRemove) {
		t.Fatalf("%+v", p.Actions)
	}
	state = sharedState("b")
	p = uninstall(t, ss, []string{"b"}, state)
	if !hasKind(p, ActionRemoveSkill) {
		t.Fatalf("%+v", p.Actions)
	}
	// Both at once: one removal of the shared dir.
	p = uninstall(t, ss, []string{"a", "b"}, sharedState("a", "b"))
	n := 0
	for _, a := range p.Actions {
		if a.Kind == ActionRemoveSkill {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one shared removal, got %d: %+v", n, p.Actions)
	}
}

func TestUninstallLeavesUnmanagedSkill(t *testing.T) {
	ss := []Status{sharedAgent("a")}
	ss[0].Skill = "unmanaged"
	p := uninstall(t, ss, []string{"a"}, sharedState("a"))
	if hasKind(p, ActionRemoveSkill) {
		t.Fatalf("%+v", p.Actions)
	}
	found := false
	for _, a := range p.Actions {
		if a.Kind == ActionSkipSkill && strings.Contains(a.Note, "not setup's") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an explanatory skip: %+v", p.Actions)
	}
	_ = os.Stdout
}

// A shared directory is decided after every agent's outcome is known, so the
// answer cannot depend on the order agents are listed in.
func TestUninstallSharedSkillKeptWhenOtherUserIsNoop(t *testing.T) {
	for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
		var ss []Status
		for _, id := range order {
			s := sharedAgent(id)
			if id == "a" {
				s.Unknown = "timed out" // a's uninstall is a noop, so a keeps its record
			}
			ss = append(ss, s)
		}
		p := uninstall(t, ss, []string{"a", "b"}, sharedState("a", "b"))
		if hasKind(p, ActionRemoveSkill) {
			t.Errorf("order %v: skill removed while a still lists it: %+v", order, p.Actions)
		}
		if !hasKind(p, ActionRemove) {
			t.Errorf("order %v: b's registration should still be removed: %+v", order, p.Actions)
		}
	}
}

// I1: an agent whose registration is already gone (removed by hand) is
// leaving, so setup's skill copy goes with it and its record is dropped. It is
// never "still used by" itself.
func TestUninstallRegistrationGoneRemovesSkillAndRecord(t *testing.T) {
	codex := mustAgent(t, "codex")
	dir := codex.SkillDir("/h")
	state := State{Agents: map[string]AgentRecord{"codex": {Command: []string{tbin, "mcp"}, SkillDir: dir}}}
	s := st(t, "codex", func(s *Status) { s.Skill = "managed" })
	p := uninstall(t, []Status{s}, []string{"codex"}, state)
	wantKinds(t, p, ActionNoop, ActionForget, ActionRemoveSkill)
	if !p.Actions[0].Settled {
		t.Errorf("an already-gone registration is a satisfied end state: %+v", p.Actions[0])
	}
	for _, a := range p.Actions {
		if strings.Contains(a.Note, "still used by") {
			t.Fatalf("the agent being uninstalled was counted as staying: %+v", a)
		}
	}
	// Without a skill copy the stale record is still dropped.
	state = State{Agents: map[string]AgentRecord{"codex": {Command: []string{tbin, "mcp"}}}}
	wantKinds(t, uninstall(t, []Status{st(t, "codex", nil)}, []string{"codex"}, state), ActionNoop, ActionForget)
}

// I1: an agent whose CLI is gone is leaving too: its skill copy is removed.
// Its registration record is kept, because the agent's own config (and with it
// setup's registration) can outlive the CLI and come back with a reinstall.
func TestUninstallNotInstalledRemovesSkill(t *testing.T) {
	codex := mustAgent(t, "codex")
	dir := codex.SkillDir("/h")
	state := State{Agents: map[string]AgentRecord{"codex": {Command: []string{tbin, "mcp"}, SkillDir: dir}}}
	s := st(t, "codex", func(s *Status) { s.Installed, s.Skill = false, "managed" })
	p := uninstall(t, []Status{s}, []string{"codex"}, state)
	wantKinds(t, p, ActionNoop, ActionRemoveSkill)
	if strings.Contains(p.Actions[0].Note, "nothing removed") {
		t.Errorf("the note must not say nothing is removed when the skill is: %q", p.Actions[0].Note)
	}
	for _, a := range p.Actions {
		if strings.Contains(a.Note, "still used by") {
			t.Fatalf("the agent being uninstalled was counted as staying: %+v", a)
		}
	}
}
