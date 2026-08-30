// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// TestEachRoleHasItsOwnMeter pins the fix for a run that could say what it
// spent in TOTAL but not which role spent it: before this task, every seat
// shared one agentbackend.UsageMeter, so a $40 test-writer bill and a $2
// mutant-generator bill summed to one $42 line nobody could attribute.
//
// It drives two chats through the test-writer's chatter and one through the
// mutant-generator's, all against fake backends on the SAME vendor (so
// localChatterFor's WithModel path, not its cross-vendor path, is what's
// under test), and asserts each role's own meter saw only its own calls —
// and that summing every role's meter equals the run's total.
func TestEachRoleHasItsOwnMeter(t *testing.T) {
	srv, _ := captureServer(t, "anthropic")
	t.Setenv("MODEL_BACKEND", "anthropic")
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "claude-sonnet-5",
		advpool.RoleTestWriter:      "claude-haiku-4-5",
		advpool.RoleTestCritic:      "claude-opus-4-1",
	}
	meters := auditRoleMeters(assign)
	chatterFor, err := localChatterFor(assign, meters, nil)
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}

	writer := chatterFor(advpool.RoleTestWriter)
	mutant := chatterFor(advpool.RoleMutantGenerator)

	for i := 0; i < 2; i++ {
		if _, err := writer.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatalf("writer.Chat #%d: %v", i, err)
		}
	}
	if _, err := mutant.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("mutant.Chat: %v", err)
	}

	writerSnap := meters[advpool.RoleTestWriter].Snapshot()
	if writerSnap.Calls != 2 {
		t.Errorf("test-writer meter saw %d calls, want 2", writerSnap.Calls)
	}
	mutantSnap := meters[advpool.RoleMutantGenerator].Snapshot()
	if mutantSnap.Calls != 1 {
		t.Errorf("mutant-generator meter saw %d calls, want 1", mutantSnap.Calls)
	}
	criticSnap := meters[advpool.RoleTestCritic].Snapshot()
	if criticSnap.Calls != 0 {
		t.Errorf("test-critic meter saw %d calls, want 0 — nothing called it", criticSnap.Calls)
	}

	// The whole run's total is the SUM of every role's meter — no seat's
	// spend is double-counted or dropped by being scoped per role.
	var sumCalls int64
	for _, m := range meters {
		sumCalls += m.Snapshot().Calls
	}
	if sumCalls != 3 {
		t.Errorf("sum of per-role meters = %d calls, want 3 (the run's total)", sumCalls)
	}
}

// TestARoleWithNoCallsHasNoRow pins the ZERO-CALLS-MEANS-NO-ROW rule:
// modelCallsFromMeters must omit the critic entirely when it never ran
// (`--critic-model off`, so it never has a meter at all), not emit a
// ModelCall with Calls: 0 that a warehouse query would misread as "the
// critic ran and cost nothing".
func TestARoleWithNoCallsHasNoRow(t *testing.T) {
	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "claude-sonnet-5",
		advpool.RoleTestWriter:      "claude-haiku-4-5",
		advpool.RoleTestCritic:      "off",
	}
	meters := auditRoleMeters(assign)
	if _, ok := meters[advpool.RoleTestCritic]; ok {
		t.Fatal("a role resolved to \"off\" got a meter — it never runs and must not appear in the roster")
	}

	calls := modelCallsFromMeters(meters)
	for _, c := range calls {
		if c.Role == advpool.RoleTestCritic {
			t.Fatalf("modelCallsFromMeters produced a row for the critic, which made no calls: %+v", c)
		}
	}
}

// TestModelCallsFromMetersOmitsUnusedRoles is the general form of the rule
// above: a role that HAS a meter (because a model was assigned to it) but
// that meter never actually recorded a call — a seat resolved but never
// dispatched — must still produce no row.
func TestModelCallsFromMetersOmitsUnusedRoles(t *testing.T) {
	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "claude-sonnet-5",
		advpool.RoleTestWriter:      "claude-haiku-4-5",
	}
	meters := auditRoleMeters(assign)
	// Only the mutant-generator is ever actually called.
	meters[advpool.RoleMutantGenerator].Add(agentbackend.Usage{InputTokens: 10, OutputTokens: 5})

	calls := modelCallsFromMeters(meters)
	if len(calls) != 1 {
		t.Fatalf("modelCallsFromMeters = %+v, want exactly one row (mutant-generator)", calls)
	}
	if calls[0].Role != advpool.RoleMutantGenerator {
		t.Errorf("the one row is for role %q, want %q", calls[0].Role, advpool.RoleMutantGenerator)
	}
}
