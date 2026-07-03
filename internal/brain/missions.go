// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

type phaseSpecIn struct {
	Name        string   `json:"name"`
	Role        string   `json:"role,omitempty"`
	Program     string   `json:"program,omitempty" jsonschema:"agent type for this phase (e.g. gemini) — heterogeneous verification"`
	Instruction string   `json:"instruction"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Count       int      `json:"count,omitempty"`
}
type createMissionIn struct {
	Directive      string        `json:"directive" jsonschema:"the high-level goal, e.g. 'add a wishlist feature'"`
	Plan           []phaseSpecIn `json:"plan,omitempty" jsonschema:"optional custom phases; omit for the default research -> design -> build -> verify -> integrate -> docs -> retro pipeline"`
	RequiresReview bool          `json:"requires_review,omitempty" jsonschema:"if true, the mission waits for a client review (accept or feedback) instead of auto-completing — enables sprints"`
	Repo           string        `json:"repo,omitempty" jsonschema:"git repo URL to build in (omit for a workspace-only mission)"`
	Base           string        `json:"base,omitempty" jsonschema:"base branch to branch from (default main)"`
}
type missionIDIn struct {
	ID int64 `json:"id"`
}
type reviewMissionIn struct {
	ID       int64  `json:"id" jsonschema:"the mission to review"`
	Accept   bool   `json:"accept" jsonschema:"true to accept the deliverable (mission done); false to request changes"`
	Feedback string `json:"feedback,omitempty" jsonschema:"the change request when accept=false — what needs to be different"`
}

type listMissionsOut struct {
	Missions []mission.Mission `json:"missions"`
}

// registerMissions adds the orchestration tools: turn a directive into a mission
// the brain drives — spawning a fresh, independent set of role agents per phase
// (build, then test ∥ secops, then a learning retro), each reading + recording in
// shared memory. Available to any allowed caller (the command surface); audited via
// the per-mission orchestrator.
func registerMissions(s *mcp.Server, store *mission.Store, q *queue.Store, mem *memory.Store, tel *telemetry.Store, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "create_mission",
		Description: "Launch a mission from a directive: the brain orchestrates independent agents to build it, then OTHER agents to test it and a secops agent to attack it, then a retrospective — sharing memory throughout. Omit plan for the default pipeline."},
		func(ctx context.Context, req *mcp.CallToolRequest, in createMissionIn) (*mcp.CallToolResult, mission.MissionView, error) {
			if in.Directive == "" {
				return nil, mission.MissionView{}, fmt.Errorf("directive required")
			}
			specs := make([]mission.PhaseSpec, 0, len(in.Plan))
			for _, p := range in.Plan {
				specs = append(specs, mission.PhaseSpec{
					Name: p.Name, Role: p.Role, Program: p.Program,
					Instruction: p.Instruction, DependsOn: p.DependsOn, Count: p.Count,
				})
			}
			// Learning loop: materialize the plan, then inject lessons recalled from
			// memory so past mistakes actively shape this mission's instructions.
			if len(specs) == 0 {
				specs = mission.DefaultPlan(in.Directive)
			}
			// Learning loop: inject VETTED lessons only, and only when a real role
			// authority exists (fail-closed — without Principals, "shared" is not
			// trustworthy because isAdmin is permissive in dev).
			if mem != nil && opts.Principals != nil {
				if hits, err := mem.RecallLessons(in.Directive, 5); err == nil {
					var lessons []mission.Lesson
					for _, h := range hits {
						text := h.Description
						if text == "" {
							text = h.Name
						}
						lessons = append(lessons, mission.Lesson{Text: text, Author: h.Author})
					}
					specs = mission.InjectLessons(specs, lessons)
				}
			}
			id, err := mission.CreateMission(store, q, in.Directive, specs, in.RequiresReview)
			if err != nil {
				return nil, mission.MissionView{}, err
			}
			// Repo provisioning: clone + checkout a mission branch when requested.
			// rollback drops BOTH the mission (phases+row) and its queue tasks so a
			// failed provisioning never leaves a half-created mission or orphan tasks.
			var crossSwarmNote string
			if in.Repo != "" {
				// dest is captured by reference so rollback removes the clone dir once
				// it's known; before then it's "" and os.RemoveAll("") is a no-op.
				var dest string
				rollback := func() {
					_ = store.DeleteMission(id)
					if q != nil {
						_ = q.DeleteMissionTasks(id)
					}
					// Remove any clone dir left on disk when Clone succeeded but a later
					// step failed (no-op when the dir was never created).
					_ = os.RemoveAll(dest)
				}
				if opts.Repo == nil || opts.Workspace == "" {
					rollback()
					return nil, mission.MissionView{}, fmt.Errorf("repo missions not enabled on this brain")
				}
				base := in.Base
				if base == "" {
					base = "main"
				}
				branch := fmt.Sprintf("corralai/m%d", id)
				dest = mission.MissionDir(opts.Workspace, id)
				if err := opts.Repo.Clone(ctx, in.Repo, base, dest); err != nil {
					rollback()
					return nil, mission.MissionView{}, fmt.Errorf("clone %s: %w", in.Repo, err)
				}
				if err := opts.Repo.Checkout(ctx, dest, branch); err != nil {
					rollback()
					return nil, mission.MissionView{}, fmt.Errorf("checkout: %w", err)
				}
				if err := store.SetRepo(id, in.Repo, base, branch); err != nil {
					rollback()
					return nil, mission.MissionView{}, fmt.Errorf("set repo: %w", err)
				}
				// Initial full index: walk the working copy and collect all paths, then
				// index in a goroutine so the create_mission response is not blocked by
				// potentially minutes of embed HTTP calls over the whole repo.
				// Fire-and-forget: a search-index failure must never abort a provisioned
				// mission; search is an aid, not a gate. The store serializes writes via
				// SetMaxOpenConns(1), so the goroutine and the engine Tick are safe.
				if opts.Index != nil {
					var all []string
					// The callback always returns nil by design (per-file errors are
					// skipped), so WalkDir's own return is intentionally discarded.
					_ = filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
						if err != nil {
							return nil
						}
						if d.IsDir() {
							if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
								return filepath.SkipDir
							}
							return nil
						}
						rel, _ := filepath.Rel(dest, p)
						all = append(all, filepath.ToSlash(rel))
						return nil
					})
					go func(missionID int64, dir string, paths []string) {
						if err := opts.Index.IndexPaths(missionID, dir, paths); err != nil {
							log.Printf("mission %d: initial index: %v", missionID, err)
						}
					}(id, dest, all)
				}
				// Cross-swarm coordination (advisory dedup): surface any verified peer
				// claim on this repo, then publish THIS brain's claim so peers observe
				// it. Brain-internal + best-effort — never blocks or fails the mission.
				crossSwarmNote = crossSwarmMissionClaim(opts, in.Repo, time.Now())
			}
			rec(tel, id, "mission_created", actorOf(req), "", map[string]any{"directive": in.Directive, "review": in.RequiresReview})
			mv, err := store.View(id, q)
			if err != nil || mv == nil {
				return nil, mission.MissionView{}, err
			}
			// When a peer already claims this repo, surface the advisory note as a text
			// content block. The structured MissionView is still returned (the SDK
			// populates StructuredContent from the Out value regardless of Content).
			// SDK coupling: relies on go-sdk populating StructuredContent from the Out
			// (second) return even when the first (CallToolResult) return is non-nil —
			// validated by TestCreateMission_AdvisorySurfacesPeerClaimWithoutBlocking;
			// a future SDK change to that behaviour would break this silently.
			if crossSwarmNote != "" {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: crossSwarmNote}}}, *mv, nil
			}
			return nil, *mv, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "mission_status",
		Description: "Full status of a mission: each phase, its role, status, and the agents working it."},
		func(_ context.Context, _ *mcp.CallToolRequest, in missionIDIn) (*mcp.CallToolResult, mission.MissionView, error) {
			mv, err := store.View(in.ID, q)
			if err != nil {
				return nil, mission.MissionView{}, err
			}
			if mv == nil {
				return nil, mission.MissionView{}, fmt.Errorf("no mission %d", in.ID)
			}
			return nil, *mv, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_missions", Description: "Recent missions and their status."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listMissionsOut, error) {
			ms, err := store.ListMissions()
			if ms == nil {
				ms = []mission.Mission{}
			}
			return nil, listMissionsOut{Missions: ms}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "review_mission",
		Description: "Client review of a mission awaiting review: accept to complete it, or request changes with feedback — which opens a change-request the lead turns into the next sprint's rework. Used by the human operator or a client agent."},
		func(_ context.Context, req *mcp.CallToolRequest, in reviewMissionIn) (*mcp.CallToolResult, mission.MissionView, error) {
			mv, err := mission.SubmitReview(store, q, in.ID, in.Accept, in.Feedback, identity(req, "client"))
			if err != nil {
				return nil, mission.MissionView{}, err
			}
			kind := "review_changes"
			if in.Accept {
				kind = "review_accepted"
			}
			rec(tel, in.ID, kind, identity(req, "client"), "", nil)
			return nil, *mv, nil
		})
}
