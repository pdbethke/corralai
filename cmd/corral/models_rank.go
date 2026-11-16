// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/models"
	"github.com/pdbethke/corralai/internal/review"
	"github.com/pdbethke/corralai/internal/statedir"
)

// rankEvidence is one store's worth of already-attributed outcomes, plus a
// human-readable name for WHERE they came from. Every ranking line printed
// below is traceable to this one string, because a number whose source the
// reader cannot name is not evidence.
type rankEvidence struct {
	Obs    []modelrank.Observation
	Source string
	// Generations is which grading rules (VerdictGeneration) the scans behind
	// Obs were graded under. Zero when the store records none (the local
	// bugcatch ledger) or has no scans.
	Generations generationRange
}

// generationRange says which grading-rule generations a body of evidence
// spans. Ranking pools observations from scans graded under different rules;
// a reader comparing two runs of `corral models rank` needs to know whether
// the pool changed generation underneath them. Min and Max are integers
// carried as text, as the column records them; Unrecorded counts scans whose
// generation is absent or not an integer, which are never folded into the
// range; Scans counts the distinct scans that contributed an observation.
type generationRange struct {
	Min, Max   string
	Unrecorded int
	Scans      int
}

func (g generationRange) String() string {
	if g.Scans == 0 {
		return ""
	}
	if g.Min == "" {
		return "no scan records a generation"
	}
	s := "generation " + g.Min
	if g.Min != g.Max {
		s = "generations " + g.Min + " to " + g.Max
	}
	if g.Unrecorded > 0 {
		s += fmt.Sprintf("; %d scan(s) record none", g.Unrecorded)
	}
	return s
}

// rankReportJSON is the report `--json` emits: the modelrank report as it
// always was, plus the generations the evidence spans. It lives here, not in
// internal/modelrank, which stays agnostic of where its evidence came from.
type rankReportJSON struct {
	modelrank.Report
	Generations *generationJSON `json:"generations,omitempty"`
}

type generationJSON struct {
	Min        string `json:"min"`
	Max        string `json:"max"`
	Unrecorded int    `json:"unrecorded"`
	Scans      int    `json:"scans"`
}

// Kinds of evidence source `corral models rank` can read. Exactly one is
// used per run and none falls back to another: a ranking that quietly read a
// different body of evidence than the operator asked for would be a wrong
// answer that looks right.
const (
	rankSourceLedger    = "ledger"    // this machine's ledger (the default)
	rankSourceScorecard = "scorecard" // the old local bug-catching store
	rankSourceDB        = "db"        // a ledger directory or warehouse the operator named
)

// rankSource says where a ranking's evidence comes from. Target is set only
// for Kind "db": the directory, DuckDB file or `md:` DSN the operator named.
type rankSource struct{ Kind, Target string }

// rankLoader reads the evidence a rankSource names. A source the operator
// NAMED (--db) and corral could not reach is a refusal, never a silent
// fallback to a different body of evidence.
type rankLoader func(src rankSource) (rankEvidence, error)

// runModels dispatches `corral models <verb>`. Only `rank` exists; the noun is
// a group rather than a bare `corral rank` because the registry (`.corral/
// models.json`) is the other half of this surface and will want verbs of its
// own.
func runModels(args []string, repoRoot string, load rankLoader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, modelsUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "rank":
		return runModelsRank(args[1:], repoRoot, load, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "corral models: unknown verb %q\n%s\n", args[0], modelsUsage)
		return 2
	}
}

const modelsUsage = `usage: corral models rank [--source ledger|scorecard] [--db <dir or dsn>] [--seat <role>] [--lang <name>] [--min-runs N] [--json]
  A bare run reads this machine's ledger (the entries every certify and review on this machine
  wrote); --source scorecard reads the old local bug-catching store instead, and --db reads a
  ledger directory or a pushed warehouse you name.
  seats: goal-deriver, mutant-generator, test-writer, test-critic, and — from a ledger directory's
  review entries — reviewer (claims that held, of those checked) and verifier (verdicts that agreed
  with the outcome; a person's adjudication is the outcome when there is one, execution otherwise),
  and committer: the audited party — the commit's author and each Co-authored-by trailer (an
  agent, in code an agent helped write) — by the changes that held under audit (a scan that passed
  its gate; a review none of whose checked claims held). The record names a person and an agent
  the same way, under the same evidence floor; what a reader does with the row is the reader's.

  Rank the models that have sat in each seat by what corral's OWN recorded
  evidence says about them — a different metric per seat, because the seats do
  different jobs.

  This is DISCLOSURE, NOT SELECTION. It prints a table. It writes no config,
  changes no default, and feeds no router: corral has no default models, and a
  ranking that quietly staffed a seat would put one back.`

func runModelsRank(args []string, repoRoot string, load rankLoader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models rank", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("db", "", "a ledger directory or pushed warehouse to read instead of this machine's ledger: a ledger directory, a DuckDB file path or an `md:<db>` MotherDuck DSN. Unreachable is a refusal, never a quiet fall back to a different body of evidence")
	source := fs.String("source", "", "where a run without --db reads its evidence: `ledger` (this machine's ledger, the default) or `scorecard` (the old local bug-catching store certify --local used to feed)")
	seat := fs.String("seat", "", "rank only this seat: goal-deriver, mutant-generator, test-writer, test-critic, reviewer, verifier or committer")
	lang := fs.String("lang", "", "rank only this language — needs evidence that records one (the local bugcatch ledger does not; a pushed warehouse does)")
	minRuns := fs.Int("min-runs", modelrank.DefaultMinRuns, "the evidence floor (default 5): a model with fewer observations in a seat is still PRINTED, with its real numbers, but marked insufficient and never preferred")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, modelsUsage)
		fmt.Fprintln(stderr, "\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if s := strings.TrimSpace(*seat); s != "" && !knownSeatName(s) {
		fmt.Fprintf(stderr, "corral models rank: --seat %q is not a seat — it must be one of: %s\n", s, strings.Join(rankSeats, ", "))
		return 2
	}

	target := strings.TrimSpace(*dsn)
	sourceSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "source" {
			sourceSet = true
		}
	})
	src := rankSource{Kind: rankSourceLedger}
	switch {
	case target != "" && sourceSet:
		fmt.Fprintln(stderr, "corral models rank: --db names a source already; --source picks between this machine's ledger and the old scorecard store")
		return 2
	case target != "":
		src = rankSource{Kind: rankSourceDB, Target: target}
	case sourceSet:
		switch strings.TrimSpace(*source) {
		case rankSourceLedger, "":
		case rankSourceScorecard:
			src.Kind = rankSourceScorecard
		default:
			fmt.Fprintf(stderr, "corral models rank: --source %q is not a source — it must be one of: ledger, scorecard\n", *source)
			return 2
		}
	}
	ev, err := load(src)
	if err != nil {
		fmt.Fprintln(stderr, "corral models rank:", err)
		if src.Kind == rankSourceDB {
			// Named-but-unreachable is exit 2, distinct from "the default
			// store failed": the operator asked a specific question of a
			// specific warehouse and got no answer from it.
			return 2
		}
		return 1
	}

	opt := modelrank.Options{
		MinRuns: *minRuns,
		Seat:    strings.TrimSpace(*seat),
		Lang:    strings.TrimSpace(*lang),
		Source:  ev.Source,
	}
	// The registry is READ, never required: a project that declares none gets
	// the concrete models the evidence carries, and the report says which of
	// the two it is.
	reg, rerr := models.Load(repoRoot)
	if rerr != nil {
		fmt.Fprintln(stderr, "corral models rank:", rerr)
		return 1
	}
	declared := declaredByModel(reg)
	opt.Declared = declared

	rep := modelrank.Rank(ev.Obs, opt)

	if l := strings.TrimSpace(*lang); l != "" && !rep.LangDimension {
		fmt.Fprintf(stderr, "corral models rank: --lang %q, but %s records no language on its observations — read a pushed warehouse with --db <dsn> (corral_audits carries a lang column), or drop --lang\n", l, ev.Source)
		return 2
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		out := rankReportJSON{Report: rep}
		if g := ev.Generations; g.Scans > 0 {
			out.Generations = &generationJSON{Min: g.Min, Max: g.Max, Unrecorded: g.Unrecorded, Scans: g.Scans}
		}
		_ = enc.Encode(out)
		return 0
	}
	printRankReport(rep, reg, ev.Generations, stdout)
	return 0
}

var rankSeats = []string{modelrank.SeatGoalDeriver, modelrank.SeatMutantGenerator, modelrank.SeatTestWriter, modelrank.SeatTestCritic, modelrank.SeatReviewer, modelrank.SeatVerifier, modelrank.SeatCommitter}

func knownSeatName(s string) bool {
	for _, k := range rankSeats {
		if k == s {
			return true
		}
	}
	return false
}

// declaredByModel inverts the registry: concrete model name -> the alias the
// project declared it under. Two aliases naming one model is legal (a fast and
// a strong seat can share a model), and the lexically first wins so the label
// is stable across runs rather than map-order noise.
func declaredByModel(reg *models.Registry) map[string]string {
	if reg == nil || reg.Len() == 0 {
		return nil
	}
	out := map[string]string{}
	aliases := reg.Aliases()
	sort.Strings(aliases)
	for _, a := range aliases {
		e, ok := reg.Lookup(a)
		if !ok {
			continue
		}
		if _, taken := out[e.Model]; !taken {
			out[e.Model] = a
		}
	}
	return out
}

func printRankReport(rep modelrank.Report, reg *models.Registry, gen generationRange, w io.Writer) {
	fmt.Fprintln(w, "corral models rank — what corral's own recorded evidence says about each seat.")
	switch rep.Mode {
	case modelrank.ModeRegistry:
		fmt.Fprintf(w, "mode: registry — ranking the %d model(s) %s declares, plus any others the evidence carries.\n", reg.Len(), reg.Source)
	default:
		fmt.Fprintln(w, "mode: evidence — this project declares no model registry, so the concrete models found in the evidence are ranked instead.")
	}
	fmt.Fprintf(w, "evidence: %s\n", rep.Source)
	if g := gen.String(); g != "" {
		fmt.Fprintf(w, "grading rules: %s\n", g)
	}
	fmt.Fprintf(w, "min-runs: %d — a row below it is printed with its real numbers, marked insufficient, and never preferred.\n", rep.MinRuns)
	if !rep.LangDimension {
		fmt.Fprintln(w, "language: this evidence records none, so every row is across all languages.")
	}
	fmt.Fprintln(w, "DISCLOSURE, NOT SELECTION: this command sets no default and staffs no seat. corral has no default models.")
	if len(rep.Groups) == 0 {
		fmt.Fprintln(w, "\nno recorded observations for any seat yet.")
		return
	}
	for _, g := range rep.Groups {
		head := g.Seat
		if g.Lang != "" {
			head += " · " + g.Lang
		}
		fmt.Fprintf(w, "\n%s\n", head)
		if len(g.Rows) > 0 && g.Rows[0].MetricLabel != "" {
			fmt.Fprintf(w, "metric: %s\n", g.Rows[0].MetricLabel)
		}
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "MODEL\tALIAS\tMETRIC\tN\tEVIDENCE\tNOTE\t")
		for _, r := range g.Rows {
			alias := r.Alias
			if alias == "" {
				alias = "—"
				if rep.Mode == modelrank.ModeRegistry {
					alias = "(undeclared)"
				}
			}
			note := r.Note
			if r.Valid != nil {
				vs := fmt.Sprintf("valid %.0f%%", *r.Valid*100)
				if note == "" {
					note = vs
				} else {
					note = vs + "; " + note
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d %s\t%s\t%s\t\n",
				r.Model, alias, rankMetricCell(r), r.N, r.NUnit, r.Evidence, note)
		}
		tw.Flush()
		if g.Prefer != "" {
			fmt.Fprintf(w, "prefer: %s\n", g.Prefer)
		} else if g.Note != "" {
			fmt.Fprintf(w, "%s\n", g.Note)
		}
	}
}

// rankMetricCell renders a seat's metric in its own units: the writer's and
// critic's are rates, the generator's is a count per run. Printing a count as
// a percentage (or the reverse) is the kind of unit slip a reader cannot catch
// from the table alone.
func rankMetricCell(r modelrank.Row) string {
	if r.Metric == nil {
		return "—"
	}
	if r.Seat == modelrank.SeatMutantGenerator {
		return fmt.Sprintf("%.1f/run", *r.Metric)
	}
	return fmt.Sprintf("%.0f%%", *r.Metric*100)
}

// bugcatchRankEvidence adapts the local bug-catching ledger — the same rows
// `corral scorecard` reports — into ranking observations, joined with the
// critic-adjudication store so the critic seat is scored against the human
// verdicts rather than left blank.
//
// Two honest gaps, disclosed rather than papered over: this ledger records NO
// language (so the report is across all languages, and --lang refuses), and no
// mutants_graded/mutants_invalid (so the generator's valid share is left
// absent instead of computed as 0, which would be the positive claim "nothing
// it planted was valid"). A pushed warehouse carries both.
func bugcatchRankEvidence(ctx context.Context, store *bugcatch.Store, critic *criticscore.Store) (rankEvidence, error) {
	ev := rankEvidence{Source: "the local bug-catching ledger (the same rows `corral scorecard` reports; a run is one audited FILE)"}
	if store != nil {
		// EVERY row, streamed — Observations() caps at its most recent
		// 10 000 and would rank a ledger past that over a sample nobody
		// chose, with the oldest writer's record the first to vanish.
		err := store.EveryObservation(ctx, func(o bugcatch.Observation) error {
			// Shadow seats are a decorrelation experiment running alongside
			// the real one; their outcomes never reached a verdict, so they
			// are not evidence about a seat's work.
			if o.Shadow {
				return nil
			}
			// A seat that named a tool, not a model, is not evidence about
			// a model (the one rule; see bugcatch's resolvedOnly).
			if o.ModelUnresolved {
				return nil
			}
			ev.Obs = append(ev.Obs, modelrank.Observation{
				Model:           o.Model,
				Role:            o.Role,
				Run:             fmt.Sprintf("%d", o.RecordID),
				Catches:         o.Catches,
				Opportunities:   o.Opportunities,
				MutantsPlanted:  o.MutantsPlanted,
				MutantsSurvived: o.MutantsSurvived,
			})
			return nil
		})
		if err != nil {
			return rankEvidence{}, err
		}
	}
	if critic != nil {
		cells, err := critic.Precision(ctx)
		if err != nil {
			return rankEvidence{}, err
		}
		for _, c := range cells {
			if c.Confirmed+c.Refuted == 0 {
				continue
			}
			ev.Obs = append(ev.Obs, modelrank.Observation{
				Model:           c.Model,
				Role:            modelrank.SeatTestCritic,
				Run:             "criticscore:" + c.Model,
				CriticConfirmed: c.Confirmed,
				CriticRefuted:   c.Refuted,
			})
		}
	}
	return ev, nil
}

// warehouseRankEvidence reads a pushed warehouse's corral_audits — the same
// rows `corral seal` and `corral verify --db` read — and expands each audited
// file into one observation per seat that worked on it, using the row's own
// models_by_role. This is the evidence that carries a LANGUAGE, which is why
// --lang needs it.
//
// The critic seat is absent here on purpose: adjudication lives in the local
// criticscore store and is never pushed, so a warehouse can say nothing about
// critic precision and this function invents nothing to fill the gap.
//
// WHAT COUNTS. The same rules bugCatchObservations applies on the local
// path, because the warehouse is that ledger pushed and must not re-open
// what the local path closed: a row served from the verdict cache
// (cache_hit / reused_from_scan_id) is a run in which no model did
// anything, and is skipped — it used to count its scan as a second "run"
// and its numbers a second time; a row whose grading never happened
// (baseline_failed, timed_out) is evidence about nothing, and is skipped;
// a row whose WRITER half never graded (test_writer_failed,
// pool_test_unsound) keeps its generator evidence — the mutants were
// planted and graded — but charges the writer no opportunities, since a
// pipeline failure is not a property of the model. Four rows that used to
// report the writer at 6/25 over "3 runs" now report the one measured
// record: 3/4 over 1.
//
// "run" here is the SCAN (one per scan_id), where the local ledger's is the
// audited FILE (one per record_id); the Source line says which, because the
// generator's "per run" count is a different number under the two.
func warehouseRankEvidence(db *sql.DB, dsn string) (rankEvidence, error) {
	ev := rankEvidence{Source: "the pushed warehouse " + dsn + " (corral_audits; a run is one SCAN, not one audited file as in the local ledger)"}
	// A file row reaches its scan through scan_uid, the identity every grain
	// of one push shares. attachWarehouse never migrates, and rank is
	// read-only, so a warehouse pushed before scan_uid (schema 3) or
	// verdict_generation existed lacks the column: it is probed, and an absent
	// one reads as a scan that records no generation rather than failing a
	// ranking that worked before.
	auditCols, err := warehouseColumns(db, "corral_audits")
	if err != nil {
		return rankEvidence{}, err
	}
	scanUIDExpr := "''"
	if auditCols["scan_uid"] {
		scanUIDExpr = "COALESCE(a.scan_uid, '')"
	}
	genByUID, err := scanGenerationsByUID(db)
	if err != nil {
		return rankEvidence{}, err
	}
	rows, err := db.Query(`SELECT COALESCE(a.lang, ''), COALESCE(a.models_by_role, ''), COALESCE(a.seat_tools, ''), COALESCE(a.scan_id, 0),
		COALESCE(a.path, ''), COALESCE(a.survivors, 0), COALESCE(a.proven_missed, 0),
		COALESCE(a.mutants_planted, 0), COALESCE(a.mutants_graded, 0), COALESCE(a.mutants_invalid, 0),
		COALESCE(a.cache_hit, false), a.reused_from_scan_id IS NOT NULL,
		COALESCE(a.baseline_failed, false), COALESCE(a.timed_out, false),
		COALESCE(a.test_writer_failed, false), COALESCE(a.pool_test_unsound, false),
		` + scanUIDExpr + `
		FROM corral_audits a WHERE a.disposition = 'audited'`)
	if err != nil {
		return rankEvidence{}, fmt.Errorf("reading corral_audits from %s: %w", dsn, err)
	}
	defer rows.Close()
	// scanGen maps each contributing scan to its recorded generation ("" when
	// none): only scans that put an observation into ev.Obs are in it.
	scanGen := map[string]string{}
	for rows.Next() {
		var lang, byRole, toolsJSON, path, scanUID string
		var scanID int64
		var survivors, proven, planted, graded, invalid int
		var cacheHit, reused, baselineFailed, timedOut, writerFailed, unsound bool
		if err := rows.Scan(&lang, &byRole, &toolsJSON, &scanID, &path, &survivors, &proven, &planted, &graded, &invalid,
			&cacheHit, &reused, &baselineFailed, &timedOut, &writerFailed, &unsound, &scanUID); err != nil {
			return rankEvidence{}, fmt.Errorf("scanning corral_audits from %s: %w", dsn, err)
		}
		if cacheHit || reused || baselineFailed || timedOut {
			continue
		}
		writerGraded := !writerFailed && !unsound
		seats := map[string]string{}
		if strings.TrimSpace(byRole) != "" {
			if err := json.Unmarshal([]byte(byRole), &seats); err != nil {
				// A row whose models_by_role cannot be read is not
				// attributable to any model; skipping it loses one row,
				// while guessing would attribute an outcome to the wrong seat.
				continue
			}
		}
		// The run key is the SCAN, not the file: a scan that audited 30 files
		// with one writer is one run's worth of evidence about that writer,
		// and counting it as 30 would clear any evidence floor on the first
		// scan.
		// The roster's agent tools: an unreadable or absent value is no
		// tools, which reads every seat as the API seat it then was.
		tools := map[string]string{}
		if strings.TrimSpace(toolsJSON) != "" {
			_ = json.Unmarshal([]byte(toolsJSON), &tools)
		}
		run := fmt.Sprintf("%d", scanID)
		for role, model := range seats {
			if strings.TrimSpace(model) == "" || strings.EqualFold(model, "off") {
				continue
			}
			// The one rule: a seat that named a tool, not a model, is not
			// evidence about a model.
			if !agentbackend.RecordedSeatNamesAModel(model, tools[role]) {
				continue
			}
			o := modelrank.Observation{Model: model, Role: role, Lang: lang, Run: run}
			switch role {
			case modelrank.SeatTestWriter:
				if !writerGraded {
					continue
				}
				o.Catches, o.Opportunities = proven, survivors
			case modelrank.SeatMutantGenerator:
				o.MutantsPlanted, o.MutantsGraded, o.MutantsInvalid, o.MutantsSurvived = planted, graded, invalid, survivors
			}
			ev.Obs = append(ev.Obs, o)
			// A scan with no uid (a pre-3 push) is keyed by its scan id, so
			// it still counts once rather than vanishing from the range.
			key := scanUID
			if key == "" {
				key = "id:" + run
			}
			scanGen[key] = genByUID[scanUID]
		}
	}
	if err := rows.Err(); err != nil {
		return ev, err
	}
	ev.Generations = summarizeGenerations(scanGen)
	return ev, nil
}

// warehouseColumns is the column set a table has in the current catalog
// (empty when the table is absent). duckdb_columns() with current_database(),
// not information_schema, for the reason auditpush.migrateTable gives: the
// warehouse is an ATTACHed catalog.
func warehouseColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT column_name FROM duckdb_columns()
		WHERE database_name = current_database() AND table_name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("probing %s columns: %w", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("probing %s columns: %w", table, err)
		}
		cols[n] = true
	}
	return cols, rows.Err()
}

// scanGenerationsByUID maps scan_uid to its recorded generation, empty when
// the warehouse predates the column. Should one scan_uid carry several
// corral_scans rows, the highest INTEGER wins, taken here in Go: a text max
// over a VARCHAR would rank "9" above "40".
func scanGenerationsByUID(db *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	cols, err := warehouseColumns(db, "corral_scans")
	if err != nil {
		return nil, err
	}
	if !cols["scan_uid"] || !cols["verdict_generation"] {
		return out, nil
	}
	rows, err := db.Query(`SELECT scan_uid, verdict_generation FROM corral_scans
		WHERE scan_uid IS NOT NULL AND verdict_generation IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("reading corral_scans verdict_generation: %w", err)
	}
	defer rows.Close()
	best := map[string]int{}
	for rows.Next() {
		var uid, gen string
		if err := rows.Scan(&uid, &gen); err != nil {
			return nil, fmt.Errorf("reading corral_scans verdict_generation: %w", err)
		}
		n, perr := strconv.Atoi(strings.TrimSpace(gen))
		if perr != nil || n < 0 {
			continue
		}
		if cur, ok := best[uid]; !ok || n > cur {
			best[uid] = n
			out[uid] = strconv.Itoa(n)
		}
	}
	return out, rows.Err()
}

// summarizeGenerations folds per-scan generations into a range. They compare
// as integers ("9" < "40"); a value that is not an integer is unrecorded,
// because a range built on text order would put 9 above 40.
func summarizeGenerations(scanGen map[string]string) generationRange {
	g := generationRange{Scans: len(scanGen)}
	lo, hi := -1, -1
	for _, v := range scanGen {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			g.Unrecorded++
			continue
		}
		if lo < 0 || n < lo {
			lo, g.Min = n, strconv.Itoa(n)
		}
		if n > hi {
			hi, g.Max = n, strconv.Itoa(n)
		}
	}
	return g
}

// defaultRankLoader is the wiring `corral models rank` uses in production:
// this machine's ledger by default, the old local bug-catching store (plus
// the local adjudication store) on --source scorecard, or a ledger directory
// or ATTACHed warehouse when the operator names one.
//
// Every open here is READ-ONLY. This command reports on runs that already
// happened; it must not be able to alter one, and resolving the machine
// ledger must not create its directory.
func defaultRankLoader(src rankSource) (rankEvidence, error) {
	ctx := context.Background()
	switch src.Kind {
	case rankSourceDB:
		return rankFromView(src.Target, func() (*sql.DB, error) {
			if auditpush.IsLedgerDir(src.Target) {
				// Only what a ranking reads: the full view is 79,760 single-row
				// inserts on a real ledger and takes minutes.
				return auditpush.LoadDirForRank(src.Target)
			}
			db, err := attachWarehouse(src.Target, true)
			if err != nil {
				return nil, fmt.Errorf("cannot read the warehouse %s: %w — check the path or `md:` DSN (and, for MotherDuck, that motherduck_token is set); no ranking was produced, and nothing fell back to a different body of evidence", src.Target, err)
			}
			return db, nil
		}, nil)
	case rankSourceLedger:
		return machineLedgerRankEvidence()
	case rankSourceScorecard:
	default:
		return rankEvidence{}, fmt.Errorf("unknown evidence source %q", src.Kind)
	}
	bugCatchPath, err := statedir.PathForWrite(statedir.BugCatchDB)
	if err != nil {
		return rankEvidence{}, err
	}
	store, err := bugcatch.Open(bugCatchPath)
	if err != nil {
		if strings.Contains(err.Error(), "Conflicting lock is held") {
			return rankEvidence{}, fmt.Errorf("the bug-catching ledger is held by the running brain — stop it, or point --db at a pushed warehouse")
		}
		return rankEvidence{}, fmt.Errorf("open the bug-catching ledger: %w", err)
	}
	defer func() { _ = store.Close() }()
	// The adjudication store is best-effort, exactly as it is for the
	// scorecard's C-PREC column: without it the critic seat has no scored
	// rows, which is honest, while refusing the whole ranking over one seat
	// would not be.
	critic, closeCritic := openCriticStoreBestEffort("corral models rank", os.Stderr)
	defer closeCritic()
	return bugcatchRankEvidence(ctx, store, critic)
}

// rankFromView reads every seat's evidence from one view (a warehouse, or a
// ledger directory loaded as the tables a warehouse holds) and closes it.
// head, when non-nil, supplies the Source line's opening in place of
// warehouseRankEvidence's own (which names a warehouse); the per-seat
// disclosures withReviewSeats appends still follow it.
func rankFromView(label string, open func() (*sql.DB, error), head func(*sql.DB) (string, error)) (rankEvidence, error) {
	db, err := open()
	if err != nil {
		return rankEvidence{}, err
	}
	defer db.Close()
	ev, err := warehouseRankEvidence(db, label)
	if err != nil {
		return ev, err
	}
	if head != nil {
		if ev.Source, err = head(db); err != nil {
			return rankEvidence{}, err
		}
	}
	return withReviewSeats(db, ev)
}

// withReviewSeats adds the review loop's seats, from the review grains — a
// warehouse's own tables, or the view a ledger directory loads as.
func withReviewSeats(db *sql.DB, ev rankEvidence) (rankEvidence, error) {
	robs, rerr := reviewRankEvidence(db)
	if rerr != nil {
		return rankEvidence{}, rerr
	}
	ev.Obs = append(ev.Obs, robs...)
	if len(robs) > 0 {
		ev.Source += "; reviewer and verifier seats from corral_findings joined to corral_adjudications"
	}
	cobs, cerr := committerRankEvidence(db)
	if cerr != nil {
		return rankEvidence{}, cerr
	}
	ev.Obs = append(ev.Obs, cobs...)
	if len(cobs) > 0 {
		ev.Source += "; the committer seat from corral_scans.author/co_authors and the reviews of each commit"
	}
	return ev, nil
}

// machineLedgerRankEvidence ranks from this machine's ledger. The path is
// resolved with statedir.Path, which has no side effects: a machine that has
// never written an entry has no such directory, and a read must not make one.
// A ledger that does not verify is a refusal that names the directory.
func machineLedgerRankEvidence() (rankEvidence, error) {
	dir, err := statedir.Path(statedir.MachineLedger)
	if err != nil {
		return rankEvidence{}, err
	}
	if _, serr := os.Stat(dir); serr != nil {
		if os.IsNotExist(serr) {
			return rankEvidence{Source: "the machine ledger " + dir + " (not created yet: no run on this machine has written an entry)"}, nil
		}
		return rankEvidence{}, fmt.Errorf("cannot read the machine ledger %s: %w", dir, serr)
	}
	return rankFromView(dir, func() (*sql.DB, error) {
		db, lerr := auditpush.LoadDirForRank(dir)
		if lerr != nil {
			return nil, fmt.Errorf("the machine ledger %s: %w", dir, lerr)
		}
		return db, nil
	}, func(db *sql.DB) (string, error) {
		var scans int
		if err := db.QueryRow(`SELECT count(*) FROM corral_scans`).Scan(&scans); err != nil {
			return "", fmt.Errorf("counting scans in the machine ledger %s: %w", dir, err)
		}
		if scans == 0 {
			return "the machine ledger " + dir + " (no entries yet)", nil
		}
		return fmt.Sprintf("the machine ledger %s (%d scan(s); a run is one SCAN)", dir, scans), nil
	})
}

// reviewRankEvidence grades the reviewer and verifier seats from the
// review grains: one observation per finding, joined to the NEWEST
// adjudication of that finding, graded by the one rule in internal/review
// (Grade) — a person's verdict is the outcome when there is one,
// execution otherwise, and a claim with no outcome grades nobody. The
// review entry is the run.
func reviewRankEvidence(db *sql.DB) ([]modelrank.Observation, error) {
	rows, err := db.Query(`SELECT f.review_uid, COALESCE(r.reviewer_model, ''), COALESCE(r.reviewer_tool, ''), COALESCE(r.verifier_tool, ''), COALESCE(r.lang, ''),
	    f.declared_tier, f.tier, f.exit_code, COALESCE(f.unrun, ''),
	    COALESCE(f.refutation_model, ''), COALESCE(f.refutation_verdict, ''), COALESCE(f.refutation_tier, ''), f.refutation_exit_code, COALESCE(f.refutation_unrun, ''),
	    COALESCE(a.verdict, ''), COALESCE(a.decided_by, '')
	  FROM corral_findings f
	  LEFT JOIN corral_reviews r ON r.review_uid = f.review_uid
	  LEFT JOIN (
	    SELECT review_uid, finding_id, verdict, decided_by,
	           row_number() OVER (PARTITION BY review_uid, finding_id ORDER BY ts DESC) AS rn
	    FROM corral_adjudications
	  ) a ON a.review_uid = f.review_uid AND a.finding_id = f.finding_id AND a.rn = 1`)
	if err != nil {
		return nil, fmt.Errorf("reading the review grains: %w", err)
	}
	defer rows.Close()
	var obs []modelrank.Observation
	for rows.Next() {
		var uid, reviewer, reviewerTool, verifierTool, lang, declared, tier, unrun, vModel, vVerdict, vTier, vUnrun, aVerdict, aBy string
		var exit, vExit sql.NullInt64
		if err := rows.Scan(&uid, &reviewer, &reviewerTool, &verifierTool, &lang, &declared, &tier, &exit, &unrun, &vModel, &vVerdict, &vTier, &vExit, &vUnrun, &aVerdict, &aBy); err != nil {
			return nil, err
		}
		f := findingFromRow(declared, tier, exit, unrun, vModel, vVerdict, vTier, vExit, vUnrun)
		var adj *review.Adjudicated
		if aVerdict != "" {
			adj = &review.Adjudicated{Verdict: aVerdict, By: aBy}
		}
		g := review.Grade(f, adj)
		// The one rule, for review's seats as for certify's: a seat that
		// named a tool, not a model, is not evidence about a model.
		if g.ReviewerChecked && agentbackend.RecordedSeatNamesAModel(reviewer, reviewerTool) {
			obs = append(obs, modelrank.Observation{Model: reviewer, Role: modelrank.SeatReviewer, Lang: lang, Run: uid,
				ReviewClaimsChecked: 1, ReviewClaimsHeld: boolToInt(g.ReviewerHeld)})
		}
		if g.VerifierCalled && agentbackend.RecordedSeatNamesAModel(vModel, verifierTool) {
			obs = append(obs, modelrank.Observation{Model: vModel, Role: modelrank.SeatVerifier, Lang: lang, Run: uid,
				VerifierCalls: 1, VerifierCorrect: boolToInt(g.VerifierCorrect)})
		}
	}
	return obs, rows.Err()
}

// committerRankEvidence is the audited party's evidence: one observation per
// audit of a change that reached a verdict, for the commit's author and for
// each co-author. A scan is a verdict when it carried a gate (passed is not
// NULL); a review is a verdict when a claim of it was checked, and the change
// held when none of the checked claims did. Rows written before the party
// was recorded (author NULL) are not evidence about anyone.
func committerRankEvidence(db *sql.DB) ([]modelrank.Observation, error) {
	var obs []modelrank.Observation
	emit := func(author, coAuthors, lang, run string, held bool) {
		seen := map[string]bool{}
		for _, who := range append([]string{author}, auditpush.Identity{CoAuthors: coAuthors}.CoAuthorList()...) {
			// One party, one observation per audit — a row written before
			// CommitIdentity folded a repeated trailer still names it twice.
			if who == "" || seen[strings.ToLower(who)] {
				continue
			}
			seen[strings.ToLower(who)] = true
			obs = append(obs, modelrank.Observation{Model: who, Role: modelrank.SeatCommitter, Lang: lang, Run: run,
				ChangesAudited: 1, ChangesHeld: boolToInt(held)})
		}
	}
	scans, err := db.Query(`SELECT commit_sha, author, COALESCE(co_authors, ''), passed FROM corral_scans WHERE author IS NOT NULL AND passed IS NOT NULL`)
	if err != nil {
		if strings.Contains(err.Error(), "author") { // a warehouse older than the column: nothing recorded
			return nil, nil
		}
		return nil, fmt.Errorf("reading the scans' party: %w", err)
	}
	for scans.Next() {
		var commit, author, co string
		var passed bool
		if err := scans.Scan(&commit, &author, &co, &passed); err != nil {
			scans.Close()
			return nil, err
		}
		emit(author, co, "", commit, passed)
	}
	scans.Close()
	rows, err := db.Query(`SELECT r.review_uid, r.commit_sha, r.author, COALESCE(r.co_authors, ''), COALESCE(r.lang, ''),
	    f.declared_tier, f.tier, f.exit_code, COALESCE(f.unrun, ''),
	    COALESCE(f.refutation_model, ''), COALESCE(f.refutation_verdict, ''), COALESCE(f.refutation_tier, ''), f.refutation_exit_code, COALESCE(f.refutation_unrun, ''),
	    COALESCE(a.verdict, '')
	  FROM corral_reviews r
	  JOIN corral_findings f ON f.review_uid = r.review_uid
	  LEFT JOIN (
	    SELECT review_uid, finding_id, verdict,
	           row_number() OVER (PARTITION BY review_uid, finding_id ORDER BY ts DESC) AS rn
	    FROM corral_adjudications
	  ) a ON a.review_uid = f.review_uid AND a.finding_id = f.finding_id AND a.rn = 1
	  WHERE r.author IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("reading the reviews' party: %w", err)
	}
	defer rows.Close()
	type perReview struct {
		commit, author, co, lang string
		checked, real            int
	}
	byReview := map[string]*perReview{}
	var order []string
	for rows.Next() {
		var uid, commit, author, co, lang, declared, tier, unrun, vModel, vVerdict, vTier, vUnrun, aVerdict string
		var exit, vExit sql.NullInt64
		if err := rows.Scan(&uid, &commit, &author, &co, &lang, &declared, &tier, &exit, &unrun, &vModel, &vVerdict, &vTier, &vExit, &vUnrun, &aVerdict); err != nil {
			return nil, err
		}
		pr := byReview[uid]
		if pr == nil {
			pr = &perReview{commit: commit, author: author, co: co, lang: lang}
			byReview[uid] = pr
			order = append(order, uid)
		}
		f := findingFromRow(declared, tier, exit, unrun, vModel, vVerdict, vTier, vExit, vUnrun)
		var adj *review.Adjudicated
		if aVerdict != "" {
			adj = &review.Adjudicated{Verdict: aVerdict}
		}
		if g := review.Grade(f, adj); g.ReviewerChecked {
			pr.checked++
			if g.ReviewerHeld {
				pr.real++
			}
		}
	}
	for _, uid := range order {
		pr := byReview[uid]
		if pr.checked == 0 {
			continue
		}
		emit(pr.author, pr.co, pr.lang, pr.commit, pr.real == 0)
	}
	return obs, rows.Err()
}

// findingFromRow rebuilds what the outcome rule needs from a
// corral_findings row: the tiers, the exit codes (held by execution is an
// exit 0 ON THE RECORD — review a906b2676dca#R1), the harness markers, and
// the refutation. A row missing a column the rule reads would grade
// wrongly, so every input of OutcomeOf is read here and nowhere else.
func findingFromRow(declared, tier string, exit sql.NullInt64, unrun, vModel, vVerdict, vTier string, vExit sql.NullInt64, vUnrun string) review.Finding {
	f := review.Finding{Declared: declared, Tier: tier, Unrun: unrun}
	if exit.Valid {
		v := int(exit.Int64)
		f.ExitCode = &v
	}
	if vVerdict != "" {
		f.Refutation = &review.Refutation{Model: vModel, Verdict: vVerdict, Tier: vTier, Unrun: vUnrun}
		if vExit.Valid {
			v := int(vExit.Int64)
			f.Refutation.ExitCode = &v
		}
	}
	return f
}

// langOfScope is the language most of the shown files are in, "" when the
// scope is mixed or unknown — a review's language dimension is the scope's.
func langOfScope(files []string) string {
	counts := map[string]int{}
	for _, f := range files {
		if p, ok := lang.Detect(f); ok {
			counts[p.Name()]++
		}
	}
	best, n, tie := "", 0, false
	for name, c := range counts {
		switch {
		case c > n:
			best, n, tie = name, c, false
		case c == n:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return best
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
