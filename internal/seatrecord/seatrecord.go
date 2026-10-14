// SPDX-License-Identifier: Elastic-2.0

// Package seatrecord holds the SQL half of the one rule that keeps a seat
// which named a TOOL rather than a model (an unpinned agent seat) out of
// per-model analysis. The rule itself is agentbackend.RecordedSeatNamesAModel;
// where a store aggregates in SQL it cannot call Go, so the rule's answer is
// stamped on the row as model_unresolved where the row is made, and every
// such store filters on the predicate below — one string, imported by every
// store, so the scorecard, the shadow draw and critic precision cannot
// disagree about which rows count.
//
// A leaf package with no imports on purpose: the stores that use it
// (bugcatch, criticscore) are leaves themselves and must not pull in the
// model backends to share a constant.
package seatrecord

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
