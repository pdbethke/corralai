// SPDX-License-Identifier: Elastic-2.0
package main

import "testing"

func TestPrintUsageMentionsEveryEnvVar(t *testing.T) {
	out := usageText()
	for _, want := range []string{
		"CORRAL_BRAIN", "AGENT_ROLE", "AGENT_NAME", "AGENT_WORKSPACE",
		"MODEL_BACKEND", "AGENT_MODEL", "CLOBBER",
	} {
		if !contains(out, want) {
			t.Errorf("usageText() missing env var %q\n---\n%s", want, out)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
