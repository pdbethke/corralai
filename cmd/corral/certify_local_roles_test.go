// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/reposcan"
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
	chatterFor, err := localChatterFor(assign, meters, nil, nil)
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

// billedThenFailedBackend is a provider that reports usage for the call it
// just made and THEN fails — the shape a real 500 after a billed call takes
// (chatConverting's own doc: "m may still carry usage for a call the
// provider billed before failing"). It exists to prove a role's spend is
// captured even when that role's call is the one that aborts the run.
type billedThenFailedBackend struct{ usage agentbackend.Usage }

func (b billedThenFailedBackend) Chat(_ []agentbackend.Message, _ []any) (agentbackend.Message, error) {
	return agentbackend.Message{Usage: b.usage}, fmt.Errorf("primary provider said 500 after billing")
}

// TestModelCallsSurviveAPrimarySeatFailure is the fix for lost spend on the
// driveLocalRun error path: before this fix, auditOneFile attached
// ModelCalls to the verdict ONLY on the success return, so a run that spent
// real tokens and then hit an infrastructure failure (a primary seat's
// provider call failing — see TestPrimarySeatFailureStillFailsTheRun, the
// gating case this mirrors) returned a bare zero Verdict and that spend
// vanished from every total built from it.
//
// This drives the exact failure runReadyTasks (and so driveLocalRun, and so
// auditOneFile) surfaces as a real Go error, through a role whose backend
// reports usage on the SAME call that fails — then applies auditOneFile's
// own fix (modelCallsFromMeters on the meters that were actually used) and
// asserts the result is non-empty and reaches a scan's summed total, the way
// it now does when stamped onto the zero verdict at the driveLocalRun error
// return in certify_local.go.
func TestModelCallsSurviveAPrimarySeatFailure(t *testing.T) {
	q, err := queue.Open(filepath.Join(t.TempDir(), "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	const mission int64 = 1
	if err := q.Enqueue(mission, []queue.TaskSpec{
		{Key: advpool.ShardTaskKey(0), Role: advpool.RoleMutantGenerator, Title: "primary", Instruction: "do"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatal(err)
	}

	meter := &agentbackend.UsageMeter{Model: "fake-model"}
	chatterFor := func(string) agentworker.Chatter {
		return agentbackend.AsChatterMetered(
			billedThenFailedBackend{usage: agentbackend.Usage{InputTokens: 900, OutputTokens: 40}}, meter)
	}

	if _, err := runReadyTasks(context.Background(), q, mission, chatterFor, nil, nil, 1, io.Discard); err == nil {
		t.Fatal("a primary seat's failed-after-billed call must still fail the run")
	}

	// The call was recorded despite the run failing — the meter is an
	// observer of what happened, not of what the run ultimately decided.
	snap := meter.Snapshot()
	if snap.Calls != 1 || snap.InputTokens != 900 || snap.OutputTokens != 40 {
		t.Fatalf("meter snapshot = %+v, want 1 call / 900 in / 40 out — the billed usage must survive the error", snap)
	}

	// auditOneFile's error path: zero.ModelCalls = modelCallsFromMeters(roles.meters).
	meters := map[string]*agentbackend.UsageMeter{advpool.RoleMutantGenerator: meter}
	calls := modelCallsFromMeters(meters)
	if len(calls) != 1 {
		t.Fatalf("modelCallsFromMeters = %+v, want exactly one row for the failed-but-billed seat", calls)
	}
	if calls[0].Role != advpool.RoleMutantGenerator || calls[0].InputTokens != 900 || calls[0].OutputTokens != 40 || calls[0].Calls != 1 {
		t.Errorf("modelCallsFromMeters row = %+v, want mutant-generator/900/40/1", calls[0])
	}

	// And the scan-wide total (built by summing every file's own
	// Verdict.ModelCalls — see scanModelCallTotals) includes a FILE THAT
	// ERRORED, exactly the case that used to vanish.
	results := []reposcan.FileResult{
		{Job: reposcan.Job{Path: "a.go"}, Verdict: advpool.Verdict{ModelCalls: calls}},
	}
	totals := scanModelCallTotals(results)
	if len(totals) != 1 || totals[0].Calls != 1 || totals[0].InputTokens != 900 {
		t.Fatalf("scanModelCallTotals = %+v, want the failed file's spend included", totals)
	}
}
