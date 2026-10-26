// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/ledger"
)

func fp(v float64) *float64 { return &v }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// graded is an audited file with a measured rate: graded mutants, survivors
// among them, and KillRate as the pool recorded it, (graded-survivors)/graded.
func graded(path string, gradedN, invalid, survivors, proven int) ledger.Row {
	return ledger.Row{Repo: "o/r", Commit: "c1", Path: path, Disposition: "audited",
		KillRate:      fp(float64(gradedN-survivors) / float64(gradedN)),
		MutantsGraded: gradedN, MutantsInvalid: invalid, MutantsPlanted: gradedN + invalid,
		Survivors: survivors, ProvenMissed: proven}
}

func runOf(uid, commit string, finished *time.Time, files ...ledger.Row) ledger.Bundle {
	return ledger.Bundle{Scan: ledger.ScanRow{ScanUID: uid, Repo: "o/r", Commit: commit, FinishedAt: finished}, Files: files}
}

func at(h int) *time.Time {
	t := time.Date(2026, 10, 1, h, 0, 0, 0, time.UTC)
	return &t
}

func summaryJSON(t *testing.T, s ledger.Summary) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Two scans, three audited files, one of them with no rate (uncovered), plus
// a rejected file. The share pools the two graded files only; the ungraded
// one is counted separately and never read as 0%; the rejected one was
// never audited and is in neither.
func TestSummaryTotals(t *testing.T) {
	uncovered := ledger.Row{Repo: "o/r", Commit: "c1", Path: "u.go", Disposition: "audited", Uncovered: true, MutantsPlanted: 4}
	rejected := ledger.Row{Repo: "o/r", Commit: "c1", Path: "r.go", Disposition: "rejected", Reason: "no test"}
	s := ledger.Summarize([]ledger.Bundle{
		runOf("a", "c1", at(1), graded("a.go", 4, 1, 1, 1), uncovered, rejected),
		runOf("b", "c2", at(2), graded("b.go", 6, 0, 3, 2)),
	})
	tt := s.Totals
	if tt.Scans != 2 || tt.FilesAudited != 3 || tt.CouldNotGrade != 1 {
		t.Fatalf("scans/audited/could-not-grade = %d/%d/%d", tt.Scans, tt.FilesAudited, tt.CouldNotGrade)
	}
	// Mutant figures are over the graded files: the same set the share is over.
	if tt.MutantsPlanted != 11 || tt.Survivors != 4 || tt.ProvenMissed != 3 {
		t.Fatalf("planted/survivors/proven = %d/%d/%d", tt.MutantsPlanted, tt.Survivors, tt.ProvenMissed)
	}
	// (4-1 + 6-3) / (4 + 6)
	if tt.CaughtShare == nil || !near(*tt.CaughtShare, 0.6) {
		t.Fatalf("caught share %v, want 0.6", tt.CaughtShare)
	}
	if tt.LastRun == nil || !tt.LastRun.Equal(*at(2)) {
		t.Fatalf("last run %v", tt.LastRun)
	}
	if len(s.Runs) != 2 || s.Runs[0].ScanUID != "a" || s.Runs[0].FilesAudited != 2 || s.Runs[0].CaughtShare == nil || !near(*s.Runs[0].CaughtShare, 0.75) {
		t.Fatalf("runs %+v", s.Runs)
	}
}

// The pooled share uses the numerator and denominator of the recorded
// KillRate: graded mutants, compile-gate rejects excluded. The values are a
// real ledger row (flag_mutex.go: 2 graded, 16 invalid, 1 survivor, rate
// 0.5), where (planted-survivors)/planted would say 17/18.
func TestSummaryShareMatchesKillRate(t *testing.T) {
	f := ledger.Row{Repo: "o/r", Commit: "c1", Path: "flag_mutex.go", Disposition: "audited",
		KillRate: fp(0.5), MutantsGraded: 2, MutantsInvalid: 16, MutantsPlanted: 18, Survivors: 1, ProvenMissed: 1}
	s := ledger.Summarize([]ledger.Bundle{runOf("a", "c1", at(1), f)})
	if s.Totals.CaughtShare == nil || *s.Totals.CaughtShare != *f.KillRate {
		t.Fatalf("caught share %v, want the recorded KillRate %v", s.Totals.CaughtShare, *f.KillRate)
	}
	if s.Runs[0].CaughtShare == nil || *s.Runs[0].CaughtShare != *f.KillRate {
		t.Fatalf("run share %v", s.Runs[0].CaughtShare)
	}
}

// Review Focus 4: scans that predate telemetry say so (null), never 0.
func TestSummaryNoTelemetry(t *testing.T) {
	s := ledger.Summarize([]ledger.Bundle{runOf("a", "c1", at(1), graded("a.go", 4, 0, 1, 0))})
	if s.Totals.Telemetry != nil {
		t.Fatalf("telemetry %+v from a scan with no Calls rows", s.Totals.Telemetry)
	}
	if j := summaryJSON(t, s); !strings.Contains(j, `"telemetry":null`) {
		t.Fatalf("want \"telemetry\":null in %s", j)
	}

	// With one scan metered and one not, the figures say how many scans they
	// cover rather than averaging the silent one in as zero.
	metered := runOf("b", "c1", at(2))
	metered.Calls = []ledger.ModelCallRow{
		{Role: "mutant-generator", Model: "m1", Calls: 3, InputTokens: 100, OutputTokens: 10},
		{Role: "test-critic", Model: "agy", Calls: 2, UnmeteredCalls: 2},
	}
	s = ledger.Summarize([]ledger.Bundle{runOf("a", "c1", at(1)), metered})
	want := &ledger.Telemetry{ScansWithTelemetry: 1, ModelCalls: 5, InputTokens: 100, OutputTokens: 10, UnmeteredCalls: 2}
	if !reflect.DeepEqual(s.Totals.Telemetry, want) {
		t.Fatalf("telemetry %+v, want %+v", s.Totals.Telemetry, want)
	}
}

// Review Focus 5: nothing graded is no measurement, not 0%.
func TestSummaryNothingGraded(t *testing.T) {
	timedOut := graded("t.go", 4, 0, 1, 1)
	timedOut.TimedOut = true
	noDenominator := ledger.Row{Repo: "o/r", Commit: "c1", Path: "z.go", Disposition: "audited", KillRate: fp(0), MutantsInvalid: 5, MutantsPlanted: 5}
	s := ledger.Summarize([]ledger.Bundle{runOf("a", "c1", nil,
		ledger.Row{Repo: "o/r", Commit: "c1", Path: "u.go", Disposition: "audited", Uncovered: true},
		timedOut, noDenominator,
	)})
	if s.Totals.CaughtShare != nil || s.Runs[0].CaughtShare != nil {
		t.Fatalf("share %v / run share %v from nothing graded", s.Totals.CaughtShare, s.Runs[0].CaughtShare)
	}
	if s.Totals.CouldNotGrade != 3 || s.Totals.MutantsPlanted != 0 || s.Totals.ProvenMissed != 0 {
		t.Fatalf("totals %+v", s.Totals)
	}
	if s.Totals.LastRun != nil || s.Runs[0].FinishedAt != nil {
		t.Fatalf("a scan with no FinishedAt gave a time: %+v", s.Totals.LastRun)
	}
	j := summaryJSON(t, s)
	for _, k := range []string{`"caught_share":null`, `"last_run":null`, `"finished_at":null`} {
		if !strings.Contains(j, k) {
			t.Fatalf("want %s in %s", k, j)
		}
	}
	if len(s.Gaps) != 0 {
		t.Fatalf("gaps from an ungraded file: %+v", s.Gaps)
	}
}

func TestSummaryGaps(t *testing.T) {
	withTest := graded("a.go", 4, 0, 2, 2)
	withTest.AuthoredTest = "func TestA(t *testing.T) {}"
	pushed := runOf("a", "c1", at(1), withTest, graded("b.go", 4, 0, 1, 1))
	pushed.SourcePushed = true
	pushed.Mutants = []ledger.MutantRow{
		{Repo: "o/r", Path: "a.go", MutantID: "m1", Outcome: "survived", Proven: true},
		{Repo: "o/r", Path: "a.go", MutantID: "m2", Outcome: "survived", Proven: true},
		{Repo: "o/r", Path: "a.go", MutantID: "m3", Outcome: "killed"},
		{Repo: "o/r", Path: "b.go", MutantID: "m4", Outcome: "survived", Proven: true},
	}
	// A run that did not push source serves no source, whatever its row holds.
	held := graded("a.go", 4, 0, 1, 1)
	held.AuthoredTest = "must not be served"
	unpushed := runOf("b", "c2", at(2), held)
	unpushed.Mutants = []ledger.MutantRow{{Repo: "o/r", Path: "a.go", MutantID: "m5", Outcome: "survived", Proven: true}}

	s := ledger.Summarize([]ledger.Bundle{pushed, unpushed})
	want := []ledger.Gap{
		{ScanUID: "a", Repo: "o/r", Commit: "c1", Path: "a.go", MutantID: "m1", Outcome: "survived", AuthoredTest: withTest.AuthoredTest, SourcePushed: true},
		{ScanUID: "a", Repo: "o/r", Commit: "c1", Path: "a.go", MutantID: "m2", Outcome: "survived", AuthoredTest: withTest.AuthoredTest, SourcePushed: true},
		{ScanUID: "a", Repo: "o/r", Commit: "c1", Path: "b.go", MutantID: "m4", Outcome: "survived", SourcePushed: true},
		{ScanUID: "b", Repo: "o/r", Commit: "c2", Path: "a.go", MutantID: "m5", Outcome: "survived"},
	}
	if !reflect.DeepEqual(s.Gaps, want) {
		t.Fatalf("gaps\n got %+v\nwant %+v", s.Gaps, want)
	}
}

func withModel(r ledger.Row, mutant, tool string) ledger.Row {
	r.ModelsByRole = `{"mutant-generator":"` + mutant + `","test-writer":"w"}`
	if tool != "" {
		r.SeatTools = `{"mutant-generator":"` + tool + `"}`
	}
	return r
}

// Only a file graded under more than one mutant model is a like-for-like
// comparison, and a seat that named a tool rather than a model is no
// evidence about a model (seatrecord.RecordedSeatNamesAModel).
func TestSummaryModelsLikeForLike(t *testing.T) {
	s := ledger.Summarize([]ledger.Bundle{
		runOf("1", "c1", at(1),
			withModel(graded("x.go", 10, 0, 2, 2), "m1", ""),
			withModel(graded("y.go", 10, 0, 5, 5), "m1", ""), // m1 only: not a comparison
			withModel(graded("z.go", 10, 0, 1, 1), "m2", ""),
		),
		runOf("2", "c1", at(2),
			withModel(graded("x.go", 10, 2, 4, 3), "m2", ""),
			withModel(graded("z.go", 10, 0, 3, 3), "claude-code", "2.1.309 (Claude Code)"), // a tool, not a model
		),
	})
	want := []ledger.ModelLine{
		{Role: "mutant-generator", Model: "m1", Files: 1, MutantsPlanted: 10, Survivors: 2, ProvenMissed: 2, CaughtShare: fp(0.8)},
		{Role: "mutant-generator", Model: "m2", Files: 1, MutantsPlanted: 12, Survivors: 4, ProvenMissed: 3, CaughtShare: fp(0.6)},
	}
	if !reflect.DeepEqual(s.Models, want) {
		t.Fatalf("models\n got %s\nwant %s", summaryJSON(t, ledger.Summary{Models: s.Models}), summaryJSON(t, ledger.Summary{Models: want}))
	}
}

// Runs are ordered by FinishedAt then ScanUID; a run with no FinishedAt sorts
// after every timed one rather than at an invented time.
func TestSummaryRunsOrder(t *testing.T) {
	s := ledger.Summarize([]ledger.Bundle{runOf("z", "c", nil), runOf("b", "c", at(2)), runOf("a", "c", at(2)), runOf("c", "c", at(1)), runOf("y", "c", nil)})
	var got []string
	for _, r := range s.Runs {
		got = append(got, r.ScanUID)
	}
	if !reflect.DeepEqual(got, []string{"c", "a", "b", "y", "z"}) {
		t.Fatalf("order %v", got)
	}
}

func TestSummaryEmptyIsNeverNullSlices(t *testing.T) {
	j := summaryJSON(t, ledger.Summarize(nil))
	for _, k := range []string{`"runs":[]`, `"gaps":[]`, `"models":[]`} {
		if !strings.Contains(j, k) {
			t.Fatalf("want %s in %s", k, j)
		}
	}
}

// Derive fills Summary from the rows it serves, and from nothing else.
func TestDeriveFillsSummary(t *testing.T) {
	dir, pub := mixedLedger(t)
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary.Totals.Scans != len(d.Scans) || len(d.Summary.Runs) != 2 {
		t.Fatalf("summary scans %d / runs %d, want the %d served", d.Summary.Totals.Scans, len(d.Summary.Runs), len(d.Scans))
	}
	served := map[string]bool{}
	for _, s := range d.Scans {
		served[s.ScanUID] = true
	}
	for _, r := range d.Summary.Runs {
		if !served[r.ScanUID] {
			t.Fatalf("run %q is not a served scan", r.ScanUID)
		}
		delete(served, r.ScanUID)
	}
}

func TestDeriveSummaryEmptyOnProblem(t *testing.T) {
	dir, pub := mixedLedger(t)
	es := readBytes(t, dir)
	tamperFile(t, dir, es[2].File, `"Audited": 1`, `"Audited": 9`)
	d, err := ledger.Derive(rawDir(t, dir), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if d.Problem == "" {
		t.Fatal("setup: no problem")
	}
	if !reflect.DeepEqual(d.Summary, ledger.Summarize(nil)) || d.Summary.Totals.Scans != 0 {
		t.Fatalf("summary served from a broken ledger: %+v", d.Summary)
	}
	j := summaryJSON(t, d.Summary)
	for _, k := range []string{`"runs":[]`, `"gaps":[]`, `"models":[]`, `"caught_share":null`} {
		if !strings.Contains(j, k) {
			t.Fatalf("want %s in %s", k, j)
		}
	}
}
