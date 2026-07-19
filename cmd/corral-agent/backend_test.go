// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// TestHandleTaskErrorTagsOps verifies the model-unreachable finding is filed as
// an operational marker (type "ops", severity "low"), not an audit finding —
// so it can't pollute the pool's signed verdict or block certification (Task 3
// excludes "ops" findings from the audit verdict + gate).
func TestHandleTaskErrorTagsOps(t *testing.T) {
	var got map[string]any
	brain := func(tool string, args map[string]any) string {
		if tool == "report_finding" {
			got = args
		}
		return "{}"
	}
	handled := handleTaskError(7, 1, "", "anthropic:claude-sonnet-5", fmt.Errorf("%w: 529", ErrModelUnreachable), brain)
	if !handled {
		t.Fatal("model-unreachable must be handled")
	}
	if got == nil {
		t.Fatal("a finding should be filed")
	}
	if got["type"] != "ops" {
		t.Fatalf(`operational finding must be type "ops", got %v`, got["type"])
	}
	if got["severity"] != "low" {
		t.Fatalf(`operational finding must be low severity (not a blocking audit finding), got %v`, got["severity"])
	}
}

// TestTaskLoopReleasesOnModelUnreachable verifies that handleTaskError, when called
// with ErrModelUnreachable, calls release_claims and report_finding via the brain
// and returns true (caller should continue without calling complete_task).
// A non-unreachable error must NOT trigger any of these side-effects.
func TestTaskLoopReleasesOnModelUnreachable(t *testing.T) {
	t.Run("unreachable releases and reports", func(t *testing.T) {
		var calls []string
		brain := func(tool string, args map[string]any) string {
			calls = append(calls, tool)
			return `{"ok":true}`
		}

		released := handleTaskError(42, 7, "", "ollama:test-model", ErrModelUnreachable, brain)
		if !released {
			t.Fatal("want released=true for ErrModelUnreachable")
		}
		hasClaim := false
		hasFinding := false
		for _, c := range calls {
			if c == "complete_task" {
				t.Fatalf("complete_task must NOT be called on model-unreachable; calls=%v", calls)
			}
			if c == "release_claims" {
				hasClaim = true
			}
			if c == "report_finding" {
				hasFinding = true
			}
		}
		if !hasClaim {
			t.Errorf("release_claims must be called; got calls=%v", calls)
		}
		if !hasFinding {
			t.Errorf("report_finding must be called; got calls=%v", calls)
		}
	})

	t.Run("non-unreachable error does not release", func(t *testing.T) {
		var calls []string
		brain := func(tool string, args map[string]any) string {
			calls = append(calls, tool)
			return `{"ok":true}`
		}

		otherErr := fmt.Errorf("some transient error")
		released := handleTaskError(42, 7, "", "ollama:test-model", otherErr, brain)
		if released {
			t.Fatal("want released=false for a non-unreachable error")
		}
		if len(calls) != 0 {
			t.Errorf("no brain calls expected for non-unreachable error; got %v", calls)
		}
	})

	t.Run("nil error does not release", func(t *testing.T) {
		released := handleTaskError(1, 1, "", "x", nil, func(string, map[string]any) string { return `{}` })
		if released {
			t.Fatal("want released=false for nil error")
		}
	})
}

// TestHandleTaskErrorShadowSeatBelowCap verifies a shadow-role task's
// model-unreachable failure still goes through the ordinary release+report
// path (same as any other role) as long as the brain-tracked consecutive
// count is at or below shadowUnreachableMaxAttempts — the fleet gets more
// chances before the seat is given up on.
func TestHandleTaskErrorShadowSeatBelowCap(t *testing.T) {
	var calls []string
	brain := func(tool string, args map[string]any) string {
		calls = append(calls, tool)
		if tool == "bump_unreachable_attempts" {
			return `{"attempts":1}`
		}
		return `{"ok":true}`
	}

	released := handleTaskError(42, 7, roleMutantGeneratorShadow, "ollama:test-model", ErrModelUnreachable, brain)
	if !released {
		t.Fatal("want released=true")
	}
	hasBump, hasRelease, hasComplete := false, false, false
	for _, c := range calls {
		switch c {
		case "bump_unreachable_attempts":
			hasBump = true
		case "release_claims":
			hasRelease = true
		case "complete_task":
			hasComplete = true
		}
	}
	if !hasBump {
		t.Errorf("bump_unreachable_attempts must be called for a shadow-role task; got %v", calls)
	}
	if !hasRelease {
		t.Errorf("below the cap, release_claims must still be called; got %v", calls)
	}
	if hasComplete {
		t.Errorf("below the cap, complete_task must NOT be called; got %v", calls)
	}
}

// TestHandleTaskErrorAbandonsShadowSeatAfterCap is the CRITICAL fix this test
// pins: once a shadow seat's brain-tracked consecutive model-unreachable
// count exceeds shadowUnreachableMaxAttempts, handleTaskError must stop
// releasing it for reclaim (which would let it cycle
// claim→404→release→reclaim forever, starving the fleet's primary shards —
// tickDevAdequacy blocks until every primary shard is terminal, and
// ClaimNextAs has no role priority to protect them) and instead complete the
// task itself with shadowProviderFailedResult — the sentinel the driver
// recognizes as "never successfully asked" and records UNMEASURED, not a
// fabricated zero-yield row.
func TestHandleTaskErrorAbandonsShadowSeatAfterCap(t *testing.T) {
	var calls []string
	var completeArgs map[string]any
	brain := func(tool string, args map[string]any) string {
		calls = append(calls, tool)
		switch tool {
		case "bump_unreachable_attempts":
			return `{"attempts":` + fmt.Sprint(shadowUnreachableMaxAttempts+1) + `}`
		case "complete_task":
			completeArgs = args
		}
		return `{"ok":true}`
	}

	released := handleTaskError(42, 7, roleMutantGeneratorShadow, "ollama:test-model", ErrModelUnreachable, brain)
	if !released {
		t.Fatal("want released=true (caller must skip its own complete_task either way)")
	}
	if completeArgs == nil {
		t.Fatalf("expected complete_task to be called once the cap is exceeded; calls=%v", calls)
	}
	if completeArgs["result"] != shadowProviderFailedResult {
		t.Fatalf("completed with result %v, want the shadowProviderFailedResult sentinel %q", completeArgs["result"], shadowProviderFailedResult)
	}
	for _, c := range calls {
		if c == "release_claims" {
			t.Fatalf("release_claims must NOT be called once the shadow seat is abandoned; calls=%v", calls)
		}
	}
}

// TestHandleTaskErrorNonShadowRoleNeverAbandons verifies the cap is scoped to
// the shadow role only: a PRIMARY mutant-generator (or any other role) task
// must keep releasing for reclaim no matter how many consecutive
// model-unreachable failures are reported — abandoning primary work would
// silently drop coverage, which sharding's own retry/drop path (a different,
// pre-existing mechanism) already governs.
func TestHandleTaskErrorNonShadowRoleNeverAbandons(t *testing.T) {
	var calls []string
	brain := func(tool string, args map[string]any) string {
		calls = append(calls, tool)
		if tool == "bump_unreachable_attempts" {
			t.Fatalf("bump_unreachable_attempts must not be called for a non-shadow role")
		}
		if tool == "complete_task" {
			t.Fatalf("complete_task must not be called for a non-shadow role's model-unreachable failure")
		}
		return `{"ok":true}`
	}
	released := handleTaskError(42, 7, "mutant-generator", "ollama:test-model", ErrModelUnreachable, brain)
	if !released {
		t.Fatal("want released=true")
	}
	if len(calls) != 2 { // release_claims + report_finding, same as the untyped-role path
		t.Fatalf("expected exactly release_claims + report_finding, got %v", calls)
	}
}

// TestRunTaskPropagatesModelUnreachable verifies that runTask surfaces
// ErrModelUnreachable (not a plain error string) when the backend 404s, so the
// queue loop can detect it and release the claim.
func TestRunTaskPropagatesModelUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	backend := agentbackend.NewOllamaBackend(srv.URL, "missing-model")
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }
	tools := []any{}

	_, err := runTask(context.Background(), backend, "test-agent", "builder",
		t.TempDir(), brain, tools, 1, 1, "test task", "do nothing", nil, nil, "")

	if !errors.Is(err, ErrModelUnreachable) {
		t.Errorf("runTask with 404 backend must return ErrModelUnreachable; got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "model unreachable") {
		t.Errorf("error message should mention the cause; got %v", err)
	}
}

// scriptedBackend replays a fixed sequence of tool calls, one per Chat call,
// then falls back to a plain "done" answer with no tool call (ends the loop).
type scriptedBackend struct {
	calls []otoolcal
	i     int
}

func (b *scriptedBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	if b.i < len(b.calls) {
		tc := b.calls[b.i]
		b.i++
		return omsg{Role: "assistant", ToolCalls: []otoolcal{tc}}, nil
	}
	return omsg{Role: "assistant", Content: "done"}, nil
}

func toolCall(name string, args map[string]any) otoolcal {
	b, _ := json.Marshal(args)
	var tc otoolcal
	tc.Function.Name = name
	tc.Function.Arguments = b
	return tc
}

// TestRunTaskStampsReportThought verifies runTask scopes a model-issued
// report_thought call the same way it already scopes report_finding: the
// model only supplies the reasoning text, and the harness stamps mission_id,
// role, and name onto it before it reaches the brain — the model can't know
// (and shouldn't need to know) its own mission_id.
func TestRunTaskStampsReportThought(t *testing.T) {
	backend := &scriptedBackend{calls: []otoolcal{
		toolCall("report_thought", map[string]any{"text": "the retry loop never backs off; checking the interval"}),
	}}
	var captured map[string]any
	brain := func(tool string, args map[string]any) string {
		if tool == "report_thought" {
			captured = args
		}
		return `{"ok":true}`
	}

	_, err := runTask(context.Background(), backend, "Ada", "builder",
		t.TempDir(), brain, nil, 99, 7, "test task", "do nothing", nil, nil, "")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if captured == nil {
		t.Fatal("report_thought was never forwarded to the brain")
	}
	if captured["mission_id"] != int64(99) && captured["mission_id"] != float64(99) {
		t.Errorf("mission_id must be stamped from the task, got %v", captured["mission_id"])
	}
	if captured["role"] != "builder" {
		t.Errorf("role must be stamped, got %v", captured["role"])
	}
	if captured["name"] != "Ada" {
		t.Errorf("name must be stamped, got %v", captured["name"])
	}
	if captured["text"] != "the retry loop never backs off; checking the interval" {
		t.Errorf("the model's own reasoning text must pass through unchanged, got %v", captured["text"])
	}
}
