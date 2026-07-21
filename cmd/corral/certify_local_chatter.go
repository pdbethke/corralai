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

// onDefaultClaudePath reports whether the run is on the default direct-Claude
// path — MODEL_BACKEND unset, "anthropic", or "claude" — vs an operator-pinned
// backend. It reads MODEL_BACKEND fresh, so after runCertifyLocal defaults an
// unset MODEL_BACKEND to "anthropic" this still reports true. Both the
// provider-key gate and the cross-vendor router consult it, so the policy lives
// in one place.
func onDefaultClaudePath() bool {
	b := strings.TrimSpace(os.Getenv("MODEL_BACKEND"))
	return b == "" || b == "anthropic" || b == "claude"
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
func localChatterFor(assign advpool.RoleAssignment) (func(role string) agentworker.Chatter, error) {
	base := agentbackend.FromEnv()
	sw, canSwitch := base.(agentbackend.ModelSwitcher)

	var criticChatter agentworker.Chatter
	if onDefaultClaudePath() && canSwitch {
		criticModel := assign[advpool.RoleTestCritic]
		// On the default path the base backend is definitively anthropic
		// (onDefaultClaudePath gates that), so the base vendor is "anthropic"
		// regardless of the base backend's default model string — do NOT
		// derive it from sw.Model() (which is the local AGENT_MODEL default,
		// vendor ""). Cross-route only when the critic resolves to a
		// recognized cloud vendor that is NOT anthropic; a same-vendor Claude
		// critic keeps the base+WithModel path below.
		if v := agentbackend.VendorOf(criticModel); criticModel != "" && v != "" && v != "anthropic" {
			cb, err := agentbackend.ForModel(criticModel)
			if err != nil {
				return nil, fmt.Errorf("cross-vendor critic: %w", err)
			}
			criticChatter = agentbackend.AsChatter(cb)
		}
	}

	return func(role string) agentworker.Chatter {
		if role == advpool.RoleTestCritic && criticChatter != nil {
			return criticChatter
		}
		if model := assign[role]; canSwitch && model != "" {
			return agentbackend.AsChatter(sw.WithModel(model))
		}
		return agentbackend.AsChatter(base)
	}, nil
}
