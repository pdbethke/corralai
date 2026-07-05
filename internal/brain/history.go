// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// MissionSummary is one row of the Completed tab's list.
type MissionSummary struct {
	ID                int64    `json:"id"`
	Directive         string   `json:"directive"`
	Status            string   `json:"status"`
	CreatedTS         float64  `json:"created_ts"`
	UpdatedTS         float64  `json:"updated_ts"`
	DurationSeconds   float64  `json:"duration_seconds"`
	TaskCount         int      `json:"task_count"`
	DoneTaskCount     int      `json:"done_task_count"`
	FindingCount      int      `json:"finding_count"`
	PRURL             string   `json:"pr_url,omitempty"`
	LearnedSignatures []string `json:"learned_signatures,omitempty"`
}

// MissionDetail is the per-mission drill-down: phases/tasks/findings/executions.
type MissionDetail struct {
	MissionSummary
	Phases     []mission.PhaseView `json:"phases"`
	Tasks      []queue.Task        `json:"tasks"`
	Findings   []queue.Finding     `json:"findings"`
	Executions []queue.Execution   `json:"executions"`
}

// matchLearnedSignatures is the spec's best-effort linkage: promoted
// (approved) proposals whose signature matches one of this mission's
// findings. Heuristic until proposals carry mission provenance directly.
func matchLearnedSignatures(findings []queue.Finding, approved []learn.Proposal) []string {
	approvedSigs := map[string]bool{}
	for _, p := range approved {
		if p.Status == learn.StatusApproved {
			approvedSigs[p.Signature] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range findings {
		sig := f.Type + "|" + f.Target
		if approvedSigs[sig] && !seen[sig] {
			seen[sig] = true
			out = append(out, sig)
		}
	}
	return out
}

// summarize builds one MissionSummary from a mission row + its queue/telemetry
// state. duration prefers mission_completed's event ts (once it exists) over
// the last task's done_ts, per the spec's "event-based once it exists" rule.
func summarize(mi mission.Mission, q *queue.Store, tel *telemetry.Store, approved []learn.Proposal) (MissionSummary, error) {
	tasks, err := q.List(mi.ID)
	if err != nil {
		return MissionSummary{}, err
	}
	findings, err := q.FindingsFiltered(mi.ID, "", "")
	if err != nil {
		return MissionSummary{}, err
	}
	done := 0
	var lastDone float64
	for _, t := range tasks {
		if t.Status == queue.StatusDone {
			done++
		}
		if t.DoneTS > lastDone {
			lastDone = t.DoneTS
		}
	}
	end := lastDone
	if tel != nil {
		if ts, found, err := tel.MissionCompletedAt(mi.ID); err != nil {
			log.Printf("history: MissionCompletedAt(%d): %v", mi.ID, err)
		} else if found {
			end = ts
		}
	}
	dur := 0.0
	if end > mi.CreatedTS {
		dur = end - mi.CreatedTS
	}
	return MissionSummary{
		ID: mi.ID, Directive: mi.Directive, Status: mi.Status,
		CreatedTS: mi.CreatedTS, UpdatedTS: mi.UpdatedTS, DurationSeconds: dur,
		TaskCount: len(tasks), DoneTaskCount: done, FindingCount: len(findings),
		PRURL: mi.PRURL, LearnedSignatures: matchLearnedSignatures(findings, approved),
	}, nil
}

// MissionHistoryList returns every non-active mission, newest created first.
// "running" AND "paused" are both excluded (#58): a paused mission is still
// active — steering it (resume/cancel) is done from the live view, not the
// Completed tab — only cancelled/done/awaiting_review missions belong here.
func MissionHistoryList(m *mission.Store, q *queue.Store, tel *telemetry.Store, l *learn.Store) ([]MissionSummary, error) {
	all, err := m.ListMissions()
	if err != nil {
		return nil, err
	}
	var approved []learn.Proposal
	if l != nil {
		approved, _ = l.List(learn.StatusApproved) // best-effort: linkage degrades to empty, never fails the list
	}
	out := make([]MissionSummary, 0, len(all))
	for _, mi := range all {
		if mi.Status == "running" || mi.Status == "paused" {
			continue
		}
		sm, err := summarize(mi, q, tel, approved)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, nil
}

// MissionHistoryDetail returns the full drill-down for one mission, or
// (nil, nil) when no such mission exists.
func MissionHistoryDetail(m *mission.Store, q *queue.Store, tel *telemetry.Store, l *learn.Store, id int64) (*MissionDetail, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, err
	}
	var approved []learn.Proposal
	if l != nil {
		approved, _ = l.List(learn.StatusApproved)
	}
	sm, err := summarize(*mi, q, tel, approved)
	if err != nil {
		return nil, err
	}
	mv, err := m.View(id, q)
	if err != nil {
		return nil, err
	}
	tasks, err := q.List(id)
	if err != nil {
		return nil, err
	}
	findings, err := q.FindingsFiltered(id, "", "")
	if err != nil {
		return nil, err
	}
	var execs []queue.Execution
	if q != nil {
		execs, err = q.ExecutionsByMission(id)
		if err != nil {
			return nil, err
		}
	}
	phases := []mission.PhaseView{}
	if mv != nil {
		phases = mv.Phases
	}
	return &MissionDetail{
		MissionSummary: sm, Phases: phases, Tasks: tasks, Findings: findings, Executions: execs,
	}, nil
}

type missionHistoryIn struct {
	ID int64 `json:"id,omitempty" jsonschema:"omit for the list of past missions; pass to drill into one mission's phases/tasks/findings/executions"`
}
type missionHistoryOut struct {
	Missions []MissionSummary `json:"missions,omitempty"`
	Mission  *MissionDetail   `json:"mission,omitempty"`
}

// registerHistory adds the mission_history read-only tool: past missions
// (list) or one mission's full drill-down (detail). Mirrors mission_analytics'
// report/ad-hoc branch shape. Registered only when Missions+Queue are set.
func registerHistory(s *mcp.Server, opts Options) {
	if opts.Missions == nil || opts.Queue == nil {
		return
	}
	mcp.AddTool(s, &mcp.Tool{Name: "mission_history",
		Description: "Past missions the herd already finished: list (directive, status, duration, task/finding counts, best-effort what-got-learned) or, given an id, the full drill-down — phases, tasks, findings, executions."},
		func(_ context.Context, _ *mcp.CallToolRequest, in missionHistoryIn) (*mcp.CallToolResult, missionHistoryOut, error) {
			if in.ID != 0 {
				d, err := MissionHistoryDetail(opts.Missions, opts.Queue, opts.Telemetry, opts.Learn, in.ID)
				if err != nil {
					return nil, missionHistoryOut{}, err
				}
				return nil, missionHistoryOut{Mission: d}, nil
			}
			ms, err := MissionHistoryList(opts.Missions, opts.Queue, opts.Telemetry, opts.Learn)
			if err != nil {
				return nil, missionHistoryOut{}, err
			}
			return nil, missionHistoryOut{Missions: ms}, nil
		})
}
