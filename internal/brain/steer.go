// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"fmt"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// Steer actions — #58's mid-mission human steering. Until now the human gate
// was TERMINAL only (approve/reject a finished/awaiting-review mission via
// review_mission) and the composer was PRE-launch (plan a mission before it
// starts); there was no way to intervene on a mission while it runs. These
// three verbs give an operator that control:
//
//   - pause:  the brain hands out no NEW tasks for the mission (the claim
//     path — queue.Store.HaltMission + the mission_halts exclusion in
//     ClaimNextAs — enforces this); in-flight claims may still finish.
//   - resume: clears the halt and returns the mission to "running".
//   - cancel: halts it for good (same claim-path enforcement) and marks it
//     "cancelled" — it leaves the active set (RunningMissions no longer
//     returns it); there is no resume from cancelled.
//
// REDIRECT/re-scope (inject a new instruction mid-flight and re-plan) is a
// documented follow-up, not implemented here.
const (
	SteerPause  = "pause"
	SteerResume = "resume"
	SteerCancel = "cancel"
)

// SteerMission applies one of the above actions to a mission and records it
// (attributed to actor) in telemetry — best-effort, like every other rec()
// call in this package — so the action surfaces in history/replay
// (BuildReplayStream merges every telemetry event for the mission). Callers
// (the pause_mission/resume_mission/cancel_mission MCP tools and the
// /api/mission/pause|resume|cancel HTTP handlers) are responsible for the
// human gate: only a verified, non-delegated superuser may steer a mission,
// exactly like PruneMission/ApproveProposal.
func SteerMission(m *mission.Store, q *queue.Store, tel *telemetry.Store, id int64, action, actor string) (*mission.Mission, error) {
	var mi *mission.Mission
	var err error
	switch action {
	case SteerPause:
		mi, err = mission.PauseMission(m, q, id)
	case SteerResume:
		mi, err = mission.ResumeMission(m, q, id)
	case SteerCancel:
		mi, err = mission.CancelMission(m, q, id)
	default:
		return nil, fmt.Errorf("unknown steer action %q (want pause|resume|cancel)", action)
	}
	if err != nil {
		return nil, err
	}
	if actor == "" {
		actor = "operator"
	}
	rec(tel, id, "mission_"+action, actor, "", nil)
	return mi, nil
}
