// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "setup.json")
	want := State{Agents: map[string]AgentRecord{
		"claude-code": {
			RegisterArgv: [][]string{{"claude", "mcp", "add", "corral"}},
			Command:      []string{"/usr/bin/corral", "mcp"},
			SkillDir:     "/h/.claude/skills/corral",
		},
	}}
	if err := SaveState(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the state:\n got %+v\nwant %+v", got, want)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Errorf("temp file left behind: %v", ents)
	}
}

func TestLoadStateMissingIsEmpty(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Agents) != 0 {
		t.Errorf("want empty state, got %+v", s)
	}
}

func TestLoadStateCorruptIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(p); err == nil {
		t.Fatal("a corrupt state file must be refused, not read as empty")
	}
}

func TestSkillUsersShared(t *testing.T) {
	shared := "/h/.agents/skills/corral"
	s := State{Agents: map[string]AgentRecord{
		"opencode": {SkillDir: shared},
		"codex":    {SkillDir: shared},
		"claude":   {SkillDir: "/h/.claude/skills/corral"},
		"mcp-only": {},
	}}
	if got, want := s.SkillUsers(shared), []string{"codex", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SkillUsers = %v, want %v", got, want)
	}
	if got := s.SkillUsers(""); len(got) != 0 {
		t.Errorf("empty dir must match nothing, got %v", got)
	}
}
