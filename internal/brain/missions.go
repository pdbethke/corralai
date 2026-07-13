// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

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

// registerMissions adds the mission lifecycle tools: status/listing, the
// client review + findings-gate resolution paths, and mid-mission human
// steering (pause/resume/cancel). Mission creation (the former create_mission
// build verb) is retired — missions are created via mission.CreateMission by
// whatever slice-2 caller re-scopes that entry point. Available to any allowed
// caller (the command surface); audited via the per-mission orchestrator.
func registerMissions(s *mcp.Server, store *mission.Store, q *queue.Store, mem *memory.Store, tel *telemetry.Store, opts Options) {
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
			if mv.Status == "done" {
				rounds := 0
				if full, ferr := store.Mission(in.ID); ferr == nil && full != nil {
					rounds = full.ReviewRounds
				}
				rec(tel, in.ID, "mission_completed", "engine", "", map[string]any{"status": "done", "review_rounds": rounds})
			}
			return nil, *mv, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "resolve_review",
		Description: "Resolve a mission parked at needs-review: the convergence findings-gate withheld certification because an open critical/high finding never became a task. Dismiss or address those findings first (resolve_finding), then call this to certify the mission done. Refused while any blocking finding is still open — a judge may not certify a result it knows still holds a critical defect."},
		func(_ context.Context, req *mcp.CallToolRequest, in missionIDIn) (*mcp.CallToolResult, mission.MissionView, error) {
			blockSev := opts.ConvergeBlockSeverity
			if blockSev == "" {
				blockSev = "high" // engine default (mission.NewEngine)
			}
			mv, err := mission.ResolveNeedsReview(store, q, in.ID, blockSev)
			if err != nil {
				return nil, mission.MissionView{}, err
			}
			rec(tel, in.ID, "review_resolved", identity(req, "operator"), "", nil)
			if mv.Status == "done" {
				rounds := 0
				if full, ferr := store.Mission(in.ID); ferr == nil && full != nil {
					rounds = full.ReviewRounds
				}
				rec(tel, in.ID, "mission_completed", "engine", "", map[string]any{"status": "done", "review_rounds": rounds})
			}
			return nil, *mv, nil
		})

	// pause_mission / resume_mission / cancel_mission are #58's mid-mission
	// human steering: today's human gate was TERMINAL only (review_mission,
	// above) and mission creation was PRE-launch only — there was no
	// way to intervene on a mission while it runs. Each is isHumanAdmin-gated
	// exactly like approve_proposal/reject_proposal: a delegation/worker token
	// riding a superuser's rolled-up authorization must never pause/cancel a
	// mission out from under the fleet. See SteerMission for the semantics
	// and the claim-path enforcement (queue.Store.HaltMission).
	mcp.AddTool(s, &mcp.Tool{Name: "pause_mission",
		Description: "Pause a running mission: the brain hands out no NEW tasks for it (in-flight claims still finish). Superuser only — mid-mission human steering."},
		func(_ context.Context, req *mcp.CallToolRequest, in missionIDIn) (*mcp.CallToolResult, mission.Mission, error) {
			if !opts.isHumanAdmin(req) {
				return nil, mission.Mission{}, fmt.Errorf("forbidden: superuser only (pausing a mission is an operator action)")
			}
			mi, err := SteerMission(store, q, tel, in.ID, SteerPause, actorOf(req))
			if err != nil {
				return nil, mission.Mission{}, err
			}
			return nil, *mi, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "resume_mission",
		Description: "Resume a paused mission: restores normal task dispatch. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in missionIDIn) (*mcp.CallToolResult, mission.Mission, error) {
			if !opts.isHumanAdmin(req) {
				return nil, mission.Mission{}, fmt.Errorf("forbidden: superuser only (resuming a mission is an operator action)")
			}
			mi, err := SteerMission(store, q, tel, in.ID, SteerResume, actorOf(req))
			if err != nil {
				return nil, mission.Mission{}, err
			}
			return nil, *mi, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "cancel_mission",
		Description: "Cancel a mission for good: no more tasks are handed out and it leaves the active set. Irreversible — there is no resume from cancelled. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in missionIDIn) (*mcp.CallToolResult, mission.Mission, error) {
			if !opts.isHumanAdmin(req) {
				return nil, mission.Mission{}, fmt.Errorf("forbidden: superuser only (cancelling a mission is an operator action)")
			}
			mi, err := SteerMission(store, q, tel, in.ID, SteerCancel, actorOf(req))
			if err != nil {
				return nil, mission.Mission{}, err
			}
			return nil, *mi, nil
		})
}
