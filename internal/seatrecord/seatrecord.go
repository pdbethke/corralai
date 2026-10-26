// SPDX-License-Identifier: Elastic-2.0

// Package seatrecord holds the one rule that keeps a seat which named a TOOL
// rather than a model (an unpinned agent seat) out of per-model analysis:
// RecordedSeatNamesAModel, and its SQL half. agentbackend.RecordedSeatNamesAModel
// is a wrapper over the Go half, which lives here so the ledger's Summary —
// compiled to WASM, where the model backends cannot go — applies the same
// rule rather than a second copy. Where a store aggregates in SQL it cannot
// call Go, so the rule's answer is
// stamped on the row as model_unresolved where the row is made, and every
// such store filters on the predicate below — one string, imported by every
// store, so the scorecard, the shadow draw and critic precision cannot
// disagree about which rows count.
//
// A leaf package with no corral imports on purpose: the stores that use it
// (bugcatch, criticscore) are leaves themselves and must not pull in the
// model backends to share a constant.
package seatrecord

import "strings"

// UnresolvedColumn is the column the rule's answer is stored in, and its DDL
// for an additive migration. NULL on every row written before it existed —
// API seats, which name their model — so a legacy row reads as resolved. That is safe because certify could not
// seat an agent before this column existed: a NULL row cannot be an unpinned
// agent seat that went unmarked.
const (
	UnresolvedColumn = "model_unresolved"
	UnresolvedDDL    = UnresolvedColumn + " BOOLEAN"
)

// ResolvedOnly is the WHERE predicate every per-model SQL reader applies.
const ResolvedOnly = "NOT COALESCE(" + UnresolvedColumn + ", false)"

// builtinAgents are the agents corral defines itself (agentbackend's
// builtinAgents keys, held equal by agentbackend's
// TestSeatrecordKnowsEveryBuiltinAgent). Only the names live here: an older
// record carries no tool, and its first word is all that says an agent sat.
var builtinAgents = map[string]bool{"claude-code": true, "codex": true, "antigravity": true}

// BuiltinAgentNames lists builtinAgents, for the parity test.
func BuiltinAgentNames() []string {
	out := make([]string, 0, len(builtinAgents))
	for n := range builtinAgents {
		out = append(out, n)
	}
	return out
}

// RecordedSeatNamesAModel reads a RECORDED seat — its spec and the tool
// beside it — and never the environment, so a historical row means what it
// meant when it was written. A recorded tool says an agent sat, and an agent
// seat names a model only when pinned (`agent:model`). A record with no tool
// is an API seat, unless its first word is a built-in agent (an entry from
// before the tool was recorded).
//
// This is the one rule every per-model analysis applies — `corral models
// rank`, the bug-catching scorecard and the ledger's Summary: a row whose
// seat names no model is not evidence about a model.
func RecordedSeatNamesAModel(spec, tool string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false
	}
	name, model, _ := strings.Cut(spec, ":")
	if strings.TrimSpace(tool) != "" {
		return strings.TrimSpace(model) != ""
	}
	if builtinAgents[name] {
		return strings.TrimSpace(model) != ""
	}
	return true
}
