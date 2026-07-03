// SPDX-License-Identifier: Elastic-2.0

// cmd/corral-agent/launcher_test.go
package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/admission"
)

func TestParseSpawnSpec(t *testing.T) {
	got := parseSpawnSpec("tester:2, builder:1 , junk, perf:0")
	// tester:2, builder:1 ; "junk" (no count) and "perf:0" are dropped.
	if len(got) != 2 || got[0].Role != "tester" || got[0].N != 2 || got[1].Role != "builder" || got[1].N != 1 {
		t.Fatalf("parsed wrong: %+v", got)
	}
}

// TestSpawnChildNameAndParentArgs verifies that each spawn_subagent call carries a
// DISTINCT "name" (not clobbered by the parent identity) and the correct "parent".
func TestSpawnChildNameAndParentArgs(t *testing.T) {
	type callRecord struct {
		name   string
		parent string
	}
	var mu sync.Mutex
	var calls []callRecord

	brain := func(tool string, args map[string]any) string {
		if tool != "spawn_subagent" {
			return "{}"
		}
		mu.Lock()
		calls = append(calls, callRecord{
			name:   fmt.Sprintf("%v", args["name"]),
			parent: fmt.Sprintf("%v", args["parent"]),
		})
		mu.Unlock()
		// Return a valid token so the launcher proceeds to host admission.
		childName := fmt.Sprintf("%v", args["parent"]) + "/" + fmt.Sprintf("%v", args["name"])
		return fmt.Sprintf(`{"name":%q,"parent":%q,"token":"tok-abc"}`, childName, args["parent"])
	}

	// Controller admits all 3.
	ctrl := admission.NewLocal(10, 100, func() float64 { return 0 })
	launch := func(env []string, _ admission.Lease) error { return nil }

	spawnConfiguredChildrenN(ctrl, brain, "parentAgent", "http://brain", launch, []childSpec{{Role: "tester", N: 3}})

	if len(calls) != 3 {
		t.Fatalf("expected 3 spawn_subagent calls, got %d", len(calls))
	}

	// All "name" values must be distinct.
	seen := map[string]bool{}
	for _, c := range calls {
		if seen[c.name] {
			t.Errorf("duplicate child name %q in spawn_subagent calls", c.name)
		}
		seen[c.name] = true
	}

	// Every call must carry the correct parent.
	for i, c := range calls {
		if c.parent != "parentAgent" {
			t.Errorf("call[%d]: expected parent=%q, got %q", i, "parentAgent", c.parent)
		}
	}
}

// TestSpawnDuplicateRoleSpecsUniqueNames verifies that two specs with the same Role
// (e.g. "tester:2,tester:1") produce 3 distinct names (tester-1, tester-2, tester-3)
// rather than colliding at tester-1 on the second spec.
func TestSpawnDuplicateRoleSpecsUniqueNames(t *testing.T) {
	var mu sync.Mutex
	var names []string

	brain := func(tool string, args map[string]any) string {
		if tool != "spawn_subagent" {
			return "{}"
		}
		n := fmt.Sprintf("%v", args["name"])
		mu.Lock()
		names = append(names, n)
		mu.Unlock()
		// Return a valid token so the launcher proceeds to host admission.
		return fmt.Sprintf(`{"name":"parent/%s","parent":"parent","token":"tok-abc"}`, n)
	}

	ctrl := admission.NewLocal(10, 100, func() float64 { return 0 })
	launch := func(env []string, _ admission.Lease) error { return nil }

	specs := []childSpec{{Role: "tester", N: 2}, {Role: "tester", N: 1}}
	spawnConfiguredChildrenN(ctrl, brain, "parent", "http://brain", launch, specs)

	if len(names) != 3 {
		t.Fatalf("expected 3 spawn_subagent calls, got %d: %v", len(names), names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate child name %q", n)
		}
		seen[n] = true
	}
	for _, want := range []string{"tester-1", "tester-2", "tester-3"} {
		if !seen[want] {
			t.Errorf("expected name %q not found in %v", want, names)
		}
	}
}

func TestSpawnConfiguredChildrenRespectsAdmission(t *testing.T) {
	// Controller that allows exactly 1, then refuses.
	ctrl := admission.NewLocal(1, 100, func() float64 { return 0 })
	var launched int
	launch := func(env []string, _ admission.Lease) error {
		// must carry token + name + role
		joined := strings.Join(env, " ")
		if !strings.Contains(joined, "CORRAL_TOKEN=") || !strings.Contains(joined, "AGENT_NAME=") || !strings.Contains(joined, "AGENT_ROLE=") {
			t.Fatalf("child env missing required vars: %v", env)
		}
		launched++
		return nil
	}
	// fake brain: spawn_subagent returns a token; everything else returns "{}"
	brain := func(tool string, args map[string]any) string {
		if tool == "spawn_subagent" {
			return `{"name":"P/c","parent":"P","token":"tok-123"}`
		}
		return "{}"
	}
	// request 3 testers but the controller only admits 1 (it never releases here)
	spawnConfiguredChildrenN(ctrl, brain, "P", "http://brain", launch, []childSpec{{Role: "tester", N: 3}})
	if launched != 1 {
		t.Fatalf("admission cap=1 should launch 1 child, launched %d", launched)
	}
}
