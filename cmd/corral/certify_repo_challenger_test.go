// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/modelcorr"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// challengerFixtureRepo is one committed file, so buildScanFileRows can hash
// what it graded.
func challengerFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func challengerRow(t *testing.T, pair *modelcorr.Pair) (jaccard *float64, kappa *float64, sufficient *bool) {
	t.Helper()
	results := []reposcan.FileResult{{
		Job:      reposcan.Job{Path: "a.go", Lang: "go"},
		Gradable: true,
		Verdict: advpool.Verdict{
			Status:              "certified",
			ChallengerAgreement: pair,
		},
	}}
	rows := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", challengerFixtureRepo(t), os.Stderr)
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1", len(rows))
	}
	return rows[0].ChallengerJaccard, rows[0].ChallengerKappa, rows[0].ChallengerSufficient
}

// TestChallengerAgreementReachesTheLedger: nothing proved the mapping ever
// carried a REAL pair. Every fixture in the suite left ChallengerAgreement
// nil, so the three columns were only ever asserted to be NULL — a mapping
// that dropped the measurement entirely would have passed every one of them.
func TestChallengerAgreementReachesTheLedger(t *testing.T) {
	t.Run("a measured pair", func(t *testing.T) {
		j, k, s := challengerRow(t, &modelcorr.Pair{
			ModelA: "writer-a", ModelB: "writer-b",
			Mutants: 20, SurvivedA: 6, SurvivedB: 5, SharedSurvivors: 3, UnionSurvivors: 8,
			Jaccard: 0.375, Kappa: 0.42, KappaDefined: true, Sufficient: true,
		})
		if j == nil || *j != 0.375 {
			t.Errorf("ChallengerJaccard = %v, want 0.375 — the measurement the challenger seat exists to produce", j)
		}
		if k == nil || *k != 0.42 {
			t.Errorf("ChallengerKappa = %v, want 0.42", k)
		}
		if s == nil || !*s {
			t.Errorf("ChallengerSufficient = %v, want true", s)
		}
	})

	// MINOR 8. modelcorr.Pair.Sufficient's own doc: "Callers MUST check it
	// before reading Jaccard" — below MinSurvivorUnion the coefficient is
	// meaningless, and Compare zeroes it. Storing that 0.0 would file "the
	// union was too small to say" as "the two writers missed nothing in
	// common", which is the strongest possible claim in the opposite
	// direction.
	t.Run("an insufficient union stores no jaccard", func(t *testing.T) {
		j, _, s := challengerRow(t, &modelcorr.Pair{
			ModelA: "writer-a", ModelB: "writer-b",
			Mutants: 3, UnionSurvivors: 1, Jaccard: 0, Sufficient: false,
		})
		if j != nil {
			t.Errorf("ChallengerJaccard = %v, want NULL — Jaccard is meaningless unless Sufficient", *j)
		}
		// Sufficient itself is still recorded: "we compared and the union was
		// too small" is a fact worth keeping.
		if s == nil || *s {
			t.Errorf("ChallengerSufficient = %v, want a recorded false", s)
		}
	})

	t.Run("no challenger at all", func(t *testing.T) {
		j, k, s := challengerRow(t, nil)
		if j != nil || k != nil || s != nil {
			t.Errorf("with no challenger the three columns must all be NULL, got %v %v %v", j, k, s)
		}
	})
}
