// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTaskModel is a table test on the pure selection helper: it must prefer
// the driver's gate-earned assignment when present, and fall back to the
// worker's own default (AGENT_MODEL) when the task carries none — the "empty
// task.Model = unchanged behavior" contract.
func TestTaskModel(t *testing.T) {
	cases := []struct {
		name          string
		assignedModel string
		defaultModel  string
		want          string
	}{
		{"assigned model wins", "claude-sonnet-4-6", "qwen2.5-coder:7b", "claude-sonnet-4-6"},
		{"empty assignment falls back to default", "", "qwen2.5-coder:7b", "qwen2.5-coder:7b"},
		{"assignment equal to default is a no-op either way", "qwen2.5-coder:7b", "qwen2.5-coder:7b", "qwen2.5-coder:7b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskModel(tc.assignedModel, tc.defaultModel); got != tc.want {
				t.Errorf("taskModel(%q, %q) = %q, want %q", tc.assignedModel, tc.defaultModel, got, tc.want)
			}
		})
	}
}

// TestRunTaskUsesAssignedModelWhenBackendCanSwitch verifies the honest
// happy path: given a backend that implements modelSwitcher (ollamaBackend
// does), a task carrying a Model different from AGENT_MODEL actually gets
// run against the assigned model — not the worker's configured default.
func TestRunTaskUsesAssignedModelWhenBackendCanSwitch(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": "done"},
		})
	}))
	defer srv.Close()

	backend := &ollamaBackend{url: srv.URL, model: "qwen2.5-coder:7b"}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runTask(context.Background(), backend, "test-agent", "builder",
		t.TempDir(), brain, nil, 1, 1, "test task", "do nothing", nil, nil, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if gotModel != "claude-sonnet-4-6" {
		t.Errorf("backend received model %q, want the task's assigned model %q", gotModel, "claude-sonnet-4-6")
	}
	if strings.Contains(summary, "not assigned") {
		t.Errorf("summary should NOT carry a mismatch note when the backend honored the assignment; got %q", summary)
	}
	// The worker's own model default must be untouched by the per-task switch.
	if backend.model != "qwen2.5-coder:7b" {
		t.Errorf("original backend.model mutated to %q; WithModel must not mutate the receiver", backend.model)
	}
}

// TestRunTaskRecordsMismatchWhenBackendCannotSwitch is the honest fallback:
// a single-model harness (a backend that does NOT implement modelSwitcher)
// cannot serve an arbitrary assigned model. It must keep running its own
// model but say so in the completion result — never silently pretend it ran
// the assignment.
func TestRunTaskRecordsMismatchWhenBackendCannotSwitch(t *testing.T) {
	backend := &scriptedBackend{} // no tool calls queued: falls straight to "done", ending the loop
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runTask(context.Background(), backend, "test-agent", "builder",
		t.TempDir(), brain, nil, 1, 1, "test task", "do nothing", nil, nil, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !strings.Contains(summary, "ran") || !strings.Contains(summary, "not assigned claude-sonnet-4-6") {
		t.Errorf("summary must honestly record the model mismatch; got %q", summary)
	}
}

// TestRunTaskNoAssignmentIsUnchanged confirms empty task.Model (the pool's
// existing/unassigned-task behavior) never triggers a mismatch note, even on
// a backend that can't switch models — nothing about that case changed.
func TestRunTaskNoAssignmentIsUnchanged(t *testing.T) {
	backend := &scriptedBackend{}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runTask(context.Background(), backend, "test-agent", "builder",
		t.TempDir(), brain, nil, 1, 1, "test task", "do nothing", nil, nil, "")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if strings.Contains(summary, "not assigned") {
		t.Errorf("no task.Model set — summary must not carry a mismatch note; got %q", summary)
	}
}
