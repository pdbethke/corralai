// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentrole"
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
	p := beePrompt("Cody", agentrole.Parse("builder"), "host-1", "claude (headless harness)", "", "")
	for _, must := range []string{
		"bootstrap", "claim_task", "complete_task", "claim_paths",
		"report_finding", "report_execution", "search_memory", "add_memory",
		"IDLE", `"roles":["builder"]`, `"instance":"host-1"`, "EXACTLY ONE task",
	} {
		if !strings.Contains(p, must) {
			t.Fatalf("bee prompt missing %q:\n%s", must, p)
		}
	}
	// Without a declared model there must be no report_host step — the brain
	// would record an empty model for the bee.
	if strings.Contains(p, "report_host") {
		t.Fatalf("bee prompt must not mention report_host when no model is declared:\n%s", p)
	}
}

// Multi-role: a comma-separated AGENT_ROLE claims any ready task across the
// whole set (#23/#39 — a small herd covering all planned roles).
func TestBeePromptMultiRoleClaimsWholeSet(t *testing.T) {
	p := beePrompt("Cody", agentrole.Parse("researcher, designer,tester"), "host-1", "claude (headless harness)", "", "")
	for _, must := range []string{
		`"roles":["researcher","designer","tester"]`,
		"researcher+designer+tester", // role display in the "you are a %s bee" line and bootstrap's role field
	} {
		if !strings.Contains(p, must) {
			t.Fatalf("multi-role bee prompt missing %q:\n%s", must, p)
		}
	}
}

// Generalist: AGENT_ROLE=any/*/empty must OMIT the roles filter — an empty
// array reaches the brain's documented "claim any ready task" behaviour the
// same as omitting the key (internal/queue/store.go ClaimNextAs).
func TestBeePromptAnyRoleOmitsFilter(t *testing.T) {
	for _, raw := range []string{"any", "*", ""} {
		p := beePrompt("Cody", agentrole.Parse(raw), "host-1", "claude (headless harness)", "", "")
		if !strings.Contains(p, `"roles":[]`) {
			t.Fatalf("AGENT_ROLE=%q: bee prompt must claim with an empty roles filter, got:\n%s", raw, p)
		}
		if !strings.Contains(p, "generalist") {
			t.Fatalf("AGENT_ROLE=%q: bee prompt must announce as generalist:\n%s", raw, p)
		}
	}
}

// Model attribution: findings are stamped from the HostBook, which only
// report_host feeds. A harness bee that never calls report_host files
// findings that show as "(not recorded)" in model_comparison — so when the
// operator declares AGENT_MODEL/AGENT_BACKEND, the prompt must instruct the
// harness to announce them.
func TestBeePromptAnnouncesDeclaredModel(t *testing.T) {
	p := beePrompt("Cody", agentrole.Parse("reviewer"), "host-1", "codex (headless harness)", "gpt-5.1-codex", "openai")
	for _, must := range []string{
		"report_host",
		`"model":"gpt-5.1-codex"`,
		`"backend":"openai"`,
		// jail is a REQUIRED report_host param — without it in the example the
		// first call errors and the harness burns a retry (seen live: Gemini
		// CLI got "params must have required property 'jail'" and had to
		// self-correct).
		`"jail":"none"`,
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
