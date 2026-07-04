// SPDX-License-Identifier: Elastic-2.0
package main

import "testing"

func TestUsageTextMentionsEveryEnvVar(t *testing.T) {
	out := usageText()
	for _, want := range []string{
		"CORRAL_BRAIN", "BEE_NAME", "BEE_ROLE", "BEE_WORKSPACE", "HARNESS_CMD",
		"HARNESS_DESC", "BEE_ROUNDS", "HARNESS_TIMEOUT_SECONDS", "HARNESS_IDLE_SECONDS",
		"BEE_PROMPT_FILE",
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
