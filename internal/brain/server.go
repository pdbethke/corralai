// SPDX-License-Identifier: Elastic-2.0

// Package brain assembles the CorralAI MCP server: it wraps the coordination
// core (internal/coord) as MCP tools served over streamable-HTTP. This is what a
// thin client (a coding agent on any machine) talks to.
package brain

import (
	"context"
	"fmt"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
)

// defaultIdleReclaimGraceSeconds is the age floor before an idle heartbeat may
// reclaim a bee's own expired-lease claim — belt-and-braces against racing the
// reply of a claim made a moment ago (ReclaimIdle additionally requires lease
// expiry). Override via Options.IdleReclaimGraceSeconds.
const defaultIdleReclaimGraceSeconds = 30.0

type bootstrapIn struct {
	Name      string `json:"name" jsonschema:"your agent name (memorable, e.g. BlueLake)"`
	Task      string `json:"task,omitempty" jsonschema:"what you're about to work on"`
	Program   string `json:"program,omitempty" jsonschema:"client program, e.g. Claude Code"`
	Role      string `json:"role,omitempty" jsonschema:"your role in the swarm (e.g. builder, tester, pentester, reviewer)"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type heartbeatIn struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty" jsonschema:"working | awaiting_approval | idle"`
}
type okOut struct {
	OK bool `json:"ok"`
}

type claimIn struct {
	Name       string   `json:"name"`
	Paths      []string `json:"paths" jsonschema:"files/dirs/branches to lease"`
	TTLSeconds float64  `json:"ttl_seconds,omitempty" jsonschema:"lease lifetime in seconds (default 3600)"`
	Exclusive  *bool    `json:"exclusive,omitempty" jsonschema:"exclusive lease (default true)"`
	Reason     string   `json:"reason,omitempty"`
}

type releaseIn struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths,omitempty" jsonschema:"paths to release; omit to release all of yours"`
}
type releaseOut struct {
	Released int64 `json:"released"`
}

type whoisIn struct {
	Name string `json:"name"`
}
type whoisOut struct {
	Agent        coord.Agent   `json:"agent"`
	ActiveClaims []coord.Claim `json:"active_claims"`
}

type listActiveIn struct {
	WindowSeconds float64 `json:"window_seconds,omitempty" jsonschema:"presence window in seconds (default 300)"`
}
type listActiveOut struct {
	Agents []coord.Agent `json:"agents"`
}

type markDoneIn struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary" jsonschema:"what you finished — peers see this and won't redo it"`
	Paths   []string `json:"paths,omitempty"`
}
type markDoneOut struct {
	Recorded string `json:"recorded"`
}

type statusIn struct {
	WindowSeconds float64 `json:"window_seconds,omitempty"`
}

// NewServer builds the MCP server backed by the coordination store and, when
// provided, the memory store (memory tools register only when mem != nil). opts
// carries authorization config (e.g. which principals own the memory corpus).
//
// Every identity-bearing coordination tool stamps the AUTHORITATIVE actor — the
// verified principal from the bearer token — over the client-supplied name, so a
// caller cannot register, claim, or complete work as anyone else.
func NewServer(store *coord.Store, mem *memory.Store, opts Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "corralai", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "bootstrap",
		Description: "Enter a coordination session in ONE call: register/refresh yourself, and get active peers, your live claims, and recently-completed work (so you don't redo a peer's finished task). Call this first."},
		func(_ context.Context, req *mcp.CallToolRequest, in bootstrapIn) (*mcp.CallToolResult, coord.Bootstrap, error) {
			name := identity(req, in.Name)
			b, err := store.BootstrapSession(name, in.Program, in.Model, in.Task, in.SessionID, in.Role)
			if err != nil {
				return nil, coord.Bootstrap{}, err
			}
			if pr, _ := actor(req); pr != "" {
				_ = store.RecordPrincipal(name, pr)
			}
			return nil, *b, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "heartbeat", Description: "Refresh your presence so peers know you're alive. Optionally report status (working|awaiting_approval|idle)."},
		func(_ context.Context, req *mcp.CallToolRequest, in heartbeatIn) (*mcp.CallToolResult, okOut, error) {
			actor := identity(req, in.Name)
			if in.Status != "" {
				if err := store.SetStatus(actor, in.Status); err != nil {
					return nil, okOut{}, err
				}
				// The slacker rule: an idle bee holding an expired task claim is
				// contradicting itself (lost claim reply, crashed run). Presence
				// shields live bees from Reap, so the brain must resolve the
				// contradiction itself or the orphan deadlocks the queue.
				if in.Status == "idle" && opts.Queue != nil {
					grace := opts.IdleReclaimGraceSeconds
					if grace <= 0 {
						grace = defaultIdleReclaimGraceSeconds
					}
					if reclaimed, err := opts.Queue.ReclaimIdle(actor, grace); err != nil {
						log.Printf("queue: idle reclaim for %s: %v", actor, err)
					} else {
						for _, t := range reclaimed {
							log.Printf("queue: %s heartbeats idle while holding expired claim on task #%d (%s) — requeued", actor, t.ID, t.Key)
							rec(opts.Telemetry, t.MissionID, "task_reclaimed", actor, t.Key,
								map[string]any{"role": t.Role, "reason": "idle heartbeat, lease expired"})
						}
					}
				}
			}
			return nil, okOut{OK: true}, store.Heartbeat(actor)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "claim_paths",
		Description: "Lease files/dirs/branches and learn of conflicts. EXCLUSIVE claims (the default) are ENFORCED: if a peer holds an overlapping exclusive lease your path is NOT granted — always check `granted` before you act on it. Pass exclusive:false for an advisory lease, which is always granted with any conflicts surfaced for you to weigh. A peer that has been awaiting human approval past the grace window has its exclusive lease downgraded to advisory, so you'll be granted with a surfaced conflict. Leases auto-expire after ttl_seconds."},
		func(_ context.Context, req *mcp.CallToolRequest, in claimIn) (*mcp.CallToolResult, coord.ClaimResult, error) {
			ttl := in.TTLSeconds
			if ttl <= 0 {
				ttl = 3600
			}
			excl := true
			if in.Exclusive != nil {
				excl = *in.Exclusive
			}
			r, err := store.ClaimPaths(identity(req, in.Name), in.Paths, ttl, excl, in.Reason)
			if err != nil {
				return nil, coord.ClaimResult{}, err
			}
			return nil, *r, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "release_claims", Description: "Release your leases (specific paths, or all of yours if omitted)."},
		func(_ context.Context, req *mcp.CallToolRequest, in releaseIn) (*mcp.CallToolResult, releaseOut, error) {
			n, err := store.ReleaseClaims(identity(req, in.Name), in.Paths)
			return nil, releaseOut{Released: n}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "whois", Description: "Profile + active claims for one agent."},
		func(_ context.Context, _ *mcp.CallToolRequest, in whoisIn) (*mcp.CallToolResult, whoisOut, error) {
			a, claims, err := store.Whois(in.Name)
			if err != nil {
				return nil, whoisOut{}, err
			}
			if a == nil {
				return nil, whoisOut{}, fmt.Errorf("no agent %q", in.Name)
			}
			return nil, whoisOut{Agent: *a, ActiveClaims: claims}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_active", Description: "Agents that heartbeat within the window."},
		func(_ context.Context, _ *mcp.CallToolRequest, in listActiveIn) (*mcp.CallToolResult, listActiveOut, error) {
			w := in.WindowSeconds
			if w <= 0 {
				w = coord.PresenceWindow
			}
			agents, err := store.ListActive(w)
			if err != nil {
				return nil, listActiveOut{}, err
			}
			if agents == nil {
				agents = []coord.Agent{}
			}
			return nil, listActiveOut{Agents: agents}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "mark_done", Description: "Record finished work so peers (via bootstrap) don't duplicate it."},
		func(_ context.Context, req *mcp.CallToolRequest, in markDoneIn) (*mcp.CallToolResult, markDoneOut, error) {
			return nil, markDoneOut{Recorded: in.Summary}, store.MarkDone(identity(req, in.Name), in.Summary, in.Paths)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "coordination_status", Description: "Snapshot: active agents, live claims, recent completed work."},
		func(_ context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, coord.Status, error) {
			w := in.WindowSeconds
			if w <= 0 {
				w = coord.PresenceWindow
			}
			st, err := store.CoordinationStatus(w)
			if err != nil {
				return nil, coord.Status{}, err
			}
			return nil, *st, nil
		})

	registerAdmin(s, opts)
	registerSubagents(s, store, opts)
	registerInbox(s, store, opts)
	if mem != nil {
		registerMemory(s, mem, opts)
	}
	if opts.Gateway != nil {
		registerGateway(s, opts)
	}
	if opts.Artifacts != nil {
		registerArtifacts(s, opts.Artifacts, opts)
	}
	if opts.Learn != nil {
		registerLearn(s, opts.Learn, mem, opts.Artifacts, opts)
	}
	if opts.Missions != nil {
		registerMissions(s, opts.Missions, opts.Queue, mem, opts.Telemetry, opts)
	}
	if opts.Repo != nil && opts.Queue != nil && opts.Missions != nil {
		registerRepoFiles(s, opts.Queue, opts.Missions, opts.Repo, opts.Workspace)
		registerRepoSync(s, opts)
	}
	if opts.Index != nil && opts.Queue != nil && opts.Missions != nil {
		registerRepoSearch(s, opts)
	}
	if opts.Queue != nil {
		registerTasks(s, opts.Queue, opts.TaskLeaseSeconds, opts.Telemetry, opts.HostBook, opts.Learn)
	}
	if opts.Reference != nil && opts.Embedder != nil {
		registerReference(s, opts)
	}
	if opts.Telemetry != nil {
		registerAnalytics(s, opts)
	}
	if opts.Oracle != nil && opts.Oracle.Enabled() {
		registerAskFleet(s, opts)
	}
	if opts.CrossSwarm {
		registerFleetClaims(s, opts)
	}
	registerExecutions(s, opts.ExecRing, opts.Queue)
	registerActivity(s, opts.ActivityRing)
	registerHost(s, opts.HostBook, opts)
	return s
}
