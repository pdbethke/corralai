// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/fleet"
)

// crossSwarmClaimTTL is the TTL requested when this brain publishes a work claim.
// fleet.PublishIntent clamps it to the configured maximum (default 24h via
// maxClaimTTL / CORRALAI_CLAIM_MAX_TTL_SEC), so a generous value is safe — a
// mission that outlives the clamp is re-claimed on the next create_mission.
const crossSwarmClaimTTL = 24 * time.Hour

// crossSwarmMissionClaim runs the ADVISORY-check + PUBLISH for a repo-work mission
// when cross-swarm coordination is enabled. It is BRAIN-INTERNAL — invoked from
// create_mission, never a bee-callable tool — which is what preserves
// observe-don't-coerce (a bee can read peer claims but can never publish one).
//
//   - Observe: fleet.ActiveClaims surfaces any VERIFIED peer claim on subject as a
//     returned advisory note. It NEVER blocks mission creation (the human/brain
//     decides). Only verified, unexpired, other-brain claims are surfaced.
//   - Claim: fleet.PublishIntent publishes THIS brain's signed claim so peers can
//     observe it.
//
// Entirely best-effort: any DuckDB/publish error is logged and never affects
// mission creation. Returns the advisory note ("" when no peer claim exists).
func crossSwarmMissionClaim(opts Options, subject string, now time.Time) string {
	if !opts.CrossSwarm || subject == "" {
		return ""
	}
	// Observe: verified peer claims on this subject (advisory only — never blocks).
	var note string
	if claims, err := fleet.ActiveClaims(opts.FleetTarget, subject, opts.FleetBrainID, now); err != nil {
		log.Printf("cross-swarm: advisory ActiveClaims(%q) failed (non-fatal): %v", subject, err)
	} else if len(claims) > 0 {
		peers := make([]string, 0, len(claims))
		for _, c := range claims {
			peers = append(peers, c.BrainID)
		}
		note = fmt.Sprintf("note: %d other swarm(s) hold an active claim on %s: %s (advisory — mission not blocked)",
			len(claims), subject, strings.Join(peers, ", "))
		log.Printf("cross-swarm: %s", note)
	}
	// Claim: publish this brain's signed intent so peers observe it. Best-effort.
	if err := fleet.PublishIntent(opts.CrossSwarmKey, opts.FleetTarget, opts.FleetBrainID, "claim", subject, crossSwarmClaimTTL, now); err != nil {
		log.Printf("cross-swarm: publish claim on %q failed (non-fatal): %v", subject, err)
	}
	return note
}

type fleetClaimsIn struct {
	Subject string `json:"subject" jsonschema:"the repo or resource to query active peer claims for"`
}

// fleetClaimOut is the caller-visible projection of a fleet.Claim.
// The private fields (nonce, sig, intent_id, kind) are intentionally omitted:
// callers need to know who claims what and when, not the cryptographic internals.
type fleetClaimOut struct {
	BrainID   string  `json:"brain_id"`
	Subject   string  `json:"subject"`
	Ts        float64 `json:"ts"`
	ExpiresTs float64 `json:"expires_ts"`
}

type fleetClaimsOut struct {
	Claims []fleetClaimOut `json:"claims"`
}

// registerFleetClaims registers the fleet_claims MCP tool on s. This is gated on
// opts.CrossSwarm (both MotherDuck and the brain keypair must be configured) —
// when false the tool is absent from the MCP surface entirely.
//
// READ-ONLY: returns only cryptographically verified, non-expired claims from
// OTHER brains (own brain is always excluded). Publishing claims is brain-internal
// (never a bee-callable tool) — observe-don't-coerce.
//
// Rate limit: per-principal (verified identity), reusing the rateLimiter from
// ask_fleet. A bee that spams fleet_claims is refused after the limit without
// touching DuckDB.
func registerFleetClaims(s *mcp.Server, opts Options) {
	lim := opts.FleetClaimsRateLimit
	if lim <= 0 {
		lim = 10 // default: 10 reads/min/principal
	}
	rl := newRateLimiter(lim, time.Minute)

	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "fleet_claims",
			Description: "Query active peer-brain claims on a subject (repo URL, resource name) for cross-swarm coordination awareness. Returns only cryptographically verified, non-expired claims from other brains. Advisory read-only signal — use to avoid duplicating work another swarm is already doing.",
		},
		func(_ context.Context, req *mcp.CallToolRequest, in fleetClaimsIn) (*mcp.CallToolResult, fleetClaimsOut, error) {
			who := identity(req, "anonymous")
			if !rl.allow(who, time.Now()) {
				return nil, fleetClaimsOut{}, fmt.Errorf("rate limit: max %d fleet_claims reads/minute per principal — try again shortly", lim)
			}
			claims, err := fleet.ActiveClaims(opts.FleetTarget, in.Subject, opts.FleetBrainID, time.Now())
			if err != nil {
				return nil, fleetClaimsOut{}, fmt.Errorf("fleet_claims: %w", err)
			}
			out := make([]fleetClaimOut, 0, len(claims))
			for _, c := range claims {
				out = append(out, fleetClaimOut{
					BrainID:   c.BrainID,
					Subject:   c.Subject,
					Ts:        c.Ts,
					ExpiresTs: c.ExpiresTs,
				})
			}
			return nil, fleetClaimsOut{Claims: out}, nil
		},
	)
}
