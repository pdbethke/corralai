// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestTimeoutHelpDescribesAWallClockBudget pins a help text against the code
// it describes.
//
// --timeout's help said "give up if the run makes no progress for this long
// (not a hard wall-clock cap)". The driver checks
// `d.Now().Sub(run.startedAt) > d.RunDeadline` — elapsed since START — and
// advpool's own comment calls it "a wall-clock budget from run start". Those
// are opposite claims about the same number.
//
// The gap is not cosmetic. An operator reading "no progress" hears a stall
// detector and leaves the default alone; what they get is a total budget, so a
// run making steady, productive progress is killed and banks a TIMEOUT verdict
// that keeps the dev kill rate and DISCARDS the proving half. Measured on
// afero's memmap.go against a recorded mutant set: 13 survivors, all 13 proven
// in 1428s at --swarm 8, and "proven_missed: (not run — pool did not
// converge)" under the 10-minute default. The proofs are the differentiator,
// and the default was throwing them away on exactly the files with the most to
// find.
//
// Read from the GENERATED reference rather than a copy of the string:
// scripts/gen-cli-docs.sh --check already guarantees that file is what the
// binaries really print, so this cannot pass against stale text.
func TestTimeoutHelpDescribesAWallClockBudget(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli", "corral.md"))
	if err != nil {
		t.Fatalf("reading the generated reference: %v", err)
	}
	lines := strings.Split(string(b), "\n")

	found := 0
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "-timeout ") {
			continue
		}
		found++
		// The description is the indented continuation under the flag line.
		var desc strings.Builder
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" || strings.HasPrefix(strings.TrimSpace(next), "-") {
				break
			}
			desc.WriteString(next)
		}
		got := desc.String()
		if strings.Contains(got, "no progress") {
			t.Errorf("--timeout still describes a no-progress timer:\n%s\nThe driver measures elapsed time from run start, so this tells an operator the opposite of what happens.", got)
		}
		if !strings.Contains(strings.ToUpper(got), "WALL-CLOCK") {
			t.Errorf("--timeout does not say it is a wall-clock budget:\n%s", got)
		}
	}
	// certify --local and certify --repo both register one. A walk that found
	// neither would pass green forever.
	if found < 2 {
		t.Fatalf("found %d -timeout flags in the generated reference, want at least 2 (--local and --repo)", found)
	}
}
