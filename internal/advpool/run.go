// SPDX-License-Identifier: Elastic-2.0

// Package advpool is the pure driver for the adversarial testing pool: a
// run definition, roles-as-data, and the DAG builder that turns a run into
// queue.TaskSpecs. It has no queue/jail/brain wiring of its own — callers
// enqueue the returned specs and drive completions themselves (Phase 5).
package advpool

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

	// ShadowModel is the CHALLENGER generator model. When set, every shard is
	// attacked a second time by this model for a region-controlled head-to-head.
	// Shadow mutants are parsed, scored, and recorded, but NEVER feed the
	// verdict — the exam's difficulty stays set by the primary model alone, so
	// certification means exactly what it meant before. "" disables.
	ShadowModel string

	// Matrix opts a run into the tests×mutants matrix (swarm slice 5): after
	// pool-adequacy, the driver enumerates the dev suite's individual tests
	// and scores each ALONE against the run's own mutants, then drives critic
	// finding adjudication (refute + confirm) off that per-test data instead
	// of the single re-scored test the pre-matrix path used. false (the
	// default) preserves today's single-test critic auto-refute path byte-
	// for-byte — every existing run/test keeps behaving exactly as before.
	Matrix bool
}

// RoleAssignment maps a role name (Role.Name) to the gate-earned model that
// should run it, e.g. from StaffingManager off the leaderboard.
type RoleAssignment map[string]string
