// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// Agentic seats: a coding CLI as the reviewer or the verifier. The seat is
// named `claude-code`, `codex`, or `claude-code:<model>` / `codex:<model>`
// to pin the model the CLI runs. The CLI is started in a disposable
// worktree at the commit with READ-ONLY tools, the brief on stdin, and its
// final message on stdout is the reply. It reads the tree itself — no byte
// cap — and it hands back scripts; corral runs them. This is what the five
// cold reviews of corral were, by hand: a coding agent handed a checkout
// and told to break it.

// agentTools are the CLIs a seat can name.
var agentTools = map[string]bool{"claude-code": true, "codex": true}

// agentSeat splits "claude-code:claude-sonnet-5" into tool and model;
// ok is false for an API model name.
func agentSeat(spec string) (tool, model string, ok bool) {
	tool, model, _ = strings.Cut(strings.TrimSpace(spec), ":")
	if !agentTools[tool] {
		return "", "", false
	}
	return tool, strings.TrimSpace(model), true
}

// seatModelOf is the model a seat spec resolves to for the decorrelation
// rule: an API seat is its own name; an agentic seat pinned to a model is
// that model; an unpinned agentic seat is the tool.
func seatModelOf(spec string) string {
	if tool, model, ok := agentSeat(spec); ok {
		if model == "" {
			return tool
		}
		return model
	}
	return strings.TrimSpace(spec)
}

// agentBackend runs the CLI once per Chat.
type agentBackend struct {
	tool, model, dir string
	timeout          time.Duration
}

// agentVersion is what `<tool> --version` prints, for the record: a
// "claude-code" row in models rank is a tool AND a version AND a model.
func agentVersion(tool string) string {
	bin := map[string]string{"claude-code": "claude", "codex": "codex"}[tool]
	out, err := exec.Command(bin, "--version").Output() // #nosec G204 -- fixed argv, a binary this code names
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

func (a agentBackend) Chat(messages []agentbackend.Message, _ []any) (agentbackend.Message, error) {
	var prompt strings.Builder
	for _, m := range messages {
		if prompt.Len() > 0 {
			prompt.WriteString("\n\n")
		}
		prompt.WriteString(m.Content)
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	var cmd *exec.Cmd
	var lastMsg string
	switch a.tool {
	case "claude-code":
		args := []string{"-p", "--output-format", "text", "--tools", "Read,Grep,Glob"}
		if a.model != "" {
			args = append(args, "--model", a.model)
		}
		cmd = exec.CommandContext(ctx, "claude", args...) // #nosec G204 -- fixed argv plus a model name the operator chose
	case "codex":
		f, err := os.CreateTemp("", "corral-codex-*.txt")
		if err != nil {
			return agentbackend.Message{}, err
		}
		lastMsg = f.Name()
		f.Close()
		defer os.Remove(lastMsg)
		args := []string{"exec", "--sandbox", "read-only", "--skip-git-repo-check", "-C", a.dir, "-o", lastMsg}
		if a.model != "" {
			args = append(args, "-m", a.model)
		}
		args = append(args, "-")
		cmd = exec.CommandContext(ctx, "codex", args...) // #nosec G204 -- fixed argv plus a model name the operator chose
	default:
		return agentbackend.Message{}, fmt.Errorf("no agentic seat named %q", a.tool)
	}
	cmd.Dir = a.dir
	cmd.Stdin = strings.NewReader(prompt.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return agentbackend.Message{}, fmt.Errorf("%s: %v: %s", a.tool, err, tail(strings.TrimSpace(stderr.String()), 800))
	}
	content := stdout.String()
	if lastMsg != "" {
		if b, err := os.ReadFile(lastMsg); err == nil && len(bytes.TrimSpace(b)) > 0 { // #nosec G304 -- our own temp file
			content = string(b)
		}
	}
	return agentbackend.Message{Role: "assistant", Content: content}, nil
}

// newAgentSeat is the backend for an agentic seat in dir (a disposable
// worktree), or nil, false for an API seat.
func newAgentSeat(spec, dir string, timeout time.Duration) (agentbackend.Backend, bool) {
	tool, model, ok := agentSeat(spec)
	if !ok {
		return nil, false
	}
	return agentBackend{tool: tool, model: model, dir: filepath.Clean(dir), timeout: timeout}, true
}
