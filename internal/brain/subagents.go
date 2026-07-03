// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/rolemodel"
)

type spawnSubagentIn struct {
	Name         string  `json:"name" jsonschema:"a short child name (e.g. tester, planner-1); the full id becomes <you>/<name>"`
	Role         string  `json:"role,omitempty" jsonschema:"assigned role (e.g. tester, deployer) — drives its role-specific skill set"`
	Task         string  `json:"task,omitempty" jsonschema:"what this subagent should do"`
	Program      string  `json:"program,omitempty"`
	Model        string  `json:"model,omitempty"`
	Parent       string  `json:"parent,omitempty" jsonschema:"parent to spawn under (default: you); must be within your own namespace"`
	OutOfProcess bool    `json:"out_of_process,omitempty" jsonschema:"true => also mint a delegation token so a SEPARATE process can act as this subagent"`
	TTLSeconds   float64 `json:"ttl_seconds,omitempty" jsonschema:"delegation token lifetime (default 3600)"`
}
type spawnSubagentOut struct {
	Name    string `json:"name"` // full hierarchical id (<parent>/<name>)
	Parent  string `json:"parent"`
	Role    string `json:"role,omitempty"`
	Token   string `json:"token,omitempty"`   // delegation token (only when out_of_process)
	Model   string `json:"model,omitempty"`   // policy-assigned model; empty = inherit parent/default
	Backend string `json:"backend,omitempty"` // policy-assigned backend; empty = inherit parent/default
}

type despawnSubagentIn struct {
	Name string `json:"name" jsonschema:"the subagent's full id to retire"`
}
type listSubagentsIn struct {
	Parent string `json:"parent,omitempty" jsonschema:"whose children to list (default: you)"`
}
type listSubagentsOut struct {
	Subagents []coord.Agent `json:"subagents"`
}

// spawnDepthOf is the number of segments in a hierarchical agent name
// ("a/b/c" => 3). The spawn budget caps this to stop recursive fork-bombs.
func spawnDepthOf(fullName string) int {
	if fullName == "" {
		return 0
	}
	return strings.Count(fullName, "/") + 1
}

// budgetDecision returns a refusal error if spawning a child named fullName under
// parent would breach the budget, given the parent's current child count and the
// principal's current live-agent count. nil => allowed. Pure (no I/O) for testing.
func budgetDecision(b SpawnBudget, fullName, parent string, parentChildren, principalLive int) error {
	if d := spawnDepthOf(fullName); d >= b.MaxSpawnDepth {
		return fmt.Errorf("spawn refused: depth %d at or beyond the spawn-depth limit %d (deepest allowed is %d)", d, b.MaxSpawnDepth, b.MaxSpawnDepth-1)
	}
	if parentChildren >= b.MaxChildrenPerParent {
		return fmt.Errorf("spawn refused: parent %q already has %d children (max %d)", parent, parentChildren, b.MaxChildrenPerParent)
	}
	if principalLive >= b.MaxAgentsPerPrincipal {
		return fmt.Errorf("spawn refused: principal at %d live agents (max %d)", principalLive, b.MaxAgentsPerPrincipal)
	}
	return nil
}

// actorOr returns principal if non-empty, otherwise fallback (avoids logging an
// empty actor in dev mode).
func actorOr(principal, fallback string) string {
	if principal != "" {
		return principal
	}
	return fallback
}

// resolveSpawnModel returns the policy-assigned ModelRef for role if that model
// is currently in the live pool (pool membership = a live agent of that model
// announced within hostTTL). Returns (ModelRef{}, false) when: the policy has
// no entry for the role, the model is not connected, or book is nil.
// Pure (no I/O, no side-effects) — unit-testable with an injected pool + now.
//
// NOTE: a model that is configured but whose agent has not yet connected will
// NOT be force-spawned. Attribution (swarm_topology Drift) and reconcile still
// flag the gap; the operator resolves it by ensuring the model is running
// before the role's agent spawns. This is intentional for v1.
func resolveSpawnModel(pol rolemodel.Policy, book *HostBook, role string, now int64) (rolemodel.ModelRef, bool) {
	if book == nil || len(pol) == 0 || role == "" {
		return rolemodel.ModelRef{}, false
	}
	pool := book.AvailableModels(hostTTL, now)
	return pol.Available(role, pool)
}

// registerSubagents adds the swarm's subagent tools: an agent (or a subagent) can
// spawn its OWN subagents — children of itself, owned by its principal. Identity
// rolls up: a subagent can never act outside its principal's namespace, and an
// out-of-process delegation token carries the principal's authz with the
// subagent's identity, TTL-bound.
func registerSubagents(s *mcp.Server, store *coord.Store, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "spawn_subagent",
		Description: "Spawn a subagent under yourself with an optional role (it appears as a child node in the swarm). For an out-of-process worker, set out_of_process=true to also get a delegation token it uses as its bearer."},
		func(_ context.Context, req *mcp.CallToolRequest, in spawnSubagentIn) (*mcp.CallToolResult, spawnSubagentOut, error) {
			parent := identity(req, in.Parent) // you, or a parent within your namespace
			if parent == "" {
				parent = "agent" // dev (no auth)
			}
			full := parent + "/" + in.Name
			principal, _ := actor(req)
			budget := opts.SpawnBudget.withDefaults()

			// Gather the two counts the decision needs (read-only).
			// Deliberate fail-open: a coord-store read error here would already be breaking the brain; the counts are stamped immediately on Spawn so the window is nil.
			kids, _ := store.Subagents(parent)
			liveForPrincipal := 0
			if principal != "" {
				// Deliberate fail-open: a coord-store read error here would already be breaking the brain; the counts are stamped immediately on Spawn so the window is nil.
				liveForPrincipal, _ = store.CountLiveByPrincipal(principal)
			}
			if err := budgetDecision(budget, full, parent, len(kids), liveForPrincipal); err != nil {
				rec(opts.Telemetry, 0, "spawn_refused", actorOr(principal, parent), full,
					map[string]any{"reason": err.Error()})
				return nil, spawnSubagentOut{}, err
			}

			if _, err := store.Spawn(parent, in.Name, in.Role, in.Program, in.Model, in.Task); err != nil {
				return nil, spawnSubagentOut{}, err
			}
			_ = store.RecordPrincipal(full, principal)
			out := spawnSubagentOut{Name: full, Parent: parent, Role: in.Role}
			// Best-effort model injection: resolve the policy-assigned model for this
			// role against the live pool. If unavailable (no pool entry, model not
			// connected), leave Model/Backend empty so the child inherits the default.
			// Degrade-never-block: resolveSpawnModel is pure and never errors.
			if ref, ok := resolveSpawnModel(opts.RoleModels, opts.HostBook, in.Role, time.Now().Unix()); ok {
				out.Model = ref.Model
				out.Backend = ref.Backend
			}
			if in.OutOfProcess {
				if opts.MintToken == nil {
					return nil, out, fmt.Errorf("out-of-process subagents unavailable (delegation not enabled on this brain)")
				}
				pRoot := principal
				if pRoot == "" {
					pRoot = parent
				}
				ttl := in.TTLSeconds
				if ttl <= 0 {
					ttl = 3600
				}
				tok, err := opts.MintToken(pRoot, full, time.Duration(ttl)*time.Second)
				if err != nil {
					return nil, out, err
				}
				out.Token = tok
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "despawn_subagent",
		Description: "Retire one of your subagents (releases its claims and removes it from the swarm)."},
		func(_ context.Context, req *mcp.CallToolRequest, in despawnSubagentIn) (*mcp.CallToolResult, okOut, error) {
			if p, _ := actor(req); p != "" && !inNamespace(in.Name, p) {
				return nil, okOut{OK: false}, fmt.Errorf("forbidden: %q is not within your namespace", in.Name)
			}
			ok, err := store.Despawn(in.Name)
			if err == nil && ok {
				// Despawn releases the subagent's claims inside coord — mirror
				// that in the ambience stream (wildcard, same nil→"*" semantics
				// as release_claims) so claim_made events don't dangle unmatched.
				recordClaimReleased(opts.Telemetry, in.Name, nil)
			}
			return nil, okOut{OK: ok}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_subagents",
		Description: "List your subagents (or another agent's children) — the swarm tree one level down."},
		func(_ context.Context, req *mcp.CallToolRequest, in listSubagentsIn) (*mcp.CallToolResult, listSubagentsOut, error) {
			parent := in.Parent
			if parent == "" {
				parent = identity(req, "")
			}
			subs, err := store.Subagents(parent)
			if subs == nil {
				subs = []coord.Agent{}
			}
			return nil, listSubagentsOut{Subagents: subs}, err
		})
}
