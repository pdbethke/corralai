// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/pdbethke/corralai/internal/seatrecord"
)

// Summary is every figure a results page shows, computed here so the page
// draws numbers and never makes them: it does no sum, filter or choice of
// which entries count (a second implementation in JS would be a second
// opinion about the record). Derive builds it from the scans it serves and
// nothing else, so a figure can never come from an entry Derive refused.
//
// A value that was not measured is nil (JSON null), never 0: no telemetry,
// nothing graded and no finish time each read as what they are.
type Summary struct {
	Totals Totals `json:"totals"`
	// Runs is one point per served scan, ordered by FinishedAt then ScanUID.
	// A scan with no FinishedAt sorts after every timed one, by ScanUID,
	// rather than at an invented time.
	Runs []RunPoint `json:"runs"`
	// Gaps is one entry per proven mutant on a graded file, in ledger order.
	Gaps []Gap `json:"gaps"`
	// Models is the like-for-like comparison: see summarizeModels.
	Models []ModelLine `json:"models"`
}

// Totals pools every served scan.
//
// FilesAudited counts files with Disposition "audited"; a rejected file was
// never audited and is in no figure here. An audited file is GRADED when
// gradedFile says so, and only graded files contribute to MutantsPlanted,
// Survivors, ProvenMissed and CaughtShare, so the four describe one set of
// files and a reader can check one against another. Every other audited file
// is counted in CouldNotGrade and nowhere else: an uncovered file's withheld
// rate must not come back as 0% in a pool (Review Focus 5).
type Totals struct {
	Scans          int `json:"scans"`
	FilesAudited   int `json:"files_audited"`
	MutantsPlanted int `json:"mutants_planted"`
	Survivors      int `json:"survivors"`
	ProvenMissed   int `json:"proven_missed"`
	CouldNotGrade  int `json:"could_not_grade"`
	// CaughtShare is the share of graded mutants the suites caught, pooled
	// over every graded file (see caughtShare); nil when nothing was graded.
	CaughtShare *float64 `json:"caught_share"`
	// Telemetry is nil when no served scan carries a Calls row: scans from
	// before telemetry recorded no tokens, which is not the same as none.
	Telemetry *Telemetry `json:"telemetry"`
	// LastRun is the latest FinishedAt; nil when no scan recorded one.
	LastRun *time.Time `json:"last_run"`
}

// Telemetry sums the Calls rows of the scans that have any, and says how many
// scans that is, so a reader never divides by scans that recorded nothing.
// UnmeteredCalls is the calls a seat made without reporting tokens (a text
// wire): the token sums are a floor whenever it is non-zero.
type Telemetry struct {
	ScansWithTelemetry int   `json:"scans_with_telemetry"`
	ModelCalls         int64 `json:"model_calls"`
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	UnmeteredCalls     int64 `json:"unmetered_calls"`
}

// RunPoint is one scan's figures, under the same graded rule as Totals.
type RunPoint struct {
	ScanUID        string     `json:"scan_uid"`
	Repo           string     `json:"repo"`
	Commit         string     `json:"commit"`
	FinishedAt     *time.Time `json:"finished_at"`
	FilesAudited   int        `json:"files_audited"`
	MutantsPlanted int        `json:"mutants_planted"`
	Survivors      int        `json:"survivors"`
	ProvenMissed   int        `json:"proven_missed"`
	CaughtShare    *float64   `json:"caught_share"`
}

// Gap is one proven missing test: a mutant the suite let through and a test
// corral wrote, then executed, killed. AuthoredTest is the file's authored
// test, and only when the run pushed source (SourcePushed); otherwise "" —
// the custody switch is the bundle's, so a row that somehow carries source
// from a run that did not push it is not served.
type Gap struct {
	ScanUID      string `json:"scan_uid"`
	Repo         string `json:"repo"`
	Commit       string `json:"commit"`
	Path         string `json:"path"`
	MutantID     string `json:"mutant_id"`
	Outcome      string `json:"outcome"`
	AuthoredTest string `json:"authored_test"`
	SourcePushed bool   `json:"source_pushed"`
}

// ModelLine is one model's figures over the files it shares with another
// model in the same role (see summarizeModels). Files counts distinct
// (Repo, Commit, Path); Rows counts the graded rows the figures sum, which is
// more than Files when the model graded one file in several runs.
type ModelLine struct {
	Role           string   `json:"role"`
	Model          string   `json:"model"`
	Files          int      `json:"files"`
	Rows           int      `json:"rows"`
	MutantsPlanted int      `json:"mutants_planted"`
	Survivors      int      `json:"survivors"`
	ProvenMissed   int      `json:"proven_missed"`
	CaughtShare    *float64 `json:"caught_share"`
}

// ComparedRole is the seat the model comparison is about: the model that
// wrote the faults. A different generator on the same file is a different
// exam, which is what makes the comparison a question about the model. It is
// advpool.RoleMutantGenerator, which this browser-safe package cannot import;
// advpool's TestLedgerComparesTheMutantGeneratorRole holds the two equal.
const ComparedRole = "mutant-generator"

// Summarize computes a Summary from served scans, one Bundle per live,
// verified scan entry in ledger order.
//
// It takes the bundles rather than Derived's flat rows because the rows carry
// no identity of the scan they came from: Row.ScanID is the operator's local
// id and is 0 when no local ledger was written (three real scans share
// (repo, 0)), and StatementSHA256 is empty without --attest. Attribution by
// a join on those would be a guess; attribution by the bundle is by
// construction. Derive passes the same bundles whose rows it serves.
func Summarize(scans []Bundle) Summary {
	s := Summary{Runs: []RunPoint{}, Gaps: []Gap{}, Models: []ModelLine{}}
	var all tally
	var tel Telemetry
	for _, b := range scans {
		var run tally
		for _, f := range b.Files {
			run.add(f)
		}
		all.merge(run)
		s.Runs = append(s.Runs, RunPoint{
			ScanUID: b.Scan.ScanUID, Repo: b.Scan.Repo, Commit: b.Scan.Commit, FinishedAt: b.Scan.FinishedAt,
			FilesAudited: run.audited, MutantsPlanted: run.planted, Survivors: run.survivors,
			ProvenMissed: run.proven, CaughtShare: run.share(),
		})
		if b.Scan.FinishedAt != nil && (s.Totals.LastRun == nil || b.Scan.FinishedAt.After(*s.Totals.LastRun)) {
			t := *b.Scan.FinishedAt
			s.Totals.LastRun = &t
		}
		if len(b.Calls) > 0 {
			tel.ScansWithTelemetry++
			for _, c := range b.Calls {
				tel.ModelCalls += int64(c.Calls)
				tel.InputTokens += c.InputTokens
				tel.OutputTokens += c.OutputTokens
				tel.UnmeteredCalls += int64(c.UnmeteredCalls)
			}
		}
		s.Gaps = append(s.Gaps, gapsOf(b)...)
	}
	s.Totals.Scans = len(scans)
	s.Totals.FilesAudited = all.audited
	s.Totals.MutantsPlanted = all.planted
	s.Totals.Survivors = all.survivors
	s.Totals.ProvenMissed = all.proven
	s.Totals.CouldNotGrade = all.audited - all.graded
	s.Totals.CaughtShare = all.share()
	if tel.ScansWithTelemetry > 0 {
		s.Totals.Telemetry = &tel
	}
	sort.SliceStable(s.Runs, func(i, j int) bool {
		a, b := s.Runs[i], s.Runs[j]
		switch {
		case a.FinishedAt == nil || b.FinishedAt == nil:
			if (a.FinishedAt == nil) != (b.FinishedAt == nil) {
				return b.FinishedAt == nil
			}
		case !a.FinishedAt.Equal(*b.FinishedAt):
			return a.FinishedAt.Before(*b.FinishedAt)
		}
		return a.ScanUID < b.ScanUID
	})
	s.Models = summarizeModels(scans)
	return s
}

// gradedFile says whether a file's rate is a measurement that can be pooled.
//
//   - audited, and KillRate recorded: an uncovered file's or an
//     all-compile-rejected file's rate is withheld as nil (signableKillRate,
//     cmd/corral/certify_repo.go), and nil is not 0%.
//   - not TimedOut: the rule `corral scans` applies to the same rows
//     (cmd/corral/scans_ledger.go, the scan kill rate), so a page and the CLI
//     grade the same files.
//   - MutantsGraded > 0: the pooled share weights each file by its graded
//     mutants, so a rate with no recorded denominator would silently weigh
//     nothing; it is counted as ungraded instead.
//   - not NaN, which JSON cannot carry anyway.
func gradedFile(f Row) bool {
	return f.Disposition == "audited" && f.KillRate != nil && !math.IsNaN(*f.KillRate) &&
		!f.TimedOut && f.MutantsGraded > 0
}

// tally accumulates files under gradedFile.
type tally struct {
	audited, graded            int
	planted, survivors, proven int
	gradedMutants, caught      int
}

func (t *tally) add(f Row) {
	if f.Disposition != "audited" {
		return
	}
	t.audited++
	if !gradedFile(f) {
		return
	}
	t.graded++
	t.planted += f.MutantsPlanted
	t.survivors += f.Survivors
	t.proven += f.ProvenMissed
	t.gradedMutants += f.MutantsGraded
	t.caught += f.MutantsGraded - f.Survivors
}

func (t *tally) merge(o tally) {
	t.audited += o.audited
	t.graded += o.graded
	t.planted += o.planted
	t.survivors += o.survivors
	t.proven += o.proven
	t.gradedMutants += o.gradedMutants
	t.caught += o.caught
}

// share is the caught share, pooled: (graded - survivors) / graded, summed
// over the graded files. It is the recorded KillRate's own formula,
// len(Killed)/Total with Total the mutants that reached grading
// (adequacy.Report.KillRate, internal/adequacy/score.go:628; Row.MutantsGraded
// is that Total and Row.MutantsPlanted adds the compile-gate rejects,
// cmd/corral/certify_repo_bundle.go:185), so one graded file's share IS its
// KillRate. (planted - survivors)/planted would count a rejected mutant as
// caught: on a real row with 2 graded and 16 rejected, 17/18 against 0.5.
// Pooled rather than averaged so a 2-mutant file does not weigh what a
// 40-mutant one does. nil when nothing was graded.
func (t tally) share() *float64 {
	if t.gradedMutants == 0 {
		return nil
	}
	v := float64(t.caught) / float64(t.gradedMutants)
	return &v
}

// gapsOf lists a scan's proven mutants on graded files, so the gap list and
// Totals.ProvenMissed describe the same files.
func gapsOf(b Bundle) []Gap {
	files := map[string]Row{}
	for _, f := range b.Files {
		if gradedFile(f) {
			if _, seen := files[f.Path]; !seen {
				files[f.Path] = f
			}
		}
	}
	var out []Gap
	for _, m := range b.Mutants {
		f, ok := files[m.Path]
		if !m.Proven || !ok {
			continue
		}
		g := Gap{ScanUID: b.Scan.ScanUID, Repo: b.Scan.Repo, Commit: b.Scan.Commit, Path: m.Path,
			MutantID: m.MutantID, Outcome: m.Outcome, SourcePushed: b.SourcePushed}
		if b.SourcePushed {
			g.AuthoredTest = f.AuthoredTest
		}
		out = append(out, g)
	}
	return out
}

// summarizeModels is the like-for-like comparison. Two models' kill rates on
// different files compare the files as much as the models, so a line counts
// only files ((Repo, Commit, Path): the same bytes) graded under more than
// one distinct model in ComparedRole, and each model's line sums only its own
// rows on those files. A seat that named a tool rather than a model (an
// unpinned agent seat) is no evidence about a model and is left out, by the
// rule every per-model reader applies (seatrecord.RecordedSeatNamesAModel). A
// row whose ModelsByRole does not parse names no model and is left out too.
// Lines are ordered by Model.
func summarizeModels(scans []Bundle) []ModelLine {
	type key struct{ repo, commit, path string }
	type rated struct {
		model string
		f     Row
	}
	byFile := map[key][]rated{}
	for _, b := range scans {
		for _, f := range b.Files {
			if !gradedFile(f) {
				continue
			}
			if m, ok := seatModel(f, ComparedRole); ok {
				k := key{f.Repo, f.Commit, f.Path}
				byFile[k] = append(byFile[k], rated{m, f})
			}
		}
	}
	byModel := map[string]*tally{}
	files := map[string]map[key]bool{}
	for k, rs := range byFile {
		distinct := map[string]bool{}
		for _, r := range rs {
			distinct[r.model] = true
		}
		if len(distinct) < 2 {
			continue
		}
		for _, r := range rs {
			if byModel[r.model] == nil {
				byModel[r.model] = &tally{}
			}
			byModel[r.model].add(r.f)
			if files[r.model] == nil {
				files[r.model] = map[key]bool{}
			}
			files[r.model][k] = true
		}
	}
	out := []ModelLine{}
	for m, t := range byModel {
		out = append(out, ModelLine{Role: ComparedRole, Model: m, Files: len(files[m]), Rows: t.graded,
			MutantsPlanted: t.planted, Survivors: t.survivors, ProvenMissed: t.proven, CaughtShare: t.share()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// seatModel is the model a row's role seat named, when it named one.
func seatModel(f Row, role string) (string, bool) {
	var models, tools map[string]string
	if json.Unmarshal([]byte(f.ModelsByRole), &models) != nil {
		return "", false
	}
	if f.SeatTools != "" && json.Unmarshal([]byte(f.SeatTools), &tools) != nil {
		// A tool record that does not parse cannot say the seat was an API
		// model; treating it as absent would.
		return "", false
	}
	m := models[role]
	if !seatrecord.RecordedSeatNamesAModel(m, tools[role]) {
		return "", false
	}
	return m, true
}
