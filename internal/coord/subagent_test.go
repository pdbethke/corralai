// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"path/filepath"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSpawnDespawn(t *testing.T) {
	s := openStore(t)
	if err := s.Register("boss@x.com", "Claude Code", "opus", "lead", "", ""); err != nil {
		t.Fatal(err)
	}
	full, err := s.Spawn("boss@x.com", "tester", "tester", "Claude Code", "haiku", "run the tests")
	if err != nil || full != "boss@x.com/tester" {
		t.Fatalf("spawn: full=%q err=%v", full, err)
	}
	// Subagent shows up with parent + role.
	subs, _ := s.Subagents("boss@x.com")
	if len(subs) != 1 || subs[0].Name != full || subs[0].Parent != "boss@x.com" || subs[0].Role != "tester" {
		t.Fatalf("subagents wrong: %#v", subs)
	}
	// It's an active agent too (so it renders in the swarm).
	active, _ := s.ListActive(PresenceWindow)
	found := false
	for _, a := range active {
		if a.Name == full {
			found = true
			if a.Parent != "boss@x.com" || a.Role != "tester" {
				t.Fatalf("active subagent missing parent/role: %#v", a)
			}
		}
	}
	if !found {
		t.Fatal("subagent should be active")
	}
	// It can hold claims; despawn releases them and removes the node.
	if _, err := s.ClaimPaths(full, []string{"tests/"}, 3600, true, "testing"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Despawn(full)
	if err != nil || !ok {
		t.Fatalf("despawn: ok=%v err=%v", ok, err)
	}
	if subs, _ := s.Subagents("boss@x.com"); len(subs) != 0 {
		t.Fatalf("subagent should be gone, got %#v", subs)
	}
	st, _ := s.CoordinationStatus(PresenceWindow)
	for _, c := range st.LiveClaims {
		if c.Agent == full {
			t.Fatal("despawn should have released the subagent's claims")
		}
	}
}

func TestHeartbeatPreservesParentRole(t *testing.T) {
	s := openStore(t)
	s.Register("p@x", "cc", "opus", "lead", "", "")
	full, _ := s.Spawn("p@x", "worker", "deployer", "cc", "opus", "deploy")
	// A re-register/heartbeat with empty parent/role must NOT wipe them.
	if err := s.upsertAgent(full, "cc", "opus", "still deploying", "", "", ""); err != nil {
		t.Fatal(err)
	}
	subs, _ := s.Subagents("p@x")
	if len(subs) != 1 || subs[0].Role != "deployer" || subs[0].Parent != "p@x" {
		t.Fatalf("parent/role clobbered by heartbeat: %#v", subs)
	}
}
