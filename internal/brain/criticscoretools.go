// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/criticscore"
)

// errCriticFindingNotFound is get_critic_finding's not-found error — a
// distinct sentinel from errAdminOnly so a caller (or test) can tell "you're
// not allowed" apart from "no such finding".
var errCriticFindingNotFound = fmt.Errorf("criticscore: no such finding")

// criticFindingIDIn is get_critic_finding's input: the stable
// "<recordID>:<queueFindingID>" id (see criticscore.Finding's doc comment).
type criticFindingIDIn struct {
	ID string `json:"id" jsonschema:"the finding id, e.g. \"42:5\" (from list_pending_critic_findings)"`
}

type pendingCriticFindingsOut struct {
	Findings []criticscore.Finding `json:"findings"`
}

// adjudicateCriticFindingIn is adjudicate_critic_finding's input: a human
// verdict that permanently overrides the pool's own auto-adjudication (see
// criticscore.Store.Adjudicate's doc comment — Source becomes "human" and a
// later auto Record can never claw it back).
type adjudicateCriticFindingIn struct {
	ID      string `json:"id" jsonschema:"the finding id, e.g. \"42:5\""`
	Verdict string `json:"verdict" jsonschema:"confirmed|refuted"`
}

// registerCriticScoreTools wires the human-gate over the adversarial pool's
// execution-checked test-critic findings: list what's still pending, read
// one in full, and adjudicate it. Mirrors registerControlTools's admin-gate
// shape exactly (isHumanAdmin on every write, auditKnowledge on every
// successful one) — this is the same "a judge may not certify herself"
// human-gate pattern, just over critic-accuracy findings instead of vetted
// controls.
func registerCriticScoreTools(s *mcp.Server, opts Options) {
	store := opts.CriticScore

	mcp.AddTool(s, &mcp.Tool{Name: "list_pending_critic_findings",
		Description: "ADMIN: list execution-checked test-critic findings still awaiting human adjudication."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, pendingCriticFindingsOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, pendingCriticFindingsOut{}, errAdminOnly
			}
			fs, err := store.ListPending(ctx)
			return nil, pendingCriticFindingsOut{Findings: fs}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_critic_finding",
		Description: "ADMIN: fetch one critic finding in full — model, target test, evidence, auto-adjudication — to read before adjudicating."},
		func(ctx context.Context, req *mcp.CallToolRequest, in criticFindingIDIn) (*mcp.CallToolResult, criticscore.Finding, error) {
			if !opts.isHumanAdmin(req) {
				return nil, criticscore.Finding{}, errAdminOnly
			}
			f, ok, err := store.Get(ctx, in.ID)
			if err != nil {
				return nil, criticscore.Finding{}, err
			}
			if !ok {
				return nil, criticscore.Finding{}, errCriticFindingNotFound
			}
			return nil, f, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "adjudicate_critic_finding",
		Description: "ADMIN: record a human verdict (confirmed|refuted) on a critic finding. Overrides the pool's own auto-adjudication permanently — the recorded, attributed human gate."},
		func(ctx context.Context, req *mcp.CallToolRequest, in adjudicateCriticFindingIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			ok, err := store.Adjudicate(ctx, in.ID, in.Verdict, identity(req, ""))
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no such finding to adjudicate"}, err
			}
			auditKnowledge(opts, req, "adjudicate_critic_finding", map[string]any{"id": in.ID, "verdict": in.Verdict})
			return nil, okMsg{OK: true, Message: in.ID + " adjudicated " + in.Verdict}, nil
		})
}
