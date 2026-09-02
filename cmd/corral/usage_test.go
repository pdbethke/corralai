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

// TestHelpNeverRequiresState pins the rule that a command must be able to say
// what it does on a bare machine.
//
// `corral criticscore -h` and `corral scorecard -h` opened their DuckDB store
// BEFORE parsing, and both stores are created lazily by the runs that write
// them — so a new operator, who has neither, got
//
//	corral scorecard: open bugcatch store: … IO Error: Cannot open file …
//
// where usage should have been. Asking a command what it does is the one
// request that cannot require the answer to already exist.
//
// CI found it and could only have found it there: the generated CLI reference
// captures each subcommand's real -h, and on a fresh runner the captured
// "help" WAS that error. Which is the deeper point — a generated reference is
// host-dependent exactly when help is.
//
// Asserted through wantsHelp rather than by executing the binary: the guard is
// what the dispatch consults before it opens anything, so this is the seam
// where the rule actually lives.
func TestHelpNeverRequiresState(t *testing.T) {
	for _, args := range [][]string{
		{"-h"}, {"--help"}, {"help"},
		{"list", "-h"},           // a subcommand's own help
		{"--why", "x", "--help"}, // help after other flags
	} {
		if !wantsHelp(args) {
			t.Errorf("wantsHelp(%q) = false — the dispatch would open a store to answer a help request, which fails on any machine that has not run the command yet", args)
		}
	}
	for _, args := range [][]string{
		{}, {"list"}, {"confirm", "7"}, {"--json"},
	} {
		if wantsHelp(args) {
			t.Errorf("wantsHelp(%q) = true — a real invocation would be answered with usage and never run", args)
		}
	}
}
