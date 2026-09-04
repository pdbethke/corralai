// SPDX-License-Identifier: Elastic-2.0

// Package advpool is the pure driver for the adversarial testing pool: a
// run definition, roles-as-data, and the DAG builder that turns a run into
// queue.TaskSpecs. It has no queue/jail/brain wiring of its own — callers
// enqueue the returned specs and drive completions themselves (Phase 5).
package advpool

import (
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// RunSpec is one adversarial-pool run: the code under review PLUS the
// developer's own tests for it. The pool's central question is not "does
// the code pass its tests" but "do the dev's tests actually test anything" —
// so DevTestPath/DevTestCode are first-class input, not an afterthought.
type RunSpec struct {
	Repo        string
	Commit      string
	Goal        string
	CodePath    string
	Code        string
	DevTestPath string
	DevTestCode string
	TestCmd     string
	NMutants    int

	// Selection narrows TestCmd to the tests that EXECUTED CodePath, from
	// evidence recorded once per scan (lang.TestSelector). The zero value
	// is the whole suite. When Method is set and Tests is empty the file is
	// UNCOVERED: no test executes it, the dev pass runs nothing, and the
	// verdict says so instead of printing a 0.00 nobody measured.
	Selection lang.Selection
	// Concurrency carries how many private trees the workspace substrate's
	// probe granted this file (or why it granted only one), from
	// cmd/corral/certify_local.go's buildJailWiring — see
	// advpool.Concurrency's doc: Trees < 1 is the explicit "not recorded"
	// state, never rounded up to a 1 nothing measured.
	Concurrency Concurrency
	Lang        string // "" defaults to "go" at render time (back-compat)

	// ImportPath is the PRE-COMPUTED result of the run's language plugin's
	// ImportPath(CodePath, exists) — the real, package-qualified import for
	// CodePath, when the caller had a real checkout on disk to derive it
	// from (see cmd/corral/certify_local.go's prepareAuditJail, the only
	// caller with filesystem access). "" means either the language needs no
	// such correction (every plugin but python) or a python file whose
	// import path could not be established (no checkout available, e.g. the
	// brain/MCP path in internal/brain/advpool.go, which never sets this —
	// it has no filesystem to consult and must not guess). renderTestWriter
	// turns this into the test-writer's per-task ImportNote — see
	// internal/lang.Plugin.ImportNote and roles.renderTestWriterWithRepair.
	ImportPath string

	// MaxShards bounds how many mutant-generator seats fan out across the
	// file's top-level symbols. 0 or 1 means unsharded (one generator, whole
	// file — the pre-slice-2 behavior, byte-identical prompt). It bounds
	// PARALLELISM only: every symbol is probed regardless (see ShardSymbols).
	// NMutants is the PER-SHARD budget, so total mutants scale with width.
	MaxShards int

	// MutantRoundCost is a MEASURED wall-clock cost of grading one round of
	// mutants — Concurrency.Trees of them at once — on this file's own
	// selected command: half the workspace probe's duration (the probe runs
	// the baseline in every tree, then the canary, so two rounds). 0 when
	// nothing measured it (a pool of one probes nothing; the jail substrate
	// has no pool), and then PlanShards fits nothing. Deadline is the
	// per-file --timeout the run must converge inside. Together they let
	// PlanShards fit the budget to the clock — see MutantBudget.Fitted.
	MutantRoundCost time.Duration
	Deadline        time.Duration

	// ShadowModel is the CHALLENGER generator model. When set, every shard is
	// attacked a second time by this model for a region-controlled head-to-head.
	// Shadow mutants are parsed, scored, and recorded, but NEVER feed the
	// verdict — the exam's difficulty stays set by the primary model alone, so
	// certification means exactly what it meant before. "" disables.
	ShadowModel string

	// ShadowWriterModel is the CHALLENGER writer model. When set, a second
	// model authors its own suite against the SAME mutant set the primary
	// writer faced, and both seats' per-mutant outcomes are recorded for a
	// correlation measurement.
	//
	// OFF unless named, for the same reason ShadowModel is: it is a
	// measurement seat that never gates a verdict, so the cost of it being off
	// by default is nothing but a comparison nobody asked for — whereas a
	// default would silently spend tokens and force a vendor.
	ShadowWriterModel string

	// WriterMode is HOW the writer seat attacks the survivors:
	// WriterModePerSurvivor fans out into one call per survivor, each proven
	// alone against its own mutant; WriterModeBatched makes ONE call carrying
	// every survivor as a diff and proves the resulting file against all of
	// them at once.
	//
	// EMPTY MEANS BATCHED, deliberately. Per-survivor is the CLI's default —
	// `certify --repo` and `certify --local` both resolve it before they get
	// here — but a RunSpec built by any other caller (the brain, an
	// in-package test) has never asked for a fan-out and must keep the shape
	// it has always had. A zero value that silently changed how many model
	// calls a run makes, and what its verdict discloses, would be a default
	// nobody chose.
	//
	// The distinction is disclosed on the verdict (Verdict.WriterMode) rather
	// than left implicit: the two modes cost differently and prove
	// differently — per-survivor proves each survivor with a test written for
	// IT, batched proves whichever survivors one shared file happens to kill
	// — so two verdicts earned under different modes are not the same
	// measurement, and a reader must be able to tell which one they hold.
	WriterMode string

	// Matrix opts a run into the tests×mutants matrix (swarm slice 5): after
	// pool-adequacy, the driver enumerates the dev suite's individual tests
	// and scores each ALONE against the run's own mutants, then drives critic
	// finding adjudication (refute + confirm) off that per-test data instead
	// of the single re-scored test the pre-matrix path used. false (the
	// default) preserves today's single-test critic auto-refute path byte-
	// for-byte — every existing run/test keeps behaving exactly as before.
	Matrix bool

	// PresetMutants REPLACES generation: when non-nil the run seeds no
	// mutant-generator seat (nor a shadow-generator one), spends no model
	// call on generation, and grades the dev suite against exactly these
	// mutants, in this order.
	//
	// It exists because the exam is otherwise re-drawn every run. Mutants are
	// authored by a MODEL, and the same model on the same unchanged file
	// produces a different set each time — generator variance on one file is
	// larger than most effects a comparison would be trying to measure. So
	// two runs of "the same" audit are not two samples of one measurement;
	// they are two different exams, and their kill rates are not comparable.
	// Pinning the set makes them comparable, which is the only way a claim
	// about anything ELSE that changed (concurrency, a writer model, a
	// substrate) can be proven rather than asserted.
	//
	// nil — the default, and every pre-existing caller — generates as before.
	PresetMutants []adequacy.Mutant

	// SelectionDuration and PoolDuration are the two phases of a file's audit
	// the driver CANNOT time, handed to it by the caller that did.
	//
	// Selection is the scan's single instrumented coverage run
	// (cmd/corral's collectSelection): it happens once for the whole repo
	// and is SHARED BY EVERY FILE of it, before any run is started. It rides
	// every RunSpec so a file's readout can name each phase of its audit, but
	// it is never added into that file's Timing.Total — the scan header
	// records it once, which is the copy a cost query sums.
	//
	// PoolDuration, by contrast, is per FILE: the workspace substrate's copy of the
	// checkout and its concurrency probe (adequacy.Disclosure.CopyDuration +
	// ProbeDuration), which happen while the jail wiring is being built —
	// again before the driver exists.
	//
	// They ride the spec rather than being re-derived downstream so there is
	// ONE measurement of each, taken where the work actually happened. Zero
	// (every caller that does not set them, including the brain/MCP path)
	// means the phase did not run, and every reader prints and stores it as
	// unknown rather than as a phase that was free.
	SelectionDuration time.Duration
	PoolDuration      time.Duration
}

// RoleAssignment maps a role name (Role.Name) to the gate-earned model that
// should run it, e.g. from StaffingManager off the leaderboard.
type RoleAssignment map[string]string
