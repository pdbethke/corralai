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

// backendForVendor maps agentbackend.VendorOf's answer onto the MODEL_BACKEND
// label FromEnv switches on. They are deliberately not the same vocabulary —
// VendorOf says who makes the model ("google"), MODEL_BACKEND names the
// backend implementation ("gemini") — so the translation lives in exactly one
// place rather than being re-guessed per caller.
func backendForVendor(vendor string) string {
	switch vendor {
	case "google":
		return "gemini"
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	default:
		return ""
	}
}

// soleAssignedCloudModel reports the single cloud vendor every assigned seat
// uses, plus one model that names it — or "", "" when the seats span more than
// one vendor, or none resolves to a cloud vendor at all.
//
// "More than one vendor" is deliberately left alone: that is the cross-vendor
// critic design (Claude writer + Gemini critic), which localChatterFor already
// routes per-role from the base backend. Only the unambiguous case — every
// seat on one vendor — is safe to infer a whole-run backend from.
// Only the GRADED seats are considered. The challenger seat
// (RoleMutantGeneratorShadow) is a measurement seat that never gates a verdict
// and carries a Claude default, so letting it veto the inference would mean an
// all-Gemini scan could never infer anything. It still needs its own credential
// — resolveAuditRoles checks it separately so the error can name that seat
// rather than blaming the whole run.
func soleAssignedCloudModel(assign advpool.RoleAssignment) (vendor, model string) {
	for _, role := range []string{
		advpool.RoleMutantGenerator,
		advpool.RoleTestWriter,
		advpool.RoleTestCritic,
	} {
		m := strings.TrimSpace(assign[role])
		// "off" is a disabled seat (the critic), not a model, and an empty
		// assignment never reached a default. Neither says anything about
		// which vendor the run needs.
		if m == "" || m == "off" {
			continue
		}
		v := agentbackend.VendorOf(m)
		if v == "" {
			// A local/ollama model name. Inferring a cloud backend from a set
			// that includes one would point a local seat at a cloud endpoint.
			return "", ""
		}
		if vendor == "" {
			vendor, model = v, m
			continue
		}
		if v != vendor {
			return "", ""
		}
	}
	return vendor, model
}

// localChatterFor builds the role→backend router for a real run: the base
// backend from FromEnv() (MODEL_BACKEND-selected), switched to each role's
// assigned model via WithModel when the backend supports it. A single ANTHROPIC
// key + the anthropic backend serves all three Claude models this way.
//
// Cross-vendor critic (design 2026-07-19): decorrelation is strongest when the
// test-critic runs on a DIFFERENT VENDOR than the writer/mutant-generator, not
// just a different model on the same vendor. When the operator hasn't pinned
// an explicit MODEL_BACKEND (the default direct-Claude path) AND the critic's
// model resolves to a cloud vendor other than the base backend's, this builds
// a dedicated critic backend via agentbackend.ForModel ONCE up front and
// routes RoleTestCritic to it; every other role keeps the base+WithModel path
// unchanged. An explicit MODEL_BACKEND (openai/openrouter/ollama — an
// operator pointing every role at one endpoint on purpose) is never
// disturbed: all roles including the critic keep today's single-backend
// WithModel behavior.
//
// Fails closed: if a cross-vendor critic is requested but its vendor's key is
// missing, this returns the actionable error from ForModel instead of
// silently falling back to the base backend — the caller must refuse to
// start the run, not fail mid-run.
// meter, when non-nil, accumulates every seat's reported token usage across the
// whole run — including the cross-vendor seats, which each get their own
// backend and would otherwise be counted by nobody.
func localChatterFor(assign advpool.RoleAssignment, meter *agentbackend.UsageMeter) (func(role string) agentworker.Chatter, error) {
	base := agentbackend.FromEnv()
	sw, canSwitch := base.(agentbackend.ModelSwitcher)
	bv := baseVendor()

	// Resolve EVERY seat, not just the critic. This used to cross-route the
	// test-critic alone, which quietly made the product's central claim
	// unreachable: the mutant-generator and the test-writer were pinned to
	// whatever single backend the run started on, so "one model plants the
	// faults, a DIFFERENT one writes the killing test" could only ever mean
	// two models from the same vendor. Asking for a Gemini generator and a
	// Claude critic did not fail loudly either — the Gemini name was sent to
	// Anthropic's endpoint and came back 404.
	//
	// It also stranded the scorecard. The learning loop measures which model
	// actually catches bugs IN WHICH ROLE, and routing could act on that for
	// one role in three; a measurement computed and then not actable is the
	// same shape as a measurement computed and discarded.
	//
	// Resolved UP FRONT so a missing credential refuses the run before any
	// jail or store is opened, naming the role the operator has to fix.
	perRole := map[string]agentworker.Chatter{}
	if bv != "" {
		for _, role := range []string{
			advpool.RoleMutantGenerator,
			advpool.RoleTestWriter,
			advpool.RoleTestCritic,
			advpool.RoleMutantGeneratorShadow,
		} {
			model := strings.TrimSpace(assign[role])
			if model == "" {
				continue
			}
			v := agentbackend.VendorOf(model)
			if v == "" || v == bv {
				continue // unrecognized, or already this backend's vendor
			}
			cb, err := agentbackend.ForModel(model)
			if err != nil {
				return nil, fmt.Errorf("cross-vendor %s: %w", role, err)
			}
			perRole[role] = agentbackend.AsChatterMetered(cb, meter)
		}
	}

	return func(role string) agentworker.Chatter {
		if c, ok := perRole[role]; ok {
			return c
		}
		if model := assign[role]; canSwitch && model != "" {
			return agentbackend.AsChatterMetered(sw.WithModel(model), meter)
		}
		return agentbackend.AsChatterMetered(base, meter)
	}, nil
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
// the operator and spend on a vendor they did not choose.
func baseVendor() string {
	switch strings.TrimSpace(os.Getenv("MODEL_BACKEND")) {
	case "", "anthropic", "claude":
		return "anthropic"
	case "gemini":
		return "google"
	case "openai":
		return "openai"
	default:
		return ""
	}
}
