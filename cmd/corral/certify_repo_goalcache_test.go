// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestGoalCacheDisclosureLineIsUnchangedWhenNothingWasReused pins the
// "derived-only wording unchanged when M==0" rule: a scan that touched the
// goal cache but reused nothing must print EXACTLY the line
// resolveGoalSource has always produced, not a new-format line claiming 0
// reused.
func TestGoalCacheDisclosureLineIsUnchangedWhenNothingWasReused(t *testing.T) {
	base := "  goals derived per file by test-model-x@v1.2.3 — no goal-critic; each goal is judged after the fact by mutant yield"
	got := goalCacheDisclosureLine(base, "test-model-x", "v1.2.3", 3, 0)
	if got != base {
		t.Errorf("goalCacheDisclosureLine with reused=0 = %q, want the base line unchanged:\n%q", got, base)
	}
}

// TestGoalCacheDisclosureLineGoldenMixed is the golden for a scan that
// derived some goals fresh and reused others from an identical prior scan.
func TestGoalCacheDisclosureLineGoldenMixed(t *testing.T) {
	base := "  goals derived per file by test-model-x@v1.2.3 — no goal-critic; each goal is judged after the fact by mutant yield"
	got := goalCacheDisclosureLine(base, "test-model-x", "v1.2.3", 2, 3)
	want := "  goals: 2 derived by test-model-x@v1.2.3, 3 reused (identical source)"
	if got != want {
		t.Errorf("goalCacheDisclosureLine mixed = %q, want %q", got, want)
	}
}

// countingDeriver is a reposcan.Deriver that counts calls and always
// succeeds — the CLI-layer counterpart to reposcan's countingGoalSource,
// used here to prove the --no-goal-cache flag actually disables the wiring
// rather than merely disclosing something different.
type countingDeriver struct{ calls *int }

func (d countingDeriver) Derive(context.Context, reposcan.Candidate, string) (string, bool, error) {
	*d.calls++
	return "must never panic", true, nil
}

// TestNoGoalCacheFlagDerivesEveryTime is I3's sibling for the cache: with
// the cache wired, a second GoalFor over identical bytes must not reach the
// deriver again; with --no-goal-cache (store not wired), every call must.
func TestNoGoalCacheFlagDerivesEveryTime(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package p\n")
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	store := newGoalLedgerCache(dsn)

	callsFor := func(store reposcan.GoalCacheStore) int {
		calls := 0
		var errb bytes.Buffer
		gs, _, code := resolveGoalSource(&errb, root, "", "test-model-x", false, 1,
			func(string) (reposcan.Deriver, error) { return countingDeriver{calls: &calls}, nil },
			store, false)
		if code != 0 {
			t.Fatalf("resolveGoalSource: code=%d stderr=%s", code, errb.String())
		}
		c := reposcan.Candidate{Path: "a.go", Lang: "go"}
		if _, _, err := gs.GoalFor(c); err != nil {
			t.Fatalf("GoalFor: %v", err)
		}
		if _, _, err := gs.GoalFor(c); err != nil {
			t.Fatalf("GoalFor: %v", err)
		}
		return calls
	}

	if n := callsFor(store); n != 1 {
		t.Errorf("with the cache wired, two calls over identical bytes should derive once, got %d", n)
	}
	if n := callsFor(nil); n != 2 {
		t.Errorf("with no store wired (the --no-goal-cache shape), every call must derive, got %d", n)
	}
}

// TestResolveGoalSourceGoalsFilePathNeverWiresTheCache is TestPinnedGoalsBypass
// (see internal/reposcan/goal_cache_test.go's doc comment): --goals returns
// fileGoalSource before the caching wiring is ever reached, so a store being
// present must change nothing about that path.
func TestResolveGoalSourceGoalsFilePathNeverWiresTheCache(t *testing.T) {
	root := t.TempDir()
	goals := filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"a.go": "hand written"}`)
	store := newGoalLedgerCache(filepath.Join(t.TempDir(), "scans.duckdb"))

	gs, disclosure, code := resolveGoalSource(&bytes.Buffer{}, root, goals, "test-model-x", false, 1,
		func(string) (reposcan.Deriver, error) {
			t.Fatal("the --goals path must never construct a deriver")
			return nil, nil
		}, store, false)
	if code != 0 || gs == nil {
		t.Fatalf("code=%d gs=%v", code, gs)
	}
	if disclosure != "" {
		t.Errorf("the --goals path must disclose nothing, got %q", disclosure)
	}
	if _, wired := gs.(*reposcan.CachingGoalSource); wired {
		t.Error("the --goals path must never be wrapped in a CachingGoalSource")
	}
}

// TestGoalReusedRoundTripsTheLedger proves the whole disclosure chain from
// a Job's Goal to a re-read scan_files row: buildScanFileRows must carry
// GoalReused onto scanstore.File, and Record/FilesForScan must round-trip it
// through the ledger unchanged.
func TestGoalReusedRoundTripsTheLedger(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package p\n\nfunc F() int { return 1 }\n")

	results := []reposcan.FileResult{{
		Job: reposcan.Job{
			Path: "a.go", Lang: "go",
			Goal:       reposcan.Goal{Text: "F never returns a negative number", Provenance: "derived:test-model-x@v1 (reused)"},
			GoalReused: true,
		},
		Gradable: true,
		Verdict:  advpool.Verdict{DevKillRate: 1, MutantsTotal: 1},
	}}
	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", dir, io.Discard)
	if len(files) != 1 {
		t.Fatalf("buildScanFileRows returned %d row(s), want 1", len(files))
	}
	if files[0].GoalReused == nil || !*files[0].GoalReused {
		t.Fatalf("buildScanFileRows dropped GoalReused: %+v", files[0])
	}

	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	scanID, err := st.Record(context.Background(), scanstore.Scan{Owner: "o", Repo: "r", Commit: "c"}, files)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	back, err := st.FilesForScan(context.Background(), scanID)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(back) != 1 || back[0].GoalReused == nil || !*back[0].GoalReused {
		t.Fatalf("goal_reused did not round-trip through the ledger: %+v", back)
	}
}

// TestGoalReusedFalseNeverFabricated: a file whose goal was NOT reused must
// read back NULL, never a stored false — the same NULL-not-false rule
// ChallengerSufficient follows.
func TestGoalReusedFalseNeverFabricated(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package p\n")
	results := []reposcan.FileResult{{
		Job:      reposcan.Job{Path: "a.go", Lang: "go", Goal: reposcan.Goal{Text: "g", Provenance: "derived:m@v1"}},
		Gradable: true,
		Verdict:  advpool.Verdict{DevKillRate: 1, MutantsTotal: 1},
	}}
	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", dir, io.Discard)
	if files[0].GoalReused != nil {
		t.Errorf("a file whose goal was not reused must carry a nil GoalReused, got %v", *files[0].GoalReused)
	}
}

// TestAttestationCarriesGoalReusedOnlyWhenTrue: the attestation's goalReused
// key is present (and true) only for a file whose goal was actually reused,
// and absent (never a signed false) otherwise.
func TestAttestationCarriesGoalReusedOnlyWhenTrue(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "statement.json")
	rep := reposcan.RepoReport{
		Owner: "o", Repo: "r", Commit: "abc123", Audited: 2, Candidates: 2,
		Weakest: []reposcan.WeakFile{
			{Path: "reused.go", KillRate: 0.5, Survivors: 1, GoalReused: true},
			{Path: "fresh.go", KillRate: 0.5, Survivors: 1, GoalReused: false},
		},
	}
	if _, err := writeAuditStatement(out, dir, rep, map[string]string{"test-writer": "w"}, nil, nil, true, 0, auditpush.Bundle{}); err != nil {
		t.Fatalf("writeAuditStatement: %v", err)
	}
	b, err := os.ReadFile(out) // #nosec G304 -- test-local path
	if err != nil {
		t.Fatalf("read statement: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decode statement: %v", err)
	}
	files, _ := decoded["predicate"].(map[string]any)["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("statement carries %d file(s), want 2", len(files))
	}
	var reused, fresh map[string]any
	for _, raw := range files {
		f := raw.(map[string]any)
		switch f["path"] {
		case "reused.go":
			reused = f
		case "fresh.go":
			fresh = f
		}
	}
	if v, ok := reused["goalReused"]; !ok || v != true {
		t.Errorf("reused.go missing goalReused:true: %+v\nfull statement:\n%s", reused, b)
	}
	if _, ok := fresh["goalReused"]; ok {
		t.Errorf("fresh.go must not sign a goalReused key at all (never a signed false): %+v\nfull statement:\n%s", fresh, b)
	}
}
