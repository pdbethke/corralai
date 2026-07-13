// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/fleet"
)

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
