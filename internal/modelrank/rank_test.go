// SPDX-License-Identifier: Elastic-2.0

package modelrank_test

import (
	"testing"

	"github.com/pdbethke/corralai/internal/modelrank"
)

func writerObs(model, lang string, runs int, catches, opps int) []modelrank.Observation {
	var out []modelrank.Observation
	for i := 0; i < runs; i++ {
		out = append(out, modelrank.Observation{
			Model: model, Role: "test-writer", Lang: lang,
			Run:           model + lang + string(rune('a'+i)),
			Catches:       catches,
			Opportunities: opps,
		})
	}
	return out
}

func find(t *testing.T, r modelrank.Report, seat, lang, model string) modelrank.Row {
	t.Helper()
	for _, g := range r.Groups {
		if g.Seat != seat || g.Lang != lang {
			continue
		}
		for _, row := range g.Rows {
			if row.Model == model {
				return row
			}
		}
	}
	t.Fatalf("no row for seat %q lang %q model %q in %+v", seat, lang, model, r.Groups)
	return modelrank.Row{}
}

func group(t *testing.T, r modelrank.Report, seat, lang string) modelrank.Group {
	t.Helper()
	for _, g := range r.Groups {
		if g.Seat == seat && g.Lang == lang {
			return g
		}
	}
	t.Fatalf("no group for seat %q lang %q", seat, lang)
	return modelrank.Group{}
}

// The writer's metric is proven gaps per survivor ATTEMPTED — nothing else.
func TestWriterMetricIsProvenGapsPerSurvivor(t *testing.T) {
	obs := writerObs("m-good", "", 6, 8, 10) // 48/60
	obs = append(obs, writerObs("m-weak", "", 6, 1, 10)...)
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5})
	good := find(t, r, "test-writer", "", "m-good")
	if good.Metric == nil || *good.Metric < 0.79 || *good.Metric > 0.81 {
		t.Fatalf("want 0.80, got %v", good.Metric)
	}
	// n is the metric's own denominator: 6 runs x 10 survivors attempted.
	if good.N != 60 || good.NUnit != "survivors attempted" {
		t.Fatalf("want n=60 survivors attempted, got %d %q", good.N, good.NUnit)
	}
	g := group(t, r, "test-writer", "")
	if g.Rows[0].Model != "m-good" {
		t.Fatalf("want the better writer ranked first, got %q", g.Rows[0].Model)
	}
	if g.Prefer != "m-good" {
		t.Fatalf("prefer = %q, want m-good", g.Prefer)
	}
}

// A generator whose faults are all trivially killed is a weak generator: the
// metric is valid, graded mutants the dev suite FAILED to kill, per run.
func TestGeneratorMetricCountsOnlyValidUnkilledMutants(t *testing.T) {
	var obs []modelrank.Observation
	for i := 0; i < 5; i++ {
		obs = append(obs,
			// prolific, but everything it plants is trivially killed
			modelrank.Observation{Model: "loud", Role: "mutant-generator", Run: "loud" + string(rune('a'+i)),
				MutantsPlanted: 100, MutantsGraded: 100, MutantsSurvived: 2},
			// fewer mutants, far more of them survive the dev suite
			modelrank.Observation{Model: "sharp", Role: "mutant-generator", Run: "sharp" + string(rune('a'+i)),
				MutantsPlanted: 25, MutantsGraded: 20, MutantsInvalid: 5, MutantsSurvived: 10},
		)
	}
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5})
	g := group(t, r, "mutant-generator", "")
	if g.Rows[0].Model != "sharp" {
		t.Fatalf("want sharp first (10 survivors/run vs 2), got %q", g.Rows[0].Model)
	}
	sharp := find(t, r, "mutant-generator", "", "sharp")
	if sharp.Metric == nil || *sharp.Metric != 10 {
		t.Fatalf("want 10 unkilled valid mutants per run, got %v", sharp.Metric)
	}
	if sharp.Valid == nil || *sharp.Valid < 0.79 || *sharp.Valid > 0.81 {
		t.Fatalf("want a 80%% valid share reported alongside, got %v", sharp.Valid)
	}
}

// The critic is scored against ADJUDICATION, and its n is adjudications —
// not pool runs, which a critic can rack up without a single verdict.
func TestCriticScoredAgainstAdjudicationAndCountsAdjudications(t *testing.T) {
	var obs []modelrank.Observation
	for i := 0; i < 20; i++ {
		obs = append(obs, modelrank.Observation{Model: "c1", Role: "test-critic", Run: "r" + string(rune('a'+i))})
	}
	obs = append(obs, modelrank.Observation{Model: "c1", Role: "test-critic", Run: "adj",
		CriticConfirmed: 3, CriticRefuted: 1})
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5})
	c := find(t, r, "test-critic", "", "c1")
	if c.Metric == nil || *c.Metric != 0.75 {
		t.Fatalf("want 0.75 precision, got %v", c.Metric)
	}
	if c.N != 4 || c.NUnit != "adjudications" {
		t.Fatalf("want n=4 adjudications, got %d %q", c.N, c.NUnit)
	}
	if c.Sufficient {
		t.Fatal("4 adjudications is below min-runs 5 — must not be sufficient")
	}
}

// goal-deriver has no direct signal. It is reported, never numbered.
func TestGoalDeriverIsNotScored(t *testing.T) {
	obs := []modelrank.Observation{{Model: "g1", Role: "goal-deriver", Run: "r1"}}
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5})
	g := group(t, r, "goal-deriver", "")
	if g.Prefer != "" {
		t.Fatalf("goal-deriver must never carry a prefer line, got %q", g.Prefer)
	}
	if g.Note == "" {
		t.Fatal("goal-deriver must say why it is not scored")
	}
	for _, row := range g.Rows {
		if row.Metric != nil {
			t.Fatalf("goal-deriver must invent no number, got %v", row.Metric)
		}
	}
}

// The live scorecard's `claude-sonnet-5 3/3 100%` row is exactly the failure
// this rule prevents: a perfect rate on three observations must never be
// promoted over a well-evidenced one.
func TestThinEvidenceIsPrintedButNeverPreferred(t *testing.T) {
	// The live scorecard's real row: 22 RUNS, but only 3 survivors ever
	// attempted. A floor counting runs would promote it; the floor counts the
	// attempts the rate is actually made of.
	obs := writerObs("claude-sonnet-5", "", 22, 0, 0)
	obs = append(obs, modelrank.Observation{Model: "claude-sonnet-5", Role: "test-writer",
		Run: "claude-sonnet-5x", Catches: 3, Opportunities: 3})
	obs = append(obs, modelrank.Observation{Model: "gemini-3.6-flash", Role: "test-writer",
		Run: "g0", Catches: 64, Opportunities: 79})
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5})
	thin := find(t, r, "test-writer", "", "claude-sonnet-5")
	if thin.Sufficient {
		t.Fatalf("3 attempts must not count as sufficient evidence (n=%d %s)", thin.N, thin.NUnit)
	}
	if thin.Metric == nil {
		t.Fatal("the row must still be printed with its real number")
	}
	g := group(t, r, "test-writer", "")
	if g.Prefer != "gemini-3.6-flash" {
		t.Fatalf("prefer = %q, want the well-evidenced model", g.Prefer)
	}
	if g.Rows[0].Model != "gemini-3.6-flash" {
		t.Fatalf("a thin 100%% row must not sort above a sufficient one, got %q", g.Rows[0].Model)
	}
}

func TestNoSufficientEvidenceMeansNoPreferLine(t *testing.T) {
	r := modelrank.Rank(writerObs("m", "", 2, 2, 2), modelrank.Options{MinRuns: 5, Seat: "test-writer"})
	g := group(t, r, "test-writer", "")
	if g.Prefer != "" {
		t.Fatalf("prefer = %q, want none", g.Prefer)
	}
	if g.Note == "" {
		t.Fatal("want a note saying nothing has enough evidence")
	}
}

// Registry mode ranks the operator's declared aliases; a model the evidence
// carries but the registry never declared is disclosed, not preferred.
func TestRegistryModeLabelsAliasesAndWithholdsUndeclared(t *testing.T) {
	obs := writerObs("gemini-3.6-flash", "", 10, 5, 10)
	obs = append(obs, writerObs("some-undeclared", "", 10, 9, 10)...)
	r := modelrank.Rank(obs, modelrank.Options{
		MinRuns:  5,
		Declared: map[string]string{"gemini-3.6-flash": "fast"},
		Source:   ".corral/models.json",
	})
	if r.Mode != modelrank.ModeRegistry {
		t.Fatalf("mode = %q, want registry", r.Mode)
	}
	fast := find(t, r, "test-writer", "", "gemini-3.6-flash")
	if fast.Alias != "fast" || !fast.Declared {
		t.Fatalf("want alias fast/declared, got %+v", fast)
	}
	und := find(t, r, "test-writer", "", "some-undeclared")
	if und.Declared {
		t.Fatal("some-undeclared must not read as declared")
	}
	g := group(t, r, "test-writer", "")
	if g.Prefer != "fast" {
		t.Fatalf("prefer = %q — an undeclared model must never be preferred over a declared one", g.Prefer)
	}
}

func TestNoRegistryRanksConcreteModelsAndSaysSo(t *testing.T) {
	r := modelrank.Rank(writerObs("m", "", 10, 5, 10), modelrank.Options{MinRuns: 5})
	if r.Mode != modelrank.ModeEvidence {
		t.Fatalf("mode = %q, want evidence", r.Mode)
	}
	row := find(t, r, "test-writer", "", "m")
	if row.Alias != "" {
		t.Fatalf("no registry means no alias, got %q", row.Alias)
	}
}

// A writer good at Python may be bad at Go — we have a real instance.
func TestLanguageSegmentation(t *testing.T) {
	obs := writerObs("m", "python", 6, 9, 10)
	obs = append(obs, writerObs("m", "go", 6, 1, 10)...)
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5})
	if !r.LangDimension {
		t.Fatal("evidence carries a language — the report must say so")
	}
	py := find(t, r, "test-writer", "python", "m")
	golang := find(t, r, "test-writer", "go", "m")
	if py.Metric == nil || golang.Metric == nil || *py.Metric <= *golang.Metric {
		t.Fatalf("segmentation lost: python=%v go=%v", py.Metric, golang.Metric)
	}

	only := modelrank.Rank(obs, modelrank.Options{MinRuns: 5, Lang: "go"})
	for _, g := range only.Groups {
		if g.Lang != "go" {
			t.Fatalf("--lang go leaked a %q group", g.Lang)
		}
	}
}

func TestSeatFilter(t *testing.T) {
	obs := writerObs("m", "", 6, 5, 10)
	obs = append(obs, modelrank.Observation{Model: "m", Role: "test-critic", Run: "x", CriticConfirmed: 9})
	r := modelrank.Rank(obs, modelrank.Options{MinRuns: 5, Seat: "test-writer"})
	for _, g := range r.Groups {
		if g.Seat != "test-writer" {
			t.Fatalf("--seat leaked group %q", g.Seat)
		}
	}
}

func TestNoLanguageDimensionIsDisclosed(t *testing.T) {
	r := modelrank.Rank(writerObs("m", "", 6, 5, 10), modelrank.Options{MinRuns: 5})
	if r.LangDimension {
		t.Fatal("this evidence carries no language — the report must not claim one")
	}
}
