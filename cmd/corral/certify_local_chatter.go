// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
	"strings"

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

// backendPinned reports whether the operator pinned a base backend at all.
// UNSET is not a vendor: it is the absence of a choice, and FromEnv answers it
// with the ollama default. Telling the two apart is what lets an unpinned run
// route every cloud-named seat by name, while an explicitly pinned gateway
// keeps its hands-off guarantee.
func backendPinned() bool {
	return strings.TrimSpace(os.Getenv("MODEL_BACKEND")) != ""
}

// baseVendor is the vendor the process-wide backend actually talks to, as a
// VendorOf-comparable string — the thing a per-role model must DIFFER from to
// need its own backend.
//
// Returns "" for a gateway the operator pinned deliberately (openrouter,
// ollama, or anything unrecognized). Those front many vendors behind one
// endpoint, so a "claude-" model name there is not an Anthropic call and must
// not be re-routed to Anthropic: an explicit MODEL_BACKEND means every seat
// goes to that endpoint on purpose, and cross-routing would silently overrule
// the operator and spend on a vendor they did not choose. Callers pair it with
// backendPinned to tell that deliberate gateway apart from an unset backend,
// which also has no cloud vendor but chose nothing.
//
// Unset used to answer "anthropic" here, a straggler from the retired "unset
// means Claude" default. It disagreed with FromEnv (which builds ollama) in
// exactly one reachable case — a MIXED-vendor run, where resolveAuditRoles
// cannot infer a single backend and so leaves MODEL_BACKEND unset. The Claude
// seat then matched this phantom "anthropic" base, skipped cross-routing, and
// was handed to the OLLAMA backend, which answered 404 for a model it had
// never heard of. A Gemini generator with a Claude critic — the documented
// cross-vendor shape — could not run at all.
func baseVendor() string {
	switch strings.TrimSpace(os.Getenv("MODEL_BACKEND")) {
	case "anthropic", "claude":
		return "anthropic"
	case "gemini":
		return "google"
	case "openai":
		// MODEL_BACKEND=openai with OPENAI_BASE_URL set is the common way to
		// name an OpenAI-COMPATIBLE gateway (LiteLLM, vLLM, a proxy) that
		// serves any model name it is handed — and there a gemini-* critic
		// is a call to that gateway, not to Google. Until the 2026-09 router
		// review this happened to WORK by accident: the cross-vendor path
		// was taken, and the Gemini backend then followed OPENAI_BASE_URL
		// (with the Google key falling back to the OpenAI one), landing on
		// the gateway anyway. That fallback now applies only to the pinned
		// MODEL_BACKEND=gemini door, so the gateway reading has to be made
		// here, on purpose: a custom base URL is a gateway; the vendor's own
		// endpoint is the vendor.
		if strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")) != "" {
			return ""
		}
		return "openai"
	default:
		return ""
	}
}
