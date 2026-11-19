// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// isAgentSeat reports whether spec names a coding-agent seat (`codex`,
// `claude-code:<model>`, any CORRALAI_AGENT_<NAME>) rather than a model name.
// Every site that treats a seat's name as an API model asks this first.
func isAgentSeat(spec string) bool {
	_, _, ok := agentbackend.AgentSeat(spec)
	return ok
}

// localChatterFor builds the role→backend router for a real run from the
// RESOLVED seat set: every assigned role's backend is agentbackend.Build of the
// seat resolveSeats decided for it, so no door here re-guesses a vendor from a
// model's name, consults MODEL_BACKEND, or falls back to a process-wide base
// backend. The routing facts (provider, wire, endpoint, key) were fixed once,
// up front, in one stated order; this function only constructs.
//
// Every seat is built, not just the critic. This used to cross-route the
// test-critic alone, which quietly made the product's central claim
// unreachable: the mutant-generator and the test-writer were pinned to
// whatever single backend the run started on, so "one model plants the
// faults, a DIFFERENT one writes the killing test" could only ever mean
// two models from the same vendor. It also stranded the scorecard: the
// learning loop measures which model actually catches bugs IN WHICH ROLE, and
// routing could act on that for one role in three; a measurement computed and
// then not actable is the same shape as a measurement computed and discarded.
//
// Built UP FRONT so a missing credential refuses the run before any jail or
// store is opened, naming the role the operator has to fix. A role with NO
// seat gets no chatter, so its task fails loudly ("no model backend for role")
// rather than being answered by a backend nobody chose.
//
// --local-endpoint placements are already merged into the seats
// (applyLocalEndpoints), which is also where a cloud or agent seat given one is
// refused.
//
// meters, when non-nil, is ONE UsageMeter PER ROLE — built by
// auditRoleMeters — that this seat's reported token usage is recorded into,
// including the cross-vendor seats, which each get their own backend and
// would otherwise be counted by nobody. A role absent from meters (or a nil
// map) records nothing, the same as a nil single meter always has.
func localChatterFor(seats seatSet, meters map[string]*agentbackend.UsageMeter, budget *agentbackend.TokenBudget) (func(role string) agentworker.Chatter, error) {
	perRole := map[string]agentworker.Chatter{}
	for _, role := range []string{
		advpool.RoleMutantGenerator,
		advpool.RoleTestWriter,
		advpool.RoleTestCritic,
		advpool.RoleMutantGeneratorShadow,
		advpool.RoleTestWriterShadow,
	} {
		rs, ok := seats.byRole(role)
		if !ok {
			continue
		}
		// No per-call timeout exists for certify's API seats, so an agent
		// seat's own 15m default applies.
		b, err := agentbackend.Build(rs, agentbackend.SeatOptions{})
		if err != nil {
			if rs.Kind == "agent" {
				return nil, fmt.Errorf("agent seat %s: %w", role, err)
			}
			// Names the flag and the model, so the operator sees which seat
			// needs the fix. A challenger is a measurement seat that never
			// gates a verdict, so its refusal also says how to drop it: the
			// old message blamed "your Claude key" for a seat the operator
			// could not see.
			if role == advpool.RoleMutantGeneratorShadow || role == advpool.RoleTestWriterShadow {
				return nil, fmt.Errorf("the challenger seat (--%s) is %q and cannot be built: %w. It only records a comparison and never gates the verdict — pass --%s off to drop it, or give it a model whose provider you hold a key for", rs.Flag, rs.Spec, err, rs.Flag)
			}
			return nil, fmt.Errorf("seat %s (--%s %q): %w", role, rs.Flag, rs.Spec, err)
		}
		if rs.Kind == "agent" {
			if err := capCanSee(budget, role, rs.Spec, b); err != nil {
				return nil, err
			}
		}
		perRole[role] = agentbackend.AsChatterBudgeted(b, meters[role], budget)
	}
	return func(role string) agentworker.Chatter {
		if c, ok := perRole[role]; ok {
			return c
		}
		return nil
	}, nil
}

// capCanSee refuses a seat a --max-tokens cap cannot bound: one whose
// backend reports no token usage (agentbackend.Metered — an agent seat on the
// `text` wire). A cap charged nothing by a seat lets the run spend past the
// number the operator typed, so it is refused HERE, while the router is being
// built — before any jail, store or model call — and never discovered from a
// bill. The rejected alternative was to cap the metered seats and disclose
// the rest, which is exactly a run exceeding its cap (spec 2026-10-13 agent
// seats, section 4). nil budget = no cap = nothing to refuse.
//
// Only an agent seat can be unmetered today, so only that branch of
// localChatterFor calls this; Metered answers true for every API backend,
// and the question is still asked of the backend, never of the seat's name.
func capCanSee(budget *agentbackend.TokenBudget, role, spec string, b agentbackend.Backend) error {
	if budget == nil || agentbackend.Metered(b) {
		return nil
	}
	name, _, _ := agentbackend.AgentSeat(spec)
	return fmt.Errorf("--max-tokens: seat %s (%s) reports no token usage, so the cap cannot see it — drop the cap, or use an agent whose wire reports usage (%s)", role, spec, agentbackend.AgentWireKey(name))
}
