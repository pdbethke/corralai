// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestRepoScanThreadsTheChallengerWriter pins the seat a repo scan could not
// fill at all.
//
// --shadow-writer-model existed ONLY in `certify --local`, so the writer-shadow
// correlation — Jaccard over survivors, the measurement that says whether two
// seats miss the SAME mutants — could only ever be computed one file at a time.
// A whole-repo scan, the mode with enough surface for a coefficient to mean
// anything, was structurally unable to produce one.
//
// A flag that parses and is then dropped before it reaches the per-file input
// is the silently-discarded-input shape this codebase keeps producing, so this
// asserts the value ARRIVES, not merely that the flag exists.
func TestRepoScanThreadsTheChallengerWriter(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), []string{"go", "test", "./..."}, "jail", 0, nil)
	ex.models = auditModels{
		writer:       "gemini-3.7-flash",
		mutant:       "qwen3.5:9b-q8_0",
		shadowWriter: "claude-haiku-4-5-20251001",
	}

	in := ex.auditInputFor(reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"})
	if in.shadowWriterModel != "claude-haiku-4-5-20251001" {
		t.Fatalf("shadowWriterModel = %q, want the challenger to reach the per-file input", in.shadowWriterModel)
	}
	if in.writerModel != "gemini-3.7-flash" {
		t.Errorf("primary writer = %q, want it unchanged", in.writerModel)
	}
}

// Unset, the seat stays OFF — the challenger must never be forced on, since it
// costs a second writer per file and names a vendor the operator did not pick.
func TestChallengerWriterOffUnlessNamed(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), nil, "jail", 0, nil)
	ex.models = auditModels{writer: "gemini-3.7-flash", mutant: "qwen3.5:9b-q8_0"}
	if in := ex.auditInputFor(reposcan.Job{Path: "a.go", Lang: "go"}); in.shadowWriterModel != "" {
		t.Fatalf("shadowWriterModel = %q with no flag, want empty", in.shadowWriterModel)
	}
}

// The resolved model set must NAME the challenger, so a ledger row and a cache
// key can tell a run that had one from a run that did not. Without this, two
// different audits collapse onto one identity.
func TestModelSetNamesTheChallenger(t *testing.T) {
	with := modelSetKey("w", "m", "", "", "challenger-x")
	without := modelSetKey("w", "m", "", "", "")
	if with == without {
		t.Fatal("model set is identical with and without a challenger: the ledger cannot tell the runs apart")
	}
	if !strings.Contains(with, "challenger-x") {
		t.Errorf("model set %q does not name the challenger", with)
	}
	_ = advpool.RoleTestWriterShadow
}
