// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// Health signal for #72: tonight a Copilot worker was out-of-quota and
// exiting status 1 every round, but the brain still showed it as a live
// agent — no health signal existed. A failing worker like that does NOT
// loudly tell the brain "I failed" (the harness just backs off and re-polls);
// it BOOTSTRAPs, may claim_task, but its CLI invocation dies before it ever
// calls complete_task. So health must be INFERRED from the claim/complete/
// reclaim activity the harness already reports honestly — no worker-side
// change required.
//
// Heuristic (tunable; see workingWindow/stallWindow below):
//   - "working": completed a task within workingWindow — actively productive.
//   - "idle": no claim activity recorded since its last success (or ever).
//     This is the safe default and is NEVER flagged as failing — a worker
//     with no ready work to claim looks identical to a healthy one with
//     nothing to do, and under-flagging beats false-alarming a genuinely
//     idle worker.
//   - "failing": has claimed a task (or had one force-reclaimed for going
//     idle mid-lease, see server.go's ReclaimIdle call) since its last
//     success, but that claim is stale (older than stallWindow) or a
//     reclaim already happened — i.e. it is claiming or polling but making
//     no forward progress. This is the case that catches a dead-quota CLI:
//     it may claim_task once, then never complete_task again.
//
// State is in-memory only (like HostBook/WorkerSessions): a brain restart
// forgets prior activity, so every agent starts back at "idle" until it
// claims or completes again — harmless, since a genuinely failing worker
// re-trips the heuristic within one stallWindow.
const (
	// workingWindow: a success this recent means the agent is actively
	// productive right now.
	workingWindow = 300 * time.Second
	// stallWindow: a claim this old with no success since means the agent
	// picked up work and stopped making progress on it.
	stallWindow = 300 * time.Second
)

// HealthAgent is one agent's inferred-health record, as exposed in
// /api/state so the operator (and later, staffing math — see #44) can see
// which workers are actually making progress.
type HealthAgent struct {
	Agent                 string `json:"agent"`
	Health                string `json:"health"` // working|idle|failing
	LastClaimTS           int64  `json:"last_claim_ts,omitempty"`
	LastSuccessTS         int64  `json:"last_success_ts,omitempty"`
	ClaimsSinceSuccess    int    `json:"claims_since_success,omitempty"`
	ReclaimedSinceSuccess int    `json:"reclaimed_since_success,omitempty"`
}

// healthState is the raw per-agent counters HealthBook tracks.
type healthState struct {
	lastClaim             int64
	lastSuccess           int64
	claimsSinceSuccess    int
	reclaimedSinceSuccess int
}

// HealthBook tracks per-agent claim/complete/reclaim activity so the brain
// can classify health without any explicit worker-side failure report.
// Safe for concurrent use; a nil *HealthBook degrades every method to a
// no-op / "idle" default (degrade-never-block, matching HostBook/WorkerSessions).
type HealthBook struct {
	mu    sync.Mutex
	items map[string]*healthState
	now   func() time.Time // clock seam; tests override
}

// NewHealthBook returns an empty tracker.
func NewHealthBook() *HealthBook {
	return &HealthBook{items: map[string]*healthState{}, now: time.Now}
}

// getLocked returns (creating if absent) agent's state. Callers must hold mu.
func (b *HealthBook) getLocked(agent string) *healthState {
	st, ok := b.items[agent]
	if !ok {
		st = &healthState{}
		b.items[agent] = st
	}
	return st
}

// RecordClaim notes that agent successfully claimed a task (claim_task
// returned a non-nil task, including a re-issued one — a re-issue still
// means the claim is live and unfinished).
func (b *HealthBook) RecordClaim(agent string) {
	if b == nil || agent == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.getLocked(agent)
	st.lastClaim = b.now().Unix()
	st.claimsSinceSuccess++
}

// RecordSuccess notes that agent completed a task — the failure counters
// reset because the agent just demonstrated forward progress.
func (b *HealthBook) RecordSuccess(agent string) {
	if b == nil || agent == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.getLocked(agent)
	st.lastSuccess = b.now().Unix()
	st.claimsSinceSuccess = 0
	st.reclaimedSinceSuccess = 0
}

// RecordReclaimed notes that a task agent held was force-reclaimed (idle
// heartbeat + expired lease, see server.go's ReclaimIdle call) — direct
// evidence the claim made no progress before it lapsed.
func (b *HealthBook) RecordReclaimed(agent string) {
	if b == nil || agent == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.getLocked(agent)
	st.reclaimedSinceSuccess++
}

// Health classifies agent per the heuristic documented on this file. An
// agent with no recorded activity (nil book, unknown name, or never having
// claimed/completed anything) reports "idle" — the safe default absent any
// evidence of either productivity or a stall.
func (b *HealthBook) Health(agent string) HealthAgent {
	out := HealthAgent{Agent: agent, Health: "idle"}
	if b == nil || agent == "" {
		return out
	}
	b.mu.Lock()
	st, ok := b.items[agent]
	var snap healthState
	if ok {
		snap = *st
	}
	nowFn := b.now
	b.mu.Unlock()
	if !ok {
		return out
	}
	out.LastClaimTS = snap.lastClaim
	out.LastSuccessTS = snap.lastSuccess
	out.ClaimsSinceSuccess = snap.claimsSinceSuccess
	out.ReclaimedSinceSuccess = snap.reclaimedSinceSuccess

	now := nowFn().Unix()
	if snap.lastSuccess > 0 && now-snap.lastSuccess < int64(workingWindow.Seconds()) {
		out.Health = "working"
		return out
	}
	if snap.claimsSinceSuccess == 0 && snap.reclaimedSinceSuccess == 0 {
		out.Health = "idle" // no claim activity at all — genuinely nothing to do
		return out
	}
	if snap.reclaimedSinceSuccess > 0 {
		out.Health = "failing" // a claim of theirs was force-reclaimed — no progress
		return out
	}
	if snap.lastClaim > 0 && now-snap.lastClaim >= int64(stallWindow.Seconds()) {
		out.Health = "failing" // claimed a while ago, still no completion since
		return out
	}
	out.Health = "working" // claimed recently, within grace — presume still on it
	return out
}

// DetectRoleStalls scans ready tasks and files one missing-req finding per task
// when the task has aged past threshold with no eligible active agent role.
// Returns how many NEW findings were filed this sweep.
func DetectRoleStalls(q *queue.Store, active []coord.Agent, book *HealthBook, threshold time.Duration, tel *telemetry.Store) (int, error) {
	if q == nil {
		return 0, nil
	}
	activeCount := len(active)
	activeRoles := map[string]bool{}
	for _, a := range active {
		if a.Role == "" {
			continue
		}
		// A FAILING agent (a claim of theirs was force-reclaimed, or it claimed
		// long ago with no completion) does not count as role coverage — otherwise
		// a dead-but-heart-beating worker keeps its role "covered" and its reclaim
		// loop stays invisible forever. Only a healthy live agent covers a role.
		if book != nil && book.Health(a.Name).Health == "failing" {
			continue
		}
		activeRoles[a.Role] = true
	}
	tasks, err := q.Active()
	if err != nil {
		return 0, err
	}
	nowTS := float64(time.Now().UnixNano()) / 1e9
	if threshold < 0 {
		threshold = 0
	}
	thresholdS := threshold.Seconds()
	cache := map[int64]map[string]bool{}
	filed := 0

	openTargets := func(missionID int64) map[string]bool {
		if m, ok := cache[missionID]; ok {
			return m
		}
		m := map[string]bool{}
		fs, ferr := q.Findings(missionID, queue.FindingOpen)
		if ferr != nil {
			log.Printf("stall-watchdog: findings(%d): %v", missionID, ferr)
			cache[missionID] = m
			return m
		}
		for _, f := range fs {
			if f.Reporter == "stall-watchdog" && f.Type == "missing-req" && f.Target != "" {
				m[f.Target] = true
			}
		}
		cache[missionID] = m
		return m
	}

	for _, t := range tasks {
		if t.Status != queue.StatusReady {
			continue
		}
		ageS := nowTS - t.CreatedTS
		if ageS < thresholdS {
			continue
		}
		eligible := false
		if t.Role == "" {
			eligible = activeCount > 0
		} else {
			eligible = activeRoles[t.Role]
		}
		if eligible {
			continue
		}
		if openTargets(t.MissionID)[t.Key] {
			continue
		}
		if _, err := q.AddFinding(queue.Finding{
			MissionID:       t.MissionID,
			TaskID:          t.ID,
			Reporter:        "stall-watchdog",
			Type:            "missing-req",
			Severity:        "high",
			Target:          t.Key,
			Evidence:        fmt.Sprintf("ready task %q (role=%q) has no eligible active agent after %.0fs", t.Key, t.Role, ageS),
			SuggestedAction: "staff this role, retarget the task role, or supersede/cancel the task",
		}); err != nil {
			return filed, err
		}
		cache[t.MissionID][t.Key] = true
		filed++
		if tel != nil {
			if err := tel.Record(telemetry.Event{
				MissionID: t.MissionID,
				Kind:      "task_stalled",
				Actor:     "stall-watchdog",
				Subject:   t.Key,
				Detail: map[string]any{
					"task_id":     t.ID,
					"role":        t.Role,
					"age_seconds": int64(ageS),
				},
			}); err != nil {
				log.Printf("telemetry task_stalled: %v", err)
			}
		}
	}
	return filed, nil
}
