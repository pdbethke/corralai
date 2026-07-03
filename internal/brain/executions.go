// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/queue"
)

// Execution records a single shell command run by a swarm agent.
type Execution struct {
	Agent    string `json:"agent"`
	Role     string `json:"role"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Ok       bool   `json:"ok"`
	TimedOut bool   `json:"timed_out"`
	Summary  string `json:"summary"`
	TS       int64  `json:"ts"` // Unix seconds
}

const execRingCap = 40

// ExecRing is a mutex-guarded ring of the last 40 executions, newest-first.
type ExecRing struct {
	mu    sync.Mutex
	items []Execution
}

// NewExecRing returns an initialised ExecRing.
func NewExecRing() *ExecRing {
	return &ExecRing{}
}

// Add prepends e to the ring, capping at execRingCap entries.
func (r *ExecRing) Add(e Execution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append([]Execution{e}, r.items...)
	if len(r.items) > execRingCap {
		r.items = r.items[:execRingCap]
	}
}

// Recent returns a copy of the ring contents, newest-first.
func (r *ExecRing) Recent() []Execution {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Execution, len(r.items))
	copy(out, r.items)
	return out
}

// registerExecutions registers the report_execution MCP tool against s.
// When ring is nil the function is a no-op.
func registerExecutions(s *mcp.Server, ring *ExecRing, q *queue.Store) {
	if ring == nil {
		return
	}

	type reportExecIn struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Command  string `json:"command"`
		ExitCode int    `json:"exit_code"`
		Ok       bool   `json:"ok"`
		TimedOut bool   `json:"timed_out"`
		Summary  string `json:"summary"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "report_execution",
		Description: "Report a shell command execution result so the swarm brain can track what agents ran and whether it succeeded.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportExecIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)
		ring.Add(Execution{
			Agent:    agent,
			Role:     in.Role,
			Command:  in.Command,
			ExitCode: in.ExitCode,
			Ok:       in.Ok,
			TimedOut: in.TimedOut,
			Summary:  in.Summary,
			TS:       time.Now().Unix(),
		})
		// Durable record for the verification gate: attribute to the agent's mission.
		if q != nil {
			mid, _ := q.ClaimedMission(agent)
			if err := q.RecordExecution(queue.Execution{
				MissionID: mid, Agent: agent, Role: in.Role, Command: in.Command,
				ExitCode: in.ExitCode, OK: in.Ok, TS: time.Now().Unix(),
			}); err != nil {
				log.Printf("report_execution: durable record failed: %v", err)
			}
		}
		return nil, okOut{OK: true}, nil
	})
}
