// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

// ownsInbox reports whether the caller may read/ack the inbox named `name`: its
// own (verified principal == name), or an admin. Unauthenticated (dev) => allowed.
func ownsInbox(req *mcp.CallToolRequest, name string, opts Options) bool {
	p, _ := actor(req)
	if p == "" {
		return true
	}
	return opts.isAdmin(req) || strings.EqualFold(p, name)
}

type sendInstructionIn struct {
	Target string `json:"target" jsonschema:"the agent name to instruct (as shown in the swarm)"`
	Text   string `json:"text" jsonschema:"the instruction to carry out"`
}
type sendInstructionOut struct {
	ID int64 `json:"id"`
	OK bool  `json:"ok"`
}
type checkInstructionsIn struct {
	Name string `json:"name" jsonschema:"your agent name — the inbox to check"`
}
type instructionsOut struct {
	Instructions []coord.Instruction `json:"instructions"`
}
type ackInstructionIn struct {
	ID     int64  `json:"id" jsonschema:"the instruction id"`
	Name   string `json:"name" jsonschema:"your agent name"`
	Result string `json:"result,omitempty" jsonschema:"short summary of what you did"`
}
type ackOut struct {
	OK bool `json:"ok"`
}

func registerInbox(s *mcp.Server, store *coord.Store, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "send_instruction",
		Description: "Issue an instruction to another agent (by its swarm name). It queues until that agent picks it up via check_instructions."},
		func(_ context.Context, req *mcp.CallToolRequest, in sendInstructionIn) (*mcp.CallToolResult, sendInstructionOut, error) {
			issuer := identity(req, "agent")
			id, err := store.SendInstruction(issuer, in.Target, in.Text)
			return nil, sendInstructionOut{ID: id, OK: err == nil}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "check_instructions",
		Description: "Pull YOUR pending instructions (commands issued to you). Call at the start of work and periodically; act on each, then ack_instruction."},
		func(_ context.Context, req *mcp.CallToolRequest, in checkInstructionsIn) (*mcp.CallToolResult, instructionsOut, error) {
			if !ownsInbox(req, in.Name, opts) {
				return nil, instructionsOut{Instructions: []coord.Instruction{}}, nil // can only read your own inbox
			}
			ins, err := store.PendingInstructions(in.Name)
			return nil, instructionsOut{Instructions: ins}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "ack_instruction",
		Description: "Mark one of YOUR instructions done with a short result summary (so the issuer sees it completed)."},
		func(_ context.Context, req *mcp.CallToolRequest, in ackInstructionIn) (*mcp.CallToolResult, ackOut, error) {
			if !ownsInbox(req, in.Name, opts) {
				return nil, ackOut{OK: false}, nil
			}
			ok, err := store.AckInstruction(in.ID, in.Name, in.Result)
			return nil, ackOut{OK: ok}, err
		})
}
