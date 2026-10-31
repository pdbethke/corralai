// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

func walkInventoryFixture() ([]reposcan.Candidate, []reposcan.Exclusion) {
	cands := []reposcan.Candidate{
		{Path: "a.go", TestPath: "a_test.go", Lang: "go"},
		{Path: "b.go", TestPath: "b_test.go", Lang: "go"},
		{Path: "c.py", TestPath: "test_c.py", Lang: "python"},
	}
	excl := []reposcan.Exclusion{
		{Path: "d.go", Reason: reposcan.ReasonNoPairedTest},
		{Path: "e.go", Reason: reposcan.ReasonNoPairedTest},
		{Path: "a_test.go", Reason: reposcan.ReasonIsTest},
		{Path: "f.py", Reason: reposcan.ReasonAmbiguousTest},
		{Path: "g.rs", Reason: reposcan.ReasonTestLandingNotBuilt},
		{Path: "h.rs", Reason: reposcan.ReasonTestLandingNotBuilt},
		{Path: "README.md", Reason: reposcan.ReasonNoLanguage},
		{Path: "i.go", Reason: reposcan.ReasonNotSelected},
	}
	return cands, excl
}

func decodeInventory(t *testing.T, text string) recordedInventory {
	t.Helper()
	var got recordedInventory
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("Inventory %q: %v", text, err)
	}
	return got
}

// The inventory on the record is the one the screen prints: every number the
// walk, exclusion and language lines show reaches the ledger bundle, and each
// is found on the screen as the record spells it.
func TestTheInventoryReachesTheLedgerAndMatchesTheScreen(t *testing.T) {
	cands, excl := walkInventoryFixture()
	inv := newRecordedInventory(cands, excl)
	inv.withUnsized(true, reposcan.RankInfo{Signal: "churn-x-size", Unsized: 2})
	cm := reposcan.CoverageMap{Ran: true, Executed: map[string]bool{"a.go": true, "b.go": false}}
	sources := []string{"a.go", "b.go", "c.py"}
	inv.withPreflight(cm, sources)

	var screen bytes.Buffer
	printLanguageProfile(&screen, reposcan.BuildLanguageProfile(cands, excl))
	printExclusions(&screen, excl)
	printPreflightReport(&screen, cm, sources)
	note := noEvidenceSourcesNote(excl)

	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c", Substrate: substrateWorkspace, Inventory: inv.text()},
		1, nil, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
	got := decodeInventory(t, b.Scan.Inventory)

	if got.Excluded != len(excl) {
		t.Errorf("excluded = %d, want the walk line's %d", got.Excluded, len(excl))
	}
	s := screen.String()
	for reason, n := range got.ExcludedByReason {
		if want := fmt.Sprintf("    %d %s\n", n, reason); !strings.Contains(s, want) {
			t.Errorf("the screen lacks %q, so it and the record disagree:\n%s", want, s)
		}
	}
	if len(got.ExcludedByReason) != 6 || got.ExcludedByReason[reposcan.ReasonNoPairedTest] != 2 {
		t.Errorf("excluded_by_reason = %v", got.ExcludedByReason)
	}
	for _, l := range got.Languages {
		if want := fmt.Sprintf("%d auditable, %d with no paired test", l.Auditable, l.NoPairedTest); !strings.Contains(s, want) {
			t.Errorf("the screen lacks %q for %s", want, l.Lang)
		}
	}
	rs := got.LandingNotBuilt["rust"]
	if rs != 2 || !strings.Contains(note, fmt.Sprintf("%d rust source(s) test landing not built", rs)) {
		t.Errorf("landing_not_built = %v, the selection note says %q", got.LandingNotBuilt, note)
	}
	if got.Preflight == nil || got.Preflight.Executed != 1 || got.Preflight.NotMeasured != 1 || got.Preflight.Unexercised != 1 {
		t.Fatalf("preflight = %+v", got.Preflight)
	}
	for _, want := range []string{
		fmt.Sprintf("%d file(s) executed at least once", got.Preflight.Executed),
		fmt.Sprintf("%d file(s) not measured by this run", got.Preflight.NotMeasured),
		fmt.Sprintf("%d file(s) measured and NEVER executed", got.Preflight.Unexercised),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the screen lacks %q", want)
		}
	}
	if got.UnsizedCands == nil || *got.UnsizedCands != 2 {
		t.Errorf("unsized_candidates = %v, want 2", got.UnsizedCands)
	}
}

// What the walk did not measure is absent: no ranking means no unsized count,
// a pre-flight that could not run holds no executed count, and a scan that
// walked nothing records no inventory at all.
func TestTheInventoryOmitsWhatNothingMeasured(t *testing.T) {
	cands, excl := walkInventoryFixture()
	inv := newRecordedInventory(cands, excl)
	inv.withUnsized(false, reposcan.RankInfo{})
	inv.withPreflight(reposcan.CoverageMap{Ran: false, Note: "no coverage tool"}, []string{"a.go"})
	var m map[string]any
	if err := json.Unmarshal([]byte(inv.text()), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"unsized_candidates", "preflight"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s is recorded for a scan that measured none: %v", k, m)
		}
	}
	// A ranking that sized everything IS a measured zero.
	inv.withUnsized(true, reposcan.RankInfo{Signal: "size-only"})
	if got := decodeInventory(t, inv.text()); got.UnsizedCands == nil || *got.UnsizedCands != 0 {
		t.Errorf("a ranking that sized every candidate recorded %v, want a measured 0", got.UnsizedCands)
	}
	if (*recordedInventory)(nil).text() != "" {
		t.Error("a scan that walked nothing recorded an inventory")
	}
}

// Rank's own count is the one the record holds: a candidate that cannot be
// stat'ed is ranked last and counted.
func TestRankCountsTheCandidatesItCouldNotSize(t *testing.T) {
	root := t.TempDir()
	cands := []reposcan.Candidate{{Path: "gone1.go"}, {Path: "gone2.go"}}
	_, info := reposcan.Rank(root, cands)
	if info.Unsized != 2 || !strings.Contains(info.Note, "2 candidate(s) could not be sized") {
		t.Errorf("RankInfo = %+v", info)
	}
}

// The shadow draw reaches the ledger with the seed spelled as --shadow-seed
// accepts it, and the printed line carries the same seed, pool size and
// parameters.
func TestTheShadowDrawReachesTheLedgerAndReplays(t *testing.T) {
	f := flagsWith("", "localmodel-a:1,localmodel-b:1", "", "", "")
	var errb bytes.Buffer
	sels, err := drawShadowSeats("corral certify --local", t.TempDir(), "go", f, nil, filepath.Join(t.TempDir(), "none.duckdb"), &errb)
	if err != nil {
		t.Fatal(err)
	}
	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c", ShadowDraw: shadowDrawJSON(sels)},
		1, nil, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
	var got []shadowpool.Selection
	if err := json.Unmarshal([]byte(b.Scan.ShadowDraw), &got); err != nil || len(got) != 1 {
		t.Fatalf("ShadowDraw %q: %v", b.Scan.ShadowDraw, err)
	}
	g := got[0]
	// A drawn (not given) seed replays from the record: it parses back to
	// the exact seed the draw used, and a draw from it chooses the same member.
	seed, err := shadowpool.ParseSeed(g.Seed)
	if err != nil {
		t.Fatalf("the recorded seed %q is not one --shadow-seed accepts: %v", g.Seed, err)
	}
	cands := []shadowpool.Candidate{}
	for _, m := range g.Members {
		cands = append(cands, shadowpool.Candidate{Typed: m.Model, Concrete: m.Concrete})
	}
	if again := shadowpool.Draw(g.Role, g.Lang, cands, nil, false, 0, seed); again.Chosen != g.Chosen {
		t.Errorf("replaying seed %s chose %q, the record says %q", g.Seed, again.Chosen, g.Chosen)
	}
	screen := errb.String()
	for _, want := range []string{
		fmt.Sprintf("drawn from a pool of %d", len(g.Members)),
		"seed " + g.Seed,
		describeDraw(g),
		describeHistory(g),
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen lacks %q, so it and the record disagree:\n%s", want, screen)
		}
	}
}

// No draw, no record: a run with named seats carries no ShadowDraw, and a
// run with no token cap carries no MaxTokensReached.
func TestNoDrawAndNoCapRecordNothing(t *testing.T) {
	if got := shadowDrawJSON(nil); got != "" {
		t.Errorf("no draw recorded %q", got)
	}
	if got := tokenCapReached(nil); got != nil {
		t.Errorf("no cap recorded %v", *got)
	}
	if got := tokenCapReached(scanTokenBudget(nil)); got != nil {
		t.Errorf("no executor recorded %v", *got)
	}
}

// Whether the cap was reached is the one budgetLine prints.
func TestTheTokenCapOutcomeMatchesTheCapLine(t *testing.T) {
	b := agentbackend.NewTokenBudget(1000)
	got := tokenCapReached(b)
	if got == nil || *got || !strings.Contains(budgetLine(b, 0), "cap: --max-tokens") || strings.Contains(budgetLine(b, 0), "REACHED") {
		t.Fatalf("under the cap: recorded %v, line %q", got, budgetLine(b, 0))
	}
	if !b.Allow() {
		t.Fatal("an empty budget refused a call")
	}
	b.Charge(agentbackend.Usage{InputTokens: 2000})
	if b.Allow() {
		t.Fatal("a spent budget admitted a call")
	}
	got = tokenCapReached(b)
	if got == nil || !*got || !strings.Contains(budgetLine(b, 0), "REACHED") {
		t.Fatalf("over the cap: recorded %v, line %q", got, budgetLine(b, 0))
	}
}

// The per-file counts reach the row from the verdict the report prints them
// from, and a verdict that timed out after scoring carries them as well.
func TestTheMutantAndWriterCountsReachTheRowAndMatchTheScreen(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		v := advpool.Verdict{
			Lang: "go", DevScored: true, TimedOut: timedOut, MutantsTotal: 6, Survivors: 2, MutantsDistinct: 6,
			MutantsUnmeasured: 3, DuplicateMutants: 4,
			WriterMode:          advpool.WriterModePerSurvivor,
			WriterSeatsUngraded: 1, WriterAttempts: &advpool.TestsPerMutantSpread{Min: 1, Median: 2, Max: 3},
			ExamCoverage: advpool.ExamCoverage{Symbols: 4, SymbolsProbed: 2, Decisions: 3, DecisionsProbed: 1, Unplaced: 2, Measured: true},
		}
		res := reposcan.FileResult{Job: reposcan.Job{Path: "pkg/a.go", Lang: "go"}, Gradable: true, Verdict: v}
		files := buildScanFileRows([]reposcan.FileResult{res}, nil, reposcan.CoverageMap{}, "", "", io.Discard)
		b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c"}, 1, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
		r := b.Files[0]
		if r.MutantsUnplaced == nil || *r.MutantsUnplaced != 2 || r.MutantsUnmeasured == nil || *r.MutantsUnmeasured != 3 ||
			r.MutantsDuplicate == nil || *r.MutantsDuplicate != 4 {
			t.Fatalf("timedOut=%v: row counts = %v %v %v", timedOut, r.MutantsUnplaced, r.MutantsUnmeasured, r.MutantsDuplicate)
		}
		var wa recordedWriterAttempts
		if err := json.Unmarshal([]byte(r.WriterAttempts), &wa); err != nil || wa.Min == nil || *wa.Min != 1 || *wa.Median != 2 || *wa.Max != 3 || wa.SeatsUngraded != 1 {
			t.Fatalf("timedOut=%v: WriterAttempts %q: %v", timedOut, r.WriterAttempts, err)
		}

		// The screen: the --repo report's exam line and writer line, and the
		// adversarial verdict's unmeasured and duplicate lines.
		rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{res}, nil)
		weak := rep.Weakest[0]
		exam := examConfidenceLine(weak)
		writer := writerModeDisclosure(weak.WriterMode, 5, weak.WriterSeatsUngraded, weak.WriterAttempts)
		wire, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var av advVerdict
		if err := json.Unmarshal(wire, &av); err != nil {
			t.Fatal(err)
		}
		var adv bytes.Buffer
		renderAdvVerdict(&adv, "x.go", av)
		for _, want := range []string{
			fmt.Sprintf("%d mutant(s) unplaced", *r.MutantsUnplaced),
			fmt.Sprintf("%d seats ungraded", wa.SeatsUngraded),
			fmt.Sprintf("%d to %d attempts per survivor, median %d", *wa.Min, *wa.Max, *wa.Median),
			fmt.Sprintf("%d mutant(s) were not graded", *r.MutantsUnmeasured),
			fmt.Sprintf("%d duplicate mutant(s) collapsed", *r.MutantsDuplicate),
		} {
			if !strings.Contains(exam+"\n"+writer+"\n"+adv.String(), want) {
				t.Errorf("timedOut=%v: the screen lacks %q\nexam: %s\nwriter: %s\nadversarial:\n%s", timedOut, want, exam, writer, adv.String())
			}
		}
	}
}

// A verdict that never scored the exam records no count of its mutants, not
// a zero that reads as "none were unmeasured"; a batched writer records no
// attempts.
func TestAnUnscoredVerdictRecordsNoMutantCounts(t *testing.T) {
	v := advpool.Verdict{Lang: "go", TimedOut: true, WriterMode: advpool.WriterModeBatched}
	res := reposcan.FileResult{Job: reposcan.Job{Path: "pkg/a.go", Lang: "go"}, Gradable: true, Verdict: v}
	r := buildScanFileRows([]reposcan.FileResult{res}, nil, reposcan.CoverageMap{}, "", "", io.Discard)[0]
	if r.MutantsUnplaced != nil || r.MutantsUnmeasured != nil || r.MutantsDuplicate != nil || r.WriterAttempts != "" {
		t.Errorf("recorded %v %v %v %q for a verdict that scored nothing", r.MutantsUnplaced, r.MutantsUnmeasured, r.MutantsDuplicate, r.WriterAttempts)
	}
}
