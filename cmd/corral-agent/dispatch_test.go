// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

func TestDispatchWriteFile(t *testing.T) {
	ws := t.TempDir()
	out := dispatch("Ada", "builder", ws, nil, "write_file", map[string]any{
		"path": "src/main.go", "content": "package main\nfunc main(){}\n",
	})
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("write_file: %s", out)
	}
	b, err := os.ReadFile(filepath.Join(ws, "src", "main.go"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if !strings.Contains(string(b), "package main") {
		t.Fatalf("content wrong: %q", b)
	}
}

func TestDispatchRunCommandDisabled(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })

	execRuntime = execState{enabled: false, reason: "execution disabled — test"}
	out := dispatch("Ada", "builder", t.TempDir(), nil, "run_command", map[string]any{"command": "echo hi"})
	if !strings.Contains(out, "execution disabled") {
		t.Fatalf("disabled runtime must refuse, got %s", out)
	}
}

func TestDispatchRunCommandRuns(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })

	iso, err := sandbox.Resolve(sandbox.Config{Backend: "none", UnsafeHost: true})
	if err != nil {
		t.Fatal(err)
	}
	execRuntime = execState{enabled: true, backend: iso}

	ws := t.TempDir()
	out := dispatch("Ada", "builder", ws, nil, "run_command", map[string]any{"command": "echo built-and-ran"})
	var r struct {
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if r.ExitCode != 0 || !strings.Contains(r.Output, "built-and-ran") {
		t.Fatalf("did not really execute: %+v", r)
	}
	out = dispatch("Ada", "builder", ws, nil, "run_command", map[string]any{"command": "exit 1"})
	json.Unmarshal([]byte(out), &r)
	if r.ExitCode != 1 {
		t.Fatalf("failing command should report exit 1, got %+v", r)
	}
}

// run_command must report each run to the brain (best-effort) so the swarm UI's
// live execution feed sees it — with the bee's role, exit, ok, and summary. The
// run_command RESULT must be unchanged by the report (observability only).
func TestDispatchRunCommandReportsToBrain(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "none", UnsafeHost: true})
	if err != nil {
		t.Fatal(err)
	}
	execRuntime = execState{enabled: true, backend: iso}

	var calls []map[string]any
	brain := func(tool string, args map[string]any) string {
		if tool == "report_execution" {
			calls = append(calls, args)
		}
		return `{"ok":true}`
	}

	out := dispatch("Ada", "tester", t.TempDir(), brain, "run_command", map[string]any{"command": "echo feed-me"})
	if !strings.Contains(out, "feed-me") {
		t.Fatalf("run_command result should be unchanged by the report: %s", out)
	}
	if len(calls) != 1 {
		t.Fatalf("want 1 report_execution call, got %d", len(calls))
	}
	c := calls[0]
	if c["role"] != "tester" || c["command"] != "echo feed-me" {
		t.Fatalf("report missing role/command: %+v", c)
	}
	if c["ok"] != true || c["timed_out"] != false {
		t.Fatalf("ok/timed_out wrong for a clean run: %+v", c)
	}
	if s, _ := c["summary"].(string); !strings.Contains(s, "feed-me") {
		t.Fatalf("summary should be the last output line, got %q", s)
	}

	calls = nil
	dispatch("Ada", "tester", t.TempDir(), brain, "run_command", map[string]any{"command": "exit 3"})
	if len(calls) != 1 || calls[0]["ok"] != false {
		t.Fatalf("failing run must report ok=false: %+v", calls)
	}
}

func TestSetupExecRefusesOnUnavailableBackend(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })
	t.Setenv("AGENT_ALLOW_EXEC", "1")
	t.Setenv("AGENT_EXEC_BACKEND", "container") // resolves to a not-implemented error
	t.Setenv("AGENT_EXEC_UNSAFE_HOST", "")

	setupExec()
	if execRuntime.enabled {
		t.Fatal("exec must stay disabled when the backend is unavailable")
	}
	if !strings.Contains(execRuntime.reason, "container") {
		t.Fatalf("reason should name the failure, got %q", execRuntime.reason)
	}
}

func TestSetupExecDisabledWithoutOptIn(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })
	t.Setenv("AGENT_ALLOW_EXEC", "")

	setupExec()
	if execRuntime.enabled {
		t.Fatal("exec must be off unless AGENT_ALLOW_EXEC=1")
	}
}

func TestSetupExecNetworkOptIn(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })
	t.Setenv("AGENT_ALLOW_EXEC", "1")
	t.Setenv("AGENT_EXEC_BACKEND", "none")
	t.Setenv("AGENT_EXEC_UNSAFE_HOST", "1")
	t.Setenv("AGENT_EXEC_NET", "1")

	setupExec()
	if !execRuntime.enabled {
		t.Fatal("exec should be enabled with none+unsafe")
	}
	if !execRuntime.network {
		t.Fatal("AGENT_EXEC_NET=1 must map to execRuntime.network=true")
	}
}

// The simulated edit_file (append "// [name] <one-line>" — the clobber demo's
// visible-trample mechanism) must NOT be offered to queue-mode mission bees:
// a model that picks it over write_file corrupts real artifacts with squashed
// comment lines, and the mission's verify gate ("go build") can then never
// pass — a livelock observed live in the demo-models profile (build-core
// refused 15+ times while stack.go filled with "// [Bob] …" lines).
func TestQueueToolsOmitSimulatedEditFile(t *testing.T) {
	names := func(tools []any) map[string]bool {
		out := map[string]bool{}
		for _, tl := range tools {
			b, _ := json.Marshal(tl)
			var v struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			_ = json.Unmarshal(b, &v)
			out[v.Function.Name] = true
		}
		return out
	}
	q := names(agentTools(false))
	if q["edit_file"] {
		t.Fatal("queue-mode tools must not include the simulated edit_file")
	}
	if !q["write_file"] || !q["run_command"] {
		t.Fatal("queue-mode tools must keep write_file + run_command")
	}
	d := names(agentTools(true))
	if !d["edit_file"] {
		t.Fatal("demo/ticket-mode tools must keep the simulated edit_file (the clobber demo depends on it)")
	}
}

// The model must be OFFERED report_thought (in both queue and ticket modes) —
// otherwise the narration guidance in runTask's system prompt asks for a tool
// the model can never call.
func TestAgentToolsIncludesReportThought(t *testing.T) {
	names := func(tools []any) map[string]bool {
		out := map[string]bool{}
		for _, tl := range tools {
			b, _ := json.Marshal(tl)
			var v struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			_ = json.Unmarshal(b, &v)
			out[v.Function.Name] = true
		}
		return out
	}
	for _, includeSimEdit := range []bool{false, true} {
		if !names(agentTools(includeSimEdit))["report_thought"] {
			t.Fatalf("agentTools(%v) must include report_thought", includeSimEdit)
		}
	}
}
