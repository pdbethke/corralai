// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"testing"
)

// TestIsPoolCriticRole is a table test on the pure role classifier: only
// test-critic is a pool critic role — every builder/tester/structured role
// stays on its existing path (general 15-step loop or the structured fast
// path), completely unaffected.
//
// The critic LOOP itself (tool restriction, step bound, ErrModelUnreachable
// propagation) now lives in internal/agentworker (RunRole) and is tested
// there; this file only covers the role classifier + the wiring at this call
// site (runTask forwards agentworker.RunRole's findings to the brain — see
// TestRunTask_TestCritic_ForwardsFindingsToBrain in runtask_test.go).
func TestIsPoolCriticRole(t *testing.T) {
	if !isPoolCriticRole("test-critic") {
		t.Fatal("test-critic must be a pool critic role")
	}
	for _, r := range []string{"mutant-generator", "test-writer", "builder", "tester", ""} {
		if isPoolCriticRole(r) {
			t.Fatalf("%q must not be a pool critic role", r)
		}
	}
}

// TestRunTaskCriticForwardsFindingsToBrain verifies the wiring this call site
// now owns: agentworker.RunRole runs the critic loop and hands back findings
// in-process (it has no brain to call), so runTask itself must forward each
// one to the brain via report_finding — otherwise the pool driver's verdict
// would never see what the critic found.
func TestRunTaskCriticForwardsFindingsToBrain(t *testing.T) {
	backend := &scriptedBackend{calls: []otoolcal{
		toolCall("report_finding", map[string]any{
			"type": "bug", "severity": "high", "target": "TestFoo",
			"evidence": "asserts nothing", "suggested_action": "assert on the result",
		}),
	}}
	var reported []map[string]any
	brain := func(tool string, args map[string]any) string {
		if tool == "report_finding" {
			reported = append(reported, args)
		}
		return `{"ok":true}`
	}

	summary, err := runTask(context.Background(), backend, "test-agent", "test-critic",
		t.TempDir(), brain, nil, 42, 7, "critique tests", "TESTS HERE", nil, nil, "")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if summary == "" {
		t.Errorf("expected a non-empty critic summary")
	}
	if len(reported) != 1 {
		t.Fatalf("expected exactly 1 report_finding call to the brain; got %d (%+v)", len(reported), reported)
	}
	f := reported[0]
	if f["mission_id"] != int64(42) || f["task_id"] != int64(7) {
		t.Errorf("report_finding must be scoped to the claimed mission/task; got %+v", f)
	}
	if f["type"] != "bug" || f["severity"] != "high" || f["target"] != "TestFoo" {
		t.Errorf("report_finding must carry the critic's finding fields verbatim; got %+v", f)
	}
}

// TestRunTaskCriticPropagatesModelUnreachable is the RED/GREEN case for the
// honesty-hole fix carried over from the pre-extraction runCriticLoop: a down
// critic model must release the claim (ErrModelUnreachable), not complete
// past it — otherwise a mission could converge/certify with no critic having
// actually run.
func TestRunTaskCriticPropagatesModelUnreachable(t *testing.T) {
	backend := &unreachableBackend{}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	_, err := runTask(context.Background(), backend, "test-agent", "test-critic",
		t.TempDir(), brain, nil, 1, 1, "critique tests", "TESTS HERE", nil, nil, "")
	if !errors.Is(err, ErrModelUnreachable) {
		t.Fatalf("expected errors.Is(err, ErrModelUnreachable); got err=%v", err)
	}
}

// unreachableBackend simulates a model that can't be reached at all (404 /
// connection-refused).
type unreachableBackend struct{}

func (b *unreachableBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	return omsg{}, ErrModelUnreachable
}
