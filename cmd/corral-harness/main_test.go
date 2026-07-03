// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"
)

func TestSplitCmdQuoting(t *testing.T) {
	got := splitCmd(`claude -p {prompt} --allowedTools "mcp__corral__*,Read,Write" --permission-mode acceptEdits`)
	want := []string{"claude", "-p", "{prompt}", "--allowedTools", "mcp__corral__*,Read,Write", "--permission-mode", "acceptEdits"}
	if len(got) != len(want) {
		t.Fatalf("split = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("split[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The prompt must substitute as ONE argv token even though it spans many
// lines — no shell ever sees it.
func TestExpandSubstitutesWholeToken(t *testing.T) {
	argvTok := expand("{prompt}", map[string]string{"prompt": "line one\nline two \"quoted\""})
	if argvTok != "line one\nline two \"quoted\"" {
		t.Fatalf("prompt must pass through verbatim, got %q", argvTok)
	}
	if got := expand("--mcp-config={mcp_config}", map[string]string{"mcp_config": "/tmp/x.json"}); got != "--mcp-config=/tmp/x.json" {
		t.Fatalf("inline placeholder expansion failed: %q", got)
	}
}

func TestBeePromptCarriesTheContract(t *testing.T) {
	p := beePrompt("Cody", "builder", "host-1", "claude (headless harness)")
	for _, must := range []string{
		"bootstrap", "claim_task", "complete_task", "claim_paths",
		"report_finding", "report_execution", "search_memory", "add_memory",
		"IDLE", `"roles":["builder"]`, `"instance":"host-1"`, "EXACTLY ONE task",
	} {
		if !strings.Contains(p, must) {
			t.Fatalf("bee prompt missing %q:\n%s", must, p)
		}
	}
}

func TestMcpConfigShape(t *testing.T) {
	got := mcpConfig("http://localhost:9019")
	if !strings.Contains(got, `"corral"`) || !strings.Contains(got, `http://localhost:9019/mcp/`) {
		t.Fatalf("unexpected mcp config: %s", got)
	}
	// trailing slash on the brain URL must not double up
	if strings.Contains(mcpConfig("http://b:9019/"), "9019//") {
		t.Fatal("double slash in mcp url")
	}
}
