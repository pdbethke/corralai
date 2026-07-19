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
}

// RoleAssignment maps a role name (Role.Name) to the gate-earned model that
// should run it, e.g. from StaffingManager off the leaderboard.
type RoleAssignment map[string]string
