// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/models"
)

// rankEvidence is one store's worth of already-attributed outcomes, plus a
// human-readable name for WHERE they came from. Every ranking line printed
// below is traceable to this one string, because a number whose source the
// reader cannot name is not evidence.
type rankEvidence struct {
	Obs    []modelrank.Observation
	Source string
}

// rankLoader reads evidence for a DSN. An EMPTY dsn means the local bugcatch
// ledger (the default source); a non-empty one is a pushed warehouse. The two
// failure modes are deliberately kept apart by the caller: a warehouse the
// operator NAMED and corral could not reach is a refusal, never a silent
// fallback to a different body of evidence.
type rankLoader func(dsn string) (rankEvidence, error)

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

const modelsUsage = `usage: corral models rank [--db <dsn>] [--seat <role>] [--lang <name>] [--min-runs N] [--json]

  Rank the models that have sat in each seat by what corral's OWN recorded
  evidence says about them — a different metric per seat, because the seats do
  different jobs.

  This is DISCLOSURE, NOT SELECTION. It prints a table. It writes no config,
  changes no default, and feeds no router: corral has no default models, and a
  ranking that quietly staffed a seat would put one back.`

func runModelsRank(args []string, repoRoot string, load rankLoader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models rank", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("db", "", "a pushed warehouse to read instead of the local bugcatch ledger: a DuckDB file path or an `md:<db>` MotherDuck DSN. Unreachable is a refusal, never a quiet fall back to a different body of evidence")
	seat := fs.String("seat", "", "rank only this seat: goal-deriver, mutant-generator, test-writer or test-critic")
	lang := fs.String("lang", "", "rank only this language — needs evidence that records one (the local bugcatch ledger does not; a pushed warehouse does)")
	minRuns := fs.Int("min-runs", modelrank.DefaultMinRuns, "the evidence floor (default 5): a model with fewer observations in a seat is still PRINTED, with its real numbers, but marked insufficient and never preferred")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, modelsUsage)
		fmt.Fprintln(stderr, "\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if s := strings.TrimSpace(*seat); s != "" && !knownSeatName(s) {
		fmt.Fprintf(stderr, "corral models rank: --seat %q is not a seat — it must be one of: %s\n", s, strings.Join(rankSeats, ", "))
		return 2
	}

	target := strings.TrimSpace(*dsn)
	ev, err := load(target)
	if err != nil {
		fmt.Fprintln(stderr, "corral models rank:", err)
		if target != "" {
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
		_ = enc.Encode(rep)
		return 0
	}
	printRankReport(rep, reg, stdout)
	return 0
}

var rankSeats = []string{modelrank.SeatGoalDeriver, modelrank.SeatMutantGenerator, modelrank.SeatTestWriter, modelrank.SeatTestCritic}

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

func printRankReport(rep modelrank.Report, reg *models.Registry, w io.Writer) {
	fmt.Fprintln(w, "corral models rank — what corral's own recorded evidence says about each seat.")
	switch rep.Mode {
	case modelrank.ModeRegistry:
		fmt.Fprintf(w, "mode: registry — ranking the %d model(s) %s declares, plus any others the evidence carries.\n", reg.Len(), reg.Source)
	default:
		fmt.Fprintln(w, "mode: evidence — this project declares no model registry, so the concrete models found in the evidence are ranked instead.")
	}
	fmt.Fprintf(w, "evidence: %s\n", rep.Source)
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
	ev := rankEvidence{Source: "the local bug-catching ledger (the same rows `corral scorecard` reports)"}
	if store != nil {
		rows, err := store.Observations(ctx)
		if err != nil {
			return rankEvidence{}, err
		}
		for _, o := range rows {
			// Shadow seats are a decorrelation experiment running alongside
			// the real one; their outcomes never reached a verdict, so they
			// are not evidence about a seat's work.
			if o.Shadow {
				continue
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
func warehouseRankEvidence(db *sql.DB, dsn string) (rankEvidence, error) {
	ev := rankEvidence{Source: "the pushed warehouse " + dsn + " (corral_audits)"}
	rows, err := db.Query(`SELECT COALESCE(lang, ''), COALESCE(models_by_role, ''), COALESCE(scan_id, 0),
		COALESCE(path, ''), COALESCE(survivors, 0), COALESCE(proven_missed, 0),
		COALESCE(mutants_planted, 0), COALESCE(mutants_graded, 0), COALESCE(mutants_invalid, 0)
		FROM corral_audits WHERE disposition = 'audited'`)
	if err != nil {
		return rankEvidence{}, fmt.Errorf("reading corral_audits from %s: %w", dsn, err)
	}
	defer rows.Close()
	for rows.Next() {
		var lang, byRole, path string
		var scanID int64
		var survivors, proven, planted, graded, invalid int
		if err := rows.Scan(&lang, &byRole, &scanID, &path, &survivors, &proven, &planted, &graded, &invalid); err != nil {
			return rankEvidence{}, fmt.Errorf("scanning corral_audits from %s: %w", dsn, err)
		}
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
		run := fmt.Sprintf("%d", scanID)
		for role, model := range seats {
			if strings.TrimSpace(model) == "" || strings.EqualFold(model, "off") {
				continue
			}
			o := modelrank.Observation{Model: model, Role: role, Lang: lang, Run: run}
			switch role {
			case modelrank.SeatTestWriter:
				o.Catches, o.Opportunities = proven, survivors
			case modelrank.SeatMutantGenerator:
				o.MutantsPlanted, o.MutantsGraded, o.MutantsInvalid, o.MutantsSurvived = planted, graded, invalid, survivors
			}
			ev.Obs = append(ev.Obs, o)
		}
	}
	return ev, rows.Err()
}

// defaultRankLoader is the wiring `corral models rank` uses in production:
// the local bug-catching ledger (plus the local adjudication store) by
// default, or an ATTACHed warehouse when the operator names one.
//
// Every open here is READ-ONLY. This command reports on runs that already
// happened; it must not be able to alter one.
func defaultRankLoader(dsn string) (rankEvidence, error) {
	ctx := context.Background()
	if strings.TrimSpace(dsn) != "" {
		db, err := attachWarehouse(dsn, true)
		if err != nil {
			return rankEvidence{}, fmt.Errorf("cannot read the warehouse %s: %w — check the path or `md:` DSN (and, for MotherDuck, that motherduck_token is set); no ranking was produced, and nothing fell back to a different body of evidence", dsn, err)
		}
		defer db.Close()
		return warehouseRankEvidence(db, dsn)
	}
	store, err := bugcatch.Open(localBugCatchDBPath())
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
	var critic *criticscore.Store
	if cs, cerr := criticscore.Open(localCriticScoreDBPath()); cerr == nil {
		critic = cs
		defer func() { _ = cs.Close() }()
	}
	return bugcatchRankEvidence(ctx, store, critic)
}
