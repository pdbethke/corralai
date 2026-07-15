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
	"time"
)

// TestBackendReports404AsUnreachable verifies that HTTP 404 from any backend
// yields an error where errors.Is(err, ErrModelUnreachable) is true, and that
// non-404 HTTP errors (e.g. 500) are NOT classified as unreachable.
func TestBackendReports404AsUnreachable(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		status := status
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"test"}`))
			}))
			defer srv.Close()

			// Exercise all three backends through postJSON (they all funnel through it).
			tests := []struct {
				name    string
				backend Backend
			}{
				{"ollama", &ollamaBackend{url: srv.URL, model: "test-model"}},
				{"openai", &openaiBackend{base: srv.URL, model: "test-model"}},
				{"anthropic", &anthropicBackend{base: srv.URL, model: "test-model"}},
			}
			for _, tc := range tests {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					_, err := tc.backend.Chat([]omsg{{Role: "user", Content: "hello"}}, nil)
					if err == nil {
						t.Fatalf("%s: want error for HTTP %d, got nil", tc.name, status)
					}
					isUnreachable := errors.Is(err, ErrModelUnreachable)
					if status == http.StatusNotFound && !isUnreachable {
						t.Errorf("%s: HTTP 404 must classify as ErrModelUnreachable, got %v", tc.name, err)
					}
					if status != http.StatusNotFound && isUnreachable {
						t.Errorf("%s: HTTP %d must NOT classify as ErrModelUnreachable, got %v", tc.name, status, err)
					}
				})
			}
		})
	}
}

// TestBackendConnectionRefusedIsUnreachable verifies that a backend pointed at a
// closed port (connection refused) also returns ErrModelUnreachable.
func TestBackendConnectionRefusedIsUnreachable(t *testing.T) {
	// Bind to an ephemeral port then immediately close — guaranteed connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // close right away so the port is unreachable

	b := &ollamaBackend{url: addr, model: "test-model"}
	_, err := b.Chat([]omsg{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("want error for connection-refused, got nil")
	}
	if !errors.Is(err, ErrModelUnreachable) {
		t.Errorf("connection-refused must classify as ErrModelUnreachable, got %v", err)
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

		released := handleTaskError(42, 7, "ollama:test-model", ErrModelUnreachable, brain)
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
		released := handleTaskError(42, 7, "ollama:test-model", otherErr, brain)
		if released {
			t.Fatal("want released=false for a non-unreachable error")
		}
		if len(calls) != 0 {
			t.Errorf("no brain calls expected for non-unreachable error; got %v", calls)
		}
	})

	t.Run("nil error does not release", func(t *testing.T) {
		released := handleTaskError(1, 1, "x", nil, func(string, map[string]any) string { return `{}` })
		if released {
			t.Fatal("want released=false for nil error")
		}
	})
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

	backend := &ollamaBackend{url: srv.URL, model: "missing-model"}
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

func TestLLMHTTPTimeout(t *testing.T) {
	t.Setenv("AGENT_LLM_TIMEOUT_SECONDS", "")
	if got := llmHTTPTimeout(); got != 180*time.Second {
		t.Errorf("unset default = %v, want 180s", got)
	}
	t.Setenv("AGENT_LLM_TIMEOUT_SECONDS", "600")
	if got := llmHTTPTimeout(); got != 600*time.Second {
		t.Errorf("override = %v, want 600s", got)
	}
	for _, bad := range []string{"-5", "0", "abc", "  "} {
		t.Setenv("AGENT_LLM_TIMEOUT_SECONDS", bad)
		if got := llmHTTPTimeout(); got != 180*time.Second {
			t.Errorf("invalid %q = %v, want default 180s", bad, got)
		}
	}
}
