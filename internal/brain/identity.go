// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/attest"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/oracle"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/recordings"
	"github.com/pdbethke/corralai/internal/reference"
	"github.com/pdbethke/corralai/internal/repo"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/taskartifacts"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// VerifyFunc independently runs a task's verify command against the brain's OWN
// working copy (dir) and reports the true pass/fail — the brain, not the worker,
// owns this bit. It is pure execution, not analysis: run the command, read the
// exit code. A judge may not certify herself. nil => the gate falls back to the
// recorded-execution lookup (legacy / non-repo missions).
type VerifyFunc func(ctx context.Context, dir, command string) (ok bool, detail string)

// RepoIndex is the interface the brain uses to index and search a mission's
// working-copy code. *repoindex.Store satisfies it; tests inject a fake.
type RepoIndex interface {
	IndexPaths(missionID int64, dir string, paths []string) error
	Search(missionID int64, query string, k int) ([]repoindex.Hit, error)
}

// Options configure authorization-aware behavior of the brain.
type Options struct {
	// MemoryOwners, when non-empty, restricts the memory tools to these verified
	// principals (emails). Empty => any authorized caller may use memory. This
	// keeps a private corpus private even from other authorized teammates.
	// Superusers (see Principals) always count as owners.
	MemoryOwners map[string]bool

	// Principals is the Django-style role authority: who may use the brain and who
	// is a superuser (admin — may promote gateway endpoints/memory and manage other
	// principals). nil => dev mode (admin gate open only when unauthenticated).
	Principals *principals.Store

	// Gateway, when set, enables the MCP-gateway tools (upstream registry + proxy).
	Gateway *gateway.Store

	// Artifacts, when set, enables fleet skill/hook sync (bidirectional, superuser
	// push). nil => the sync tools aren't registered.
	Artifacts *artifacts.Store

	// Missions, when set, enables the orchestration tools (create_mission etc.).
	// The mission ENGINE is driven separately (a ticker in main). nil => off.
	Missions *mission.Store

	// Queue is the task queue the swarm executes a mission through (the pull
	// model). Required alongside Missions for create_mission to enqueue work and
	// for the claim_task/complete_task/list_tasks tools. nil => no task tools.
	Queue *queue.Store

	// TaskArtifacts is the dedicated database store for task execution artifacts.
	TaskArtifacts *taskartifacts.Store

	// Browser is the Go-native headless browser manager.
	Browser *BrowserManager

	// TaskLeaseSeconds is the claim lease a bee gets on claim_task — the fallback
	// requeue window when presence is unavailable. 0 => 300s.
	TaskLeaseSeconds float64

	// IdleReclaimGraceSeconds is the minimum age of a task claim before an idle
	// heartbeat from its holder may reclaim it (the slacker rule; the claim's
	// lease must also have expired). 0 => 30s.
	IdleReclaimGraceSeconds float64

	// ReclaimBackoffSeconds is the self-heal cooldown: after an agent's task is
	// force-reclaimed (a failing worker making no progress), claim_task denies
	// that agent a new claim for this long, so it can't re-enter a tight reclaim
	// loop and starve healthy workers. The window expires into a probation claim
	// and a success clears it. 0 => 30s; set negative (or via "none" upstream) to
	// disable.
	ReclaimBackoffSeconds float64

	// ConvergeBlockSeverity mirrors mission.Engine.ConvergeBlockSeverity: the
	// lowest open-finding severity that parks a converging mission at
	// "needs-review". The resolve_review tool re-checks against this same
	// threshold so the human can't certify a mission the engine would still
	// hold. "" => "high" (the engine default).
	ConvergeBlockSeverity string

	// Reference is the bring-your-own reference corpus (RAG); Embedder is the
	// remote embeddings client. Both required for the reference tools — nil =>
	// the reference engine is disabled (no embeddings endpoint configured).
	Reference *reference.Store
	Embedder  *reference.Embedder

	// Telemetry, when set, records the mission event timeline (created, claimed,
	// completed, findings, re-plans, reviews) for DuckDB analysis. nil => off.
	Telemetry *telemetry.Store

	// Recordings, when set, exposes scrubbed exported replay recordings as a
	// queryable DuckDB store (list/query/get replay). nil => tools off.
	Recordings *recordings.Store

	// Egress guards outbound gateway connections against SSRF (blocks private/
	// loopback/link-local targets unless allowlisted). nil => a default block guard.
	Egress *gateway.Guard

	// MintToken issues a scoped delegation token so an OUT-OF-PROCESS subagent can
	// authenticate as a subagent of the caller (principal rolls up, identity is the
	// subagent name, TTL-bound). nil => out-of-process subagents unavailable.
	MintToken func(principal, subagent string, ttl time.Duration) (string, error)

	// MintObserver issues a read-only observer token (may view the swarm, may not
	// act). Used by the mint_observer admin tool. nil => observer tokens unavailable.
	MintObserver func(principal string, ttl time.Duration) (string, error)

	// Repo, when set, enables repo-work missions (git → PR) and the repo read tools.
	Repo *repo.Engine
	// Workspace is where repo clones live (the brain owns the working copy).
	Workspace string

	// Verify, when set, makes the completion gate RUN the task's verify command
	// itself (in a jail, cwd = MissionDir(Workspace, mission)) and certify on the
	// real exit code — instead of trusting a worker-reported execution row. This
	// is what makes the gate independent ("a judge may not certify herself").
	// nil => the gate falls back to the recorded-execution lookup.
	Verify VerifyFunc

	// Index, when set, enables repo_search and per-mission code indexing.
	// Declared as the RepoIndex interface so tests can inject a fake without
	// pulling in the concrete *repoindex.Store; *repoindex.Store satisfies it.
	Index RepoIndex

	// Oracle, when set and Enabled(), registers the ask_fleet tool — a natural-
	// language query over the whole fleet's state via MotherDuck. nil or disabled
	// => the tool is not registered (no MCP surface, no DuckDB/LLM work).
	Oracle *oracle.Client

	// AskFleetRateLimit overrides the default per-principal ask_fleet rate limit
	// (asks per minute). Zero uses the default (10). Exposed for tests.
	AskFleetRateLimit int

	// ExecRing, when set, enables the report_execution tool and accumulates the
	// last 40 agent command results for the swarm UI. nil => tool not registered.
	ExecRing *ExecRing

	// ActivityRing, when set, enables the report_activity tool — every bee tool-call
	// streamed to the UI console so all phases show motion, not just exec phases.
	// nil => tool not registered.
	ActivityRing *ActivityRing

	// HostBook, when set, enables the report_host tool — each bee's runtime facts
	// (host, model, jail) for the UI topology view. nil => tool not registered.
	HostBook *HostBook

	// Health, when set, tracks per-agent claim/complete/reclaim activity so
	// claim_task/complete_task/the idle-reclaim heartbeat path can feed the
	// health heuristic (see health.go). nil => /api/state's health field
	// degrades to "idle" for every agent (HealthBook.Health is nil-safe).
	Health *HealthBook

	// WorkerSessions tracks, per MCP session, whether the session has
	// identified itself as a corral-agent worker (ClientInfo.Name, or an
	// earlier bootstrap/report_host call) — the dev-mode half of the human
	// gate (see isHumanAdmin). nil => the dev-mode worker-session check is
	// skipped for the marked-by-behavior signal; the live ClientInfo check
	// still runs (WorkerSessions.Is is nil-receiver-safe).
	WorkerSessions *WorkerSessions

	// RoleModels is the declared policy mapping each role to its expected model.
	// When set, swarm_topology annotates each host with Expected + Drift so the
	// operator can spot a mis-assigned model at a glance. nil/empty => no
	// annotations (Expected="", Drift=false — degrade-never-block).
	RoleModels *rolemodel.Policy

	// SpawnBudget bounds spawn_subagent: standing live agents per principal, spawn
	// tree depth, and children per parent. Zero fields take the defaults (64/4/8);
	// zero NEVER means unlimited.
	SpawnBudget SpawnBudget

	// Coord, when set, enables audit rows for knowledge ingest and promotion
	// (add_reference, promote_reference, add_memory, promote_memory). Audit writes
	// are best-effort — a nil Coord or a write failure never fails the tool call.
	Coord *coord.Store

	// FleetTarget is the MotherDuck (or local DuckDB) path for the cross-swarm
	// coordination plane (fleet_brains / fleet_intents tables). Required alongside
	// CrossSwarm for fleet_claims to function.
	FleetTarget string

	// FleetBrainID is this brain's canonical identifier for cross-swarm coordination.
	// Used as the "exceptBrain" exclusion so a brain never sees its own claims.
	FleetBrainID string

	// CrossSwarm, when true, registers the fleet_claims cross-swarm dedup tool.
	// Set only when CORRALAI_MOTHERDUCK is configured AND the brain's Ed25519
	// keypair was successfully loaded/persisted (so identity is stable across
	// restarts). False → the tool is absent; the brain runs normally.
	CrossSwarm bool

	// CrossSwarmKey is this brain's Ed25519 keypair, used to SIGN the coordination
	// claims this brain publishes (brain-internal, from create_mission). The private
	// key NEVER leaves the process — only signatures + the public key cross to
	// MotherDuck. Set alongside CrossSwarm=true. The zero value is unusable
	// (nil Priv); publish is only attempted when CrossSwarm is true.
	CrossSwarmKey attest.KeyPair

	// FleetClaimsRateLimit overrides the per-principal fleet_claims rate limit
	// (reads per minute). Zero uses the default (10). Exposed for tests.
	FleetClaimsRateLimit int

	// Learn, when set, enables the learning-loop proposal tools (list/approve/
	// reject/skill) and backs the sweep ticker that clusters recurring findings
	// and lessons into human-gated proposals. nil => the tools aren't registered
	// and the sweep is a no-op.
	Learn *learn.Store

	// LearnDrafter is the LLM surface Learn uses to phrase (never decide)
	// guidance and an optional skill body for a newly opened proposal. nil =>
	// proposals open pending with no draft; a human still approves/rejects them
	// from the raw evidence.
	LearnDrafter learn.Asker
}

// SpawnBudget is the brain-side request-side DoS bound for spawning.
type SpawnBudget struct {
	MaxAgentsPerPrincipal int
	// MaxSpawnDepth is the depth at which spawning is REFUSED (>=); the deepest allowed depth is MaxSpawnDepth-1. A top-level agent is depth 1.
	MaxSpawnDepth        int
	MaxChildrenPerParent int
}

// withDefaults replaces any unset (0) field with its default. Zero is "default",
// never "unlimited".
func (b SpawnBudget) withDefaults() SpawnBudget {
	if b.MaxAgentsPerPrincipal == 0 {
		b.MaxAgentsPerPrincipal = 64
	}
	if b.MaxSpawnDepth == 0 {
		b.MaxSpawnDepth = 4
	}
	if b.MaxChildrenPerParent == 0 {
		b.MaxChildrenPerParent = 8
	}
	return b
}

// isAdmin reports whether the request's verified principal is a superuser.
func (o Options) isAdmin(req *mcp.CallToolRequest) bool {
	p, _ := actor(req)
	if o.Principals == nil {
		return p == "" // dev (no role store) => allow only when unauthenticated
	}
	return o.Principals.IsSuperuser(p)
}

// isHumanAdmin is the human gate: isAdmin PLUS proof the caller isn't a
// delegated subagent riding on a superuser's rolled-up authorization. A
// delegation token always carries Extra["subagent"] (oidc.go's
// verifyDelegation), so an agent spawned under a superuser principal passes
// isAdmin — the gap this closes. Every admin write that shapes fleet-wide
// behavior (proposal approval/rejection, memory/reference promotion) must
// gate on this, not isAdmin alone: the herd must never vet its own knowledge.
func (o Options) isHumanAdmin(req *mcp.CallToolRequest) bool {
	if !o.isAdmin(req) || subagentOf(req) != "" {
		return false
	}
	return !o.WorkerSessions.Is(req)
}

// actor returns the verified principal (email) and tenant from the request's
// bearer token, or empty strings when unauthenticated (dev mode / no claim).
func actor(req *mcp.CallToolRequest) (principal, tenant string) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return "", ""
	}
	ti := req.Extra.TokenInfo
	principal = ti.UserID
	if ti.Extra != nil {
		if t, ok := ti.Extra["tenant_id"].(string); ok {
			tenant = t
		}
	}
	return principal, tenant
}

// subagentOf returns the subagent identity carried by a delegation token (an
// out-of-process subagent), or "" for an ordinary principal token.
func subagentOf(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.Extra == nil {
		return ""
	}
	if s, ok := req.Extra.TokenInfo.Extra["subagent"].(string); ok {
		return s
	}
	return ""
}

// inNamespace reports whether a coordination name belongs to principal p — either
// p itself or a subagent within p's namespace ("p/..."). This is what lets an
// agent register/claim as its own subagents but not as a different principal.
func inNamespace(name, p string) bool {
	return name == p || strings.HasPrefix(name, p+"/")
}

// identity returns the AUTHORITATIVE coordination name. A delegation token names
// the subagent directly (already validated to be in the principal's namespace at
// mint time). Otherwise the verified principal must own the requested name's
// namespace (so a caller can act as itself or its OWN subagents, never as another
// principal); unauthenticated dev falls back to the client-supplied name.
func identity(req *mcp.CallToolRequest, fallback string) string {
	if sub := subagentOf(req); sub != "" {
		return sub
	}
	p, _ := actor(req)
	if p == "" {
		return fallback
	}
	if inNamespace(fallback, p) {
		return fallback
	}
	return p
}

// isMemoryOwner reports whether the principal owns the corpus (sees private +
// shared entries). Superusers always own it; otherwise empty owners => everyone
// authorized is treated as an owner.
func (o Options) isMemoryOwner(req *mcp.CallToolRequest) bool {
	p, _ := actor(req)
	if o.Principals != nil && o.Principals.IsSuperuser(p) {
		return true
	}
	if len(o.MemoryOwners) == 0 {
		return true
	}
	return p != "" && o.MemoryOwners[p]
}
