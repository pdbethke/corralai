// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"log"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// Activity is a single tool-call a bee made — search_memory, write_file,
// claim_paths, report_finding, … — surfaced so the swarm UI's live console
// streams in EVERY phase, not only the exec phases that run shell commands.
// This is observability-only: unlike Execution it is never durable and never
// feeds the verification gate (run_command stays on report_execution for that).
type Activity struct {
	Agent  string `json:"agent"`
	Role   string `json:"role"`
	Tool   string `json:"tool"`
	Detail string `json:"detail"`
	TS     int64  `json:"ts"` // Unix seconds
}

const activityRingCap = 60

// ActivityRing is a mutex-guarded ring of the last activityRingCap tool-calls,
// newest-first. It mirrors ExecRing deliberately rather than sharing it: the
// execution ring is load-bearing for the gate and its UI render carries exit
// badges, while activity is a lighter, command-free stream.
type ActivityRing struct {
	mu    sync.Mutex
	items []Activity
}

// NewActivityRing returns an initialised ActivityRing.
func NewActivityRing() *ActivityRing { return &ActivityRing{} }

// Add prepends a to the ring, capping at activityRingCap entries.
func (r *ActivityRing) Add(a Activity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append([]Activity{a}, r.items...)
	if len(r.items) > activityRingCap {
		r.items = r.items[:activityRingCap]
	}
}

// Recent returns a copy of the ring contents, newest-first.
func (r *ActivityRing) Recent() []Activity {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Activity, len(r.items))
	copy(out, r.items)
	return out
}

// agentActivityCap bounds how many agent_activity events one mission can
// record — the cap protects the telemetry log, the log protects replay.
const agentActivityCap = 2000

// activityDetailMax bounds the DURABLE copy of an activity's summary, in
// runes. The field is agent-authored free text pitched as a one-line summary,
// and 2000 multi-KB "lines" per mission would defeat the cap's point. The
// live ring keeps the full line; only the recorded copy is cut.
const activityDetailMax = 240

// truncateDetail cuts s to activityDetailMax runes, marking the cut with a
// trailing ellipsis so a truncated summary reads as truncated in replay.
func truncateDetail(s string) string {
	if utf8.RuneCountInString(s) <= activityDetailMax {
		return s
	}
	r := []rune(s)
	return string(r[:activityDetailMax-1]) + "…"
}

// capCache remembers which missions have been observed at the agent_activity
// cap so post-cap reports skip the COUNT query entirely. One bool per mission
// ever capped — naturally bounded by mission count, so no eviction needed at
// this scale.
type capCache struct {
	mu sync.Mutex
	m  map[int64]bool
}

// capped reports whether missionID was already observed at cap.
func (c *capCache) capped(missionID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[missionID]
}

// mark records missionID as capped, reporting whether this call was the first
// to do so — the loud "cap reached" log keys off that so it fires exactly once.
func (c *capCache) mark(missionID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[missionID] {
		return false
	}
	if c.m == nil {
		c.m = map[int64]bool{}
	}
	c.m[missionID] = true
	return true
}

// recordActivity emits an agent_activity telemetry event for a, gated by
// agentActivityCap. tel or missionID==0 is a no-op (activity reported outside
// a mission, or telemetry disabled, is observability-only and never durable).
// Crossing the cap logs loudly exactly once (per process) so an unbounded
// mission's silence is visible, not silent.
func recordActivity(tel *telemetry.Store, ring *ActivityRing, capped *capCache, missionID int64, a Activity) {
	ring.Add(a)
	if tel == nil || missionID == 0 {
		return
	}
	if capped.capped(missionID) {
		return
	}
	n, err := tel.CountKind(missionID, "agent_activity")
	if err != nil {
		log.Printf("telemetry agent_activity: count: %v", err)
		return
	}
	if n >= agentActivityCap {
		// Accepted race: concurrent reporters can each read n just under the
		// cap and land a few events past 2000 — the cap protects the log, not
		// exact arithmetic.
		if capped.mark(missionID) {
			log.Printf("telemetry: agent_activity cap (%d) reached for mission %d — further activity for this mission will not be recorded", agentActivityCap, missionID)
		}
		return
	}
	if err := tel.Record(telemetry.Event{
		MissionID: missionID, Kind: "agent_activity", Actor: a.Agent,
		Detail: map[string]any{"role": a.Role, "tool": a.Tool, "detail": truncateDetail(a.Detail)},
	}); err != nil {
		log.Printf("telemetry agent_activity: %v", err)
	}
}

// registerActivity registers the report_activity MCP tool against s. When ring
// is nil the function is a no-op.
func registerActivity(s *mcp.Server, ring *ActivityRing, opts Options) {
	if ring == nil {
		return
	}
	type reportActivityIn struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		Tool      string `json:"tool"`
		Detail    string `json:"detail"`
		MissionID int64  `json:"mission_id,omitempty" jsonschema:"the mission this activity belongs to, when known — enables durable recording for replay"`
	}
	capped := &capCache{}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "report_activity",
		Description: "Report a tool-call you just made (observability only) so the swarm's live console shows what every bee is doing, in every phase. Pass mission_id when you have one so it's durably recorded for replay. Best-effort: never blocks or alters your work.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportActivityIn) (*mcp.CallToolResult, okOut, error) {
		recordActivity(opts.Telemetry, ring, capped, in.MissionID, Activity{
			Agent:  identity(req, in.Name),
			Role:   in.Role,
			Tool:   in.Tool,
			Detail: in.Detail,
			TS:     time.Now().Unix(),
		})
		return nil, okOut{OK: true}, nil
	})
}
