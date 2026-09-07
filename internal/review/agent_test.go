// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"strings"
	"testing"
)

// Both agentic briefs carry the one tree paragraph, and neither tells an
// agent it cannot run commands — an agent whose only file interface is a
// shell (Codex) took that literally, twice, and read nothing.
func TestAgentBriefsShareTheTreeParagraphAndPermitReadingByShell(t *testing.T) {
	reviewer := AgentBrief("r", "c", "pkg", []string{"pkg/a.go"})
	verifier := VerifierAgentBrief(Review{Repo: "r", Commit: "c", Scope: "pkg", Findings: []Finding{{ID: "R1", Claim: "x", File: "pkg/a.go"}}}, []string{"pkg/a.go"})
	for name, brief := range map[string]string{"reviewer": reviewer, "verifier": verifier} {
		for _, want := range []string{"DISPOSABLE COPY", "a shell used only to read (cat, sed, grep, ls, find)", "Do NOT run the tests, build, or execute the code under review", "nothing you execute yourself counts", "sh SCRIPT you hand back"} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief lacks %q:\n%s", name, want, brief)
			}
		}
		if strings.Contains(brief, "cannot run") || strings.Contains(brief, "must not try") {
			t.Errorf("%s brief still forbids commands, which forbids reading for a shell-only agent:\n%s", name, brief)
		}
	}
	if !strings.Contains(reviewer, "REPRODUCED finding is") || !strings.Contains(verifier, "REPRODUCED refutation is") {
		t.Error("the paragraph must name what a seat hands back: a finding, or a refutation")
	}
}
