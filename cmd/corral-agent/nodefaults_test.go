// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain lets a test re-run this binary as the real corral-agent: with
// CORRAL_AGENT_AS_MAIN set the process is main(), anything else is the tests.
func TestMain(m *testing.M) {
	if os.Getenv("CORRAL_AGENT_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// runAgent runs corral-agent's main in a child process with exactly env, and
// returns its exit code and stderr. Nothing is inherited, so a refusal is the
// agent's, not the host's.
func runAgent(t *testing.T, env ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append([]string{"CORRAL_AGENT_AS_MAIN=1", "CORRAL_BRAIN=http://127.0.0.1:1/mcp/", "AGENT_WORKSPACE=" + t.TempDir(), "HOME=" + t.TempDir()}, env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), stderr.String()
	}
	if err != nil {
		t.Fatalf("running the agent: %v", err)
	}
	return 0, stderr.String()
}

// corral-agent has no default backend, model or endpoint: a worker started
// without them exits 2 naming what to set, before it connects to any brain.
func TestAgentRefusesWithoutADeclaredBackend(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"no MODEL_BACKEND", []string{"AGENT_MODEL=m"}, "MODEL_BACKEND"},
		{"no AGENT_MODEL", []string{"MODEL_BACKEND=ollama", "OLLAMA_URL=http://127.0.0.1:1"}, "AGENT_MODEL"},
		{"ollama without OLLAMA_URL", []string{"MODEL_BACKEND=ollama", "AGENT_MODEL=m"}, "OLLAMA_URL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, stderr := runAgent(t, c.env...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, c.want) {
				t.Errorf("stderr must name %s, got:\n%s", c.want, stderr)
			}
		})
	}
}
