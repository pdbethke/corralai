// SPDX-License-Identifier: Elastic-2.0

package main

import "testing"

// TestShowHelpRecognizesFlags proves -h/--help/help are recognized WITHOUT
// starting the server — this is the fix for corral's "the generator hangs
// forever because `corral -h` boots the brain" bug found while wiring
// scripts/gen-cli-docs.sh: with no help recognized at all, `-h` fell through
// to main()'s server startup and never exited.
func TestShowHelpRecognizesFlags(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		if !showHelp(args) {
			t.Errorf("showHelp(%v) = false, want true", args)
		}
	}
	if showHelp([]string{}) {
		t.Error("showHelp([]) = true, want false")
	}
	if showHelp([]string{"--version"}) {
		t.Error("showHelp([--version]) = true, want false")
	}
}

func TestUsageTextMentionsAddrAndDB(t *testing.T) {
	out := usageText()
	for _, want := range []string{"CORRALAI_ADDR", "CORRALAI_DB", "Usage:"} {
		if !containsUsage(out, want) {
			t.Errorf("usageText() missing %q\n---\n%s", want, out)
		}
	}
}

func containsUsage(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
