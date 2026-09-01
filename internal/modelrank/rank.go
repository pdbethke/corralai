// SPDX-License-Identifier: Elastic-2.0

// Package modelrank turns corral's own recorded telemetry into a per-SEAT
// ranking of the models that have sat in it: which writer actually proves
// gaps, which generator plants faults a dev suite cannot kill, which critic
// survives adjudication.
//
// WHY A DIFFERENT METRIC PER SEAT. The seats do different jobs, so a single
// leaderboard number across them is a category error. A writer is judged on
// whether the test it authored compiled, ran, and killed the fault — nothing
// else about it matters. A generator is judged on VALID mutants the dev suite
// failed to kill, because a generator whose faults are all trivially killed
// has produced volume, not difficulty. A critic is judged against human
// adjudication, the only place its findings are checked. And a goal-deriver is
// not judged at all: no recorded outcome is attributable to it, so this
// package reports it as unscored rather than inventing a number for it.
//
// DISCLOSURE, NEVER SELECTION. Nothing here writes config, fills a seat, or
// feeds a router. corral has no default models — every seat is named by the
// operator or the run refuses — and a ranking that quietly became an input to
// staffing would reintroduce that rule through the back door. It has happened
// in this project before: a production router's performance statistics
// OVERRODE the operator's configured model list and re-selected a retired
// model, forever. The output of this package is a table for a human to read.
//
// THIN EVIDENCE IS NOT A RANKING. A model with a perfect rate over three
// observations has not earned a recommendation; the live scorecard has exactly
// such a row. Rows below Options.MinRuns are PRINTED with their real numbers,
// marked insufficient, sorted below every sufficient row, and excluded from
// the prefer line. If nothing clears the bar, there is no prefer line.
package modelrank

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultMinRuns is the evidence floor below which a row is disclosed but
// never recommended. Five is not a statistical threshold — it is the smallest
// number at which a single lucky file stops dominating the rate.
const DefaultMinRuns = 5

// The seats this package knows how to score, in the order a run meets them.
// An observation naming anything else (a shadow seat, a role from a future
// version) is ignored rather than folded in: a metric defined for one seat is
// meaningless applied to another.
const (
	SeatGoalDeriver     = "goal-deriver"
	SeatMutantGenerator = "mutant-generator"
	SeatTestWriter      = "test-writer"
	SeatTestCritic      = "test-critic"
)

var seatOrder = []string{SeatGoalDeriver, SeatMutantGenerator, SeatTestWriter, SeatTestCritic}

// The two modes, named so a reader of --json can tell which question was
// answered: "did the models this project DECLARED earn their seats", or "what
// do the models in the evidence look like" when nothing is declared.
const (
	ModeRegistry = "registry"
	ModeEvidence = "evidence"
)

// Observation is one recorded outcome, already attributed to a model and a
// seat. It is deliberately a flat, source-agnostic struct: the bugcatch ledger
// and the pushed warehouse record different things, and the ranking must not
// grow a branch per store.
//
// Run is what makes an n an n. Every distinct value counts as one run, so a
// store that fans a seat out into several rows per run (bugcatch shards the
// mutant-generator) reports the runs it actually had.
type Observation struct {
	Model string
	Role  string
	Lang  string
	Run   string

	// The writer's outcome: survivors attempted, and the ones that ended in a
	// proven gap.
	Catches       int
	Opportunities int

	// The generator's outcome. Survived is the one that carries the signal;
	// Planted/Invalid/Graded are the denominators that say whether a low
	// number means "made easy faults" or "made faults that would not build".
	MutantsPlanted  int
	MutantsGraded   int
	MutantsInvalid  int
	MutantsSurvived int

	// The critic's outcome, once a human ruled on it.
	CriticConfirmed int
	CriticRefuted   int
}

// Options are the caller's questions, not tuning knobs. Declared maps a
// CONCRETE model name to the alias the project declared it under; a non-empty
// map is what puts the report in registry mode.
type Options struct {
	MinRuns  int
	Seat     string
	Lang     string
	Declared map[string]string
	Source   string
}

// Row is one (model, seat, language) cell with the metric its seat is judged
// by, plus enough of the raw counts that a reader can check the number rather
// than take it.
type Row struct {
	Seat  string `json:"seat"`
	Lang  string `json:"lang,omitempty"`
	Model string `json:"model"`
	Alias string `json:"alias,omitempty"`
	// Declared is false for a model the evidence carries but the registry
	// never named. Such a row is shown — hiding it would hide evidence — but
	// never preferred, because preferring it would be recommending a model
	// the project has not declared it is willing to run.
	Declared bool `json:"declared"`

	// Metric is the seat's own number, nil when the seat is unscored or the
	// denominator was zero. Never fabricated to 0.
	Metric      *float64 `json:"metric,omitempty"`
	MetricLabel string   `json:"metric_label"`
	// Valid is the generator's valid share (graded/planted), reported beside
	// the metric so a weak number can be read as "easy faults" or "faults
	// that did not build". nil for every other seat.
	Valid *float64 `json:"valid_share,omitempty"`

	// N is always the METRIC'S OWN DENOMINATOR, and NUnit names it: survivors
	// attempted for the writer, runs for the generator, adjudications for the
	// critic. Any other choice lets a rate rest on three observations while
	// reporting an n that clears the evidence floor.
	N          int    `json:"n"`
	NUnit      string `json:"n_unit"`
	Sufficient bool   `json:"sufficient"`
	Evidence   string `json:"evidence"`
	Note       string `json:"note,omitempty"`
}

// Group is one seat (optionally one seat × language) with its ranked rows and
// at most one prefer line.
type Group struct {
	Seat   string `json:"seat"`
	Lang   string `json:"lang,omitempty"`
	Rows   []Row  `json:"rows"`
	Prefer string `json:"prefer,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Report is the whole answer, including which mode produced it and whether
// the evidence carried a language dimension at all.
type Report struct {
	Mode    string `json:"mode"`
	Source  string `json:"source,omitempty"`
	MinRuns int    `json:"min_runs"`
	// LangDimension is false when NO observation carried a language. It is a
	// property of the evidence, not of the repo: the bugcatch ledger records
	// no language, so a rank read from it is across all languages and says so
	// rather than labelling every row with a language it did not measure.
	LangDimension bool    `json:"lang_dimension"`
	Groups        []Group `json:"groups"`
}

type agg struct {
	seat, lang, model string
	runs              map[string]bool
	catches, opps     int
	planted, graded   int
	invalid, survived int
	confirmed         int
	refuted           int
}

// Rank computes the report. It is pure: same observations in, same report out,
// no clock, no store, no network.
func Rank(obs []Observation, opt Options) Report {
	if opt.MinRuns <= 0 {
		opt.MinRuns = DefaultMinRuns
	}
	rep := Report{Mode: ModeEvidence, Source: opt.Source, MinRuns: opt.MinRuns}
	if len(opt.Declared) > 0 {
		rep.Mode = ModeRegistry
	}

	seatWanted := strings.TrimSpace(opt.Seat)
	langWanted := strings.TrimSpace(opt.Lang)

	cells := map[string]*agg{}
	for _, o := range obs {
		seat := strings.TrimSpace(o.Role)
		if !knownSeat(seat) {
			continue
		}
		lang := strings.TrimSpace(o.Lang)
		if lang != "" {
			rep.LangDimension = true
		}
		if seatWanted != "" && seat != seatWanted {
			continue
		}
		if langWanted != "" && lang != langWanted {
			continue
		}
		key := seat + "\x00" + lang + "\x00" + o.Model
		a := cells[key]
		if a == nil {
			a = &agg{seat: seat, lang: lang, model: o.Model, runs: map[string]bool{}}
			cells[key] = a
		}
		if o.Run != "" {
			a.runs[o.Run] = true
		}
		a.catches += o.Catches
		a.opps += o.Opportunities
		a.planted += o.MutantsPlanted
		a.graded += o.MutantsGraded
		a.invalid += o.MutantsInvalid
		a.survived += o.MutantsSurvived
		a.confirmed += o.CriticConfirmed
		a.refuted += o.CriticRefuted
	}

	byGroup := map[string][]Row{}
	var order []string
	for _, a := range cells {
		row := rowFor(a, opt)
		gk := a.seat + "\x00" + a.lang
		if _, seen := byGroup[gk]; !seen {
			order = append(order, gk)
		}
		byGroup[gk] = append(byGroup[gk], row)
	}

	sort.Slice(order, func(i, j int) bool {
		si, li, _ := strings.Cut(order[i], "\x00")
		sj, lj, _ := strings.Cut(order[j], "\x00")
		if si != sj {
			return seatIndex(si) < seatIndex(sj)
		}
		return li < lj
	})

	for _, gk := range order {
		seat, lang, _ := strings.Cut(gk, "\x00")
		rows := byGroup[gk]
		sortRows(rows)
		g := Group{Seat: seat, Lang: lang, Rows: rows}
		if seat == SeatGoalDeriver {
			g.Note = "not scored (goal quality is only visible downstream, via mutant yield)"
		} else {
			g.Prefer, g.Note = prefer(rows, opt)
		}
		rep.Groups = append(rep.Groups, g)
	}
	return rep
}

func knownSeat(s string) bool {
	for _, k := range seatOrder {
		if s == k {
			return true
		}
	}
	return false
}

func seatIndex(s string) int {
	for i, k := range seatOrder {
		if s == k {
			return i
		}
	}
	return len(seatOrder)
}

func ratio(num, den int) *float64 {
	if den <= 0 {
		return nil
	}
	v := float64(num) / float64(den)
	return &v
}

func rowFor(a *agg, opt Options) Row {
	row := Row{Seat: a.seat, Lang: a.lang, Model: a.model, N: len(a.runs), NUnit: "runs"}
	if alias, ok := opt.Declared[a.model]; ok {
		row.Alias = alias
		row.Declared = true
	}
	switch a.seat {
	case SeatTestWriter:
		// The ONLY outcome that matters for a writer: the authored test
		// compiled, ran, and killed the fault.
		//
		// n is SURVIVORS ATTEMPTED, not runs — the metric's own denominator.
		// This is not a detail: the live scorecard carries a writer with 22
		// runs behind a 3/3 rate, because most of those runs handed it no
		// survivor to attempt. Counting the runs would clear any evidence
		// floor and promote a 100% rate resting on three attempts, which is
		// the exact recommendation this command exists not to make.
		row.MetricLabel = "proven gaps per survivor attempted"
		row.Metric = ratio(a.catches, a.opps)
		row.N = a.opps
		row.NUnit = "survivors attempted"
		row.Evidence = fmt.Sprintf("%d/%d survivors proven over %d runs", a.catches, a.opps, len(a.runs))
	case SeatMutantGenerator:
		// Valid, graded mutants the dev suite failed to kill, PER RUN. Yield
		// and difficulty in one measured count rather than a synthesised
		// index: a generator that plants 100 trivially-killed faults scores
		// below one that plants 20 the suite misses.
		// n stays RUNS here, because runs IS this metric's denominator.
		row.MetricLabel = "valid mutants the dev suite missed, per run"
		if len(a.runs) > 0 {
			v := float64(a.survived) / float64(len(a.runs))
			row.Metric = &v
		}
		// Absent, not zero, when the store records no graded count: 0% valid
		// is a positive claim about what the generator produced, and the
		// bug-catching ledger simply does not carry the column.
		if a.graded > 0 {
			row.Valid = ratio(a.graded, a.planted)
		}
		row.Evidence = fmt.Sprintf("%d planted, %d graded, %d invalid, %d survived over %d runs",
			a.planted, a.graded, a.invalid, a.survived, len(a.runs))
	case SeatTestCritic:
		// Precision against ADJUDICATION, and n is adjudications — a critic
		// can sit in a hundred runs without one of its findings having been
		// ruled on, and counting those runs as evidence would recommend a
		// critic nobody has checked.
		row.MetricLabel = "precision against human adjudication"
		row.Metric = ratio(a.confirmed, a.confirmed+a.refuted)
		row.N = a.confirmed + a.refuted
		row.NUnit = "adjudications"
		row.Evidence = fmt.Sprintf("%d confirmed, %d refuted", a.confirmed, a.refuted)
	case SeatGoalDeriver:
		row.MetricLabel = "not scored"
		row.Evidence = fmt.Sprintf("%d runs", len(a.runs))
		row.Note = "no recorded outcome is attributable to this seat"
	}
	row.Sufficient = row.Metric != nil && row.N >= opt.MinRuns
	if a.seat != SeatGoalDeriver && !row.Sufficient {
		row.Note = fmt.Sprintf("insufficient evidence (n=%d)", row.N)
	}
	return row
}

// sortRows puts every sufficient row above every insufficient one, so a
// perfect rate at n=3 can never appear to lead the table.
func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Sufficient != b.Sufficient {
			return a.Sufficient
		}
		am, bm := a.Metric != nil, b.Metric != nil
		if am != bm {
			return am
		}
		if am && *a.Metric != *b.Metric {
			return *a.Metric > *b.Metric
		}
		if a.N != b.N {
			return a.N > b.N
		}
		return a.Model < b.Model
	})
}

// prefer names at most one model per group, and only from rows that cleared
// the evidence floor — in registry mode, only from DECLARED ones. It returns
// a note instead when nothing qualifies, because "no answer" is an answer and
// a blank line is not.
func prefer(rows []Row, opt Options) (string, string) {
	registry := len(opt.Declared) > 0
	for _, r := range rows {
		if !r.Sufficient {
			continue
		}
		if registry && !r.Declared {
			continue
		}
		if r.Alias != "" {
			return r.Alias, ""
		}
		return r.Model, ""
	}
	if registry {
		return "", fmt.Sprintf("no declared model has %d+ observations in this seat yet — nothing to prefer", opt.MinRuns)
	}
	return "", fmt.Sprintf("no model has %d+ observations in this seat yet — nothing to prefer", opt.MinRuns)
}
