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
//
// Global-ambience path-claim beats (claim_made/claim_released, recorded by
// internal/coord with mission_id=0 — there is no mission to join on) are folded
// in by TIME-WINDOW inclusion (v2): any claim beat whose timestamp falls inside
// the mission's active [lo,hi] span is part of what the herd touched during
// this build, so it rides the same merged, sorted stream. This powers the
// file-tree replay lens ("who touched which path, when" — paths only; the tape
// never captures file contents). The remaining global-ambience kinds
// (host_seen/memory_written) stay excluded — they don't map onto a file tree.
func BuildReplayStream(q *queue.Store, tel *telemetry.Store, missionID int64) ([]ReplayEvent, error) {
	var out []ReplayEvent

	tasks, err := q.List(missionID)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.CreatedTS > 0 {
			// task_created carries the task's LINEAGE, so a recording can tell
			// the whole story of a task from the tape alone (the site's task
			// storytelling modal — see internal/ui/web/replay-player.js's
			// buildReplayTaskStories): depends_on is the upstream cause (the
			// task keys that had to finish first — and, reversed, the
			// downstream tasks this one unblocked); instruction is what the
			// task actually asked for; supersedes is the id of the task this
			// one replaced when the herd re-planned. All optional — a mission
			// with no dependencies simply omits depends_on, and the modal is
			// honest about a field the tape didn't capture.
			detail := map[string]any{"role": t.Role, "title": t.Title}
			if len(t.DependsOn) > 0 {
				detail["depends_on"] = t.DependsOn
			}
			if t.Instruction != "" {
				detail["instruction"] = t.Instruction
			}
			if t.Supersedes > 0 {
				detail["supersedes"] = t.Supersedes
			}
			out = append(out, ReplayEvent{TS: t.CreatedTS, Kind: "task_created", Subject: t.Key, Detail: detail})
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

		// v2: fold GLOBAL path-claim ambience into this mission's stream by
		// time-window inclusion. claim_made/claim_released carry mission_id=0
		// (internal/coord has no mission to join on), so they're selected by
		// the mission's active [lo,hi] span instead — computed from every
		// mission-scoped beat gathered above. See this function's header.
		if lo, hi, ok := replaySpan(out); ok {
			amb, err := tel.GlobalAmbienceBetween([]string{"claim_made", "claim_released"}, lo, hi)
			if err != nil {
				return nil, err
			}
			for _, e := range amb {
				out = append(out, ReplayEvent{TS: e.TS, Kind: e.Kind, Actor: e.Actor, Subject: e.Subject, Model: e.Model, Detail: e.Detail})
			}
		}

		// Attribute every beat to the MODEL behind its actor. report_host emits
		// host_seen (global, mission_id=0) carrying each agent's model+backend.
		// Findings already stamp reporter_model at report time, but task and
		// execution beats don't — so a model that only builds/tests would never
		// show in the "work by model" view. Fold the agent→model map in and
		// stamp any beat that doesn't already carry one (system actors like the
		// verify-gate never report_host, so they stay honestly unattributed).
		if lo, hi, ok := replaySpan(out); ok {
			seen, err := tel.GlobalAmbienceBetween([]string{"host_seen"}, lo-3600, hi+60)
			if err != nil {
				return nil, err
			}
			model, backend := map[string]string{}, map[string]string{}
			for _, e := range seen {
				if m, _ := e.Detail["model"].(string); m != "" {
					model[e.Actor] = m
				}
				if b, _ := e.Detail["backend"].(string); b != "" {
					backend[e.Actor] = b
				}
			}
			for i := range out {
				if out[i].Model == "" {
					if m, ok := model[out[i].Actor]; ok {
						out[i].Model = m
					}
				}
				if b, ok := backend[out[i].Actor]; ok {
					if out[i].Detail == nil {
						out[i].Detail = map[string]any{}
					}
					if _, has := out[i].Detail["backend"]; !has {
						out[i].Detail["backend"] = b
					}
				}
			}
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

// replaySpan reports the [lo,hi] timestamp span of a beat slice (positive
// timestamps only), used to define a mission's active window for time-window
// inclusion of global ambience. ok is false when no beat carries a ts.
func replaySpan(evs []ReplayEvent) (lo, hi float64, ok bool) {
	for _, e := range evs {
		if e.TS <= 0 {
			continue
		}
		if !ok {
			lo, hi, ok = e.TS, e.TS, true
			continue
		}
		if e.TS < lo {
			lo = e.TS
		}
		if e.TS > hi {
			hi = e.TS
		}
	}
	return
}
