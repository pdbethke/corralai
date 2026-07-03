// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// registerActivity registers the report_activity MCP tool against s. When ring
// is nil the function is a no-op.
func registerActivity(s *mcp.Server, ring *ActivityRing) {
	if ring == nil {
		return
	}
	type reportActivityIn struct {
		Name   string `json:"name"`
		Role   string `json:"role"`
		Tool   string `json:"tool"`
		Detail string `json:"detail"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "report_activity",
		Description: "Report a tool-call you just made (observability only) so the swarm's live console shows what every bee is doing, in every phase. Best-effort: never blocks or alters your work.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportActivityIn) (*mcp.CallToolResult, okOut, error) {
		ring.Add(Activity{
			Agent:  identity(req, in.Name),
			Role:   in.Role,
			Tool:   in.Tool,
			Detail: in.Detail,
			TS:     time.Now().Unix(),
		})
		return nil, okOut{OK: true}, nil
	})
}
