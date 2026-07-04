// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"sort"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// ReplayEvent is one timestamped beat in a mission's reconstructed history —
// Part B's client replays a merged, sorted stream of these through the same
// apply()/render path the live canvas uses. Positions are never recorded
// here; the client recomputes layout, deliberately (see the spec).
type ReplayEvent struct {
	TS      float64        `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor,omitempty"`
	Subject string         `json:"subject,omitempty"`
	Model   string         `json:"model,omitempty"` // model that filed this beat, when known (findings, telemetry) — Part D's recordings derive meta.models and per-model analytics from this
	Detail  map[string]any `json:"detail,omitempty"`
}

// BuildReplayStream reconstructs a mission's whole build from durable rows
// only — tasks (created/claimed/done/cancelled/superseded), findings,
// executions, and (when present) the telemetry event log — merged and sorted
// oldest first. A mission recorded before Part C's new kinds shipped simply
// contributes nothing from telemetry; the stream still plays from
// tasks/findings/executions alone (graceful degradation) — a mission with
// only queued tasks (no claims yet) still yields task_created beats.
// Global-ambience telemetry kinds (claim_made/claim_released/host_seen/
// memory_written, mission_id=0) are deliberately excluded from this v1
// mission-scoped merge — canvas links derive from task claim windows
// instead; time-window inclusion of global ambience is a flagged v2
// improvement, not implemented here.
func BuildReplayStream(q *queue.Store, tel *telemetry.Store, missionID int64) ([]ReplayEvent, error) {
	var out []ReplayEvent

	tasks, err := q.List(missionID)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.CreatedTS > 0 {
			out = append(out, ReplayEvent{TS: t.CreatedTS, Kind: "task_created", Subject: t.Key,
				Detail: map[string]any{"role": t.Role, "title": t.Title}})
		}
		if t.ClaimedTS > 0 {
			out = append(out, ReplayEvent{TS: t.ClaimedTS, Kind: "task_claimed", Actor: t.ClaimedBy, Subject: t.Key,
				Detail: map[string]any{"role": t.Role, "title": t.Title}})
		}
		if t.DoneTS > 0 {
			kind := "task_" + t.Status // task_done, task_cancelled, task_superseded
			out = append(out, ReplayEvent{TS: t.DoneTS, Kind: kind, Actor: t.ClaimedBy, Subject: t.Key})
		}
	}

	findings, err := q.FindingsFiltered(missionID, "", "")
	if err != nil {
		return nil, err
	}
	for _, f := range findings {
		fev := ReplayEvent{TS: f.CreatedTS, Kind: "finding_reported", Actor: f.Reporter, Subject: f.Target,
			Model:  f.ReporterModel,
			Detail: map[string]any{"type": f.Type, "severity": f.Severity}}
		if f.ReporterBackend != "" {
			fev.Detail["backend"] = f.ReporterBackend
		}
		out = append(out, fev)
		if f.ResolvedTS > 0 {
			out = append(out, ReplayEvent{TS: f.ResolvedTS, Kind: "finding_resolved", Subject: f.Target,
				Detail: map[string]any{"status": f.Status}})
		}
	}

	execs, err := q.ExecutionsByMission(missionID)
	if err != nil {
		return nil, err
	}
	for _, e := range execs {
		out = append(out, ReplayEvent{TS: float64(e.TS), Kind: "execution", Actor: e.Agent, Subject: e.Command,
			Detail: map[string]any{"ok": e.OK, "exit_code": e.ExitCode, "role": e.Role}})
	}

	if tel != nil {
		evs, err := tel.EventsForMission(missionID)
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			out = append(out, ReplayEvent{TS: e.TS, Kind: e.Kind, Actor: e.Actor, Subject: e.Subject, Model: e.Model, Detail: e.Detail})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS < out[j].TS
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}
