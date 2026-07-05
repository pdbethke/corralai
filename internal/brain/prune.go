// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// MissionFootprint is one mission's storage cost: the coordination rows
// (tasks/claims-via-task-state/findings/executions, held in the queue's
// SQLite) plus the telemetry events (held in DuckDB, including thought
// beats) it has accumulated. This is the DB relief valve's FOOTPRINT view
// (#66) — enough for an operator to see what a mission is costing before
// deciding to export its story and prune it.
type MissionFootprint struct {
	MissionID       int64 `json:"mission_id"`
	Tasks           int   `json:"tasks"`
	Findings        int   `json:"findings"`
	Executions      int   `json:"executions"`
	TelemetryEvents int   `json:"telemetry_events"`
	TotalRows       int   `json:"total_rows"`
}

// MissionFootprintOf computes one mission's footprint across both stores.
// tel may be nil (telemetry disabled) — TelemetryEvents degrades to 0.
func MissionFootprintOf(q *queue.Store, tel *telemetry.Store, missionID int64) (MissionFootprint, error) {
	fp := MissionFootprint{MissionID: missionID}
	tasks, findings, execs, err := q.FootprintCounts(missionID)
	if err != nil {
		return MissionFootprint{}, err
	}
	fp.Tasks, fp.Findings, fp.Executions = tasks, findings, execs
	if tel != nil {
		n, err := tel.CountForMission(missionID)
		if err != nil {
			return MissionFootprint{}, err
		}
		fp.TelemetryEvents = n
	}
	fp.TotalRows = fp.Tasks + fp.Findings + fp.Executions + fp.TelemetryEvents
	return fp, nil
}

// MissionFootprintAll computes the footprint of every known mission — the
// fleet-wide view an operator scans to decide which missions are worth
// exporting + pruning.
func MissionFootprintAll(m *mission.Store, q *queue.Store, tel *telemetry.Store) ([]MissionFootprint, error) {
	all, err := m.ListMissions()
	if err != nil {
		return nil, err
	}
	out := make([]MissionFootprint, 0, len(all))
	for _, mi := range all {
		fp, err := MissionFootprintOf(q, tel, mi.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, fp)
	}
	return out, nil
}

// PruneMission DESTRUCTIVELY deletes a mission's records from BOTH the
// coordination store (tasks/findings/executions, via queue.Store.PruneMission)
// and telemetry (events, via telemetry.Store.PruneMission), reclaiming the
// storage MissionFootprintOf reports. It returns the footprint as it stood
// immediately before deletion, so the caller (an HTTP/MCP handler) can log or
// echo what was reclaimed.
//
// This is irreversible and must be human-gated by the caller (see
// internal/ui's isSuperuser + auth.ReadOnly/Subagent checks, mirroring the
// isHumanAdmin gate on other admin writes) — a delegation/worker token must
// never be able to prune. Callers are expected to have already durably
// exported the mission's story (BuildReplayStream / GET /api/replay) to a
// static file BEFORE calling this: once pruned, the mission's live detail is
// gone from both stores and the exported tape is the only surviving record.
// A published static recording (site/src/data/recordings/*.json) is a
// separate file outside either store and is never touched here.
func PruneMission(q *queue.Store, tel *telemetry.Store, missionID int64) (MissionFootprint, error) {
	before, err := MissionFootprintOf(q, tel, missionID)
	if err != nil {
		return MissionFootprint{}, err
	}
	if err := q.PruneMission(missionID); err != nil {
		return MissionFootprint{}, err
	}
	if tel != nil {
		if err := tel.PruneMission(missionID); err != nil {
			return MissionFootprint{}, err
		}
	}
	return before, nil
}
