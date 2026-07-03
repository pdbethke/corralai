// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// validSkillName is the choke point for LLM-drafted skill names before they
// ever reach an artifact path ("skills/"+SkillName+"/SKILL.md"): lowercase
// alphanumeric + hyphens, starting alphanumeric, no "..", "/", or other path
// characters — so a drafted name like "../hooks/pre-commit" can never escape
// the skills/ namespace on a naive sync_pull client. Capped at 64 characters
// (a sane ceiling for a skill slug, not a real limit anyone should hit).
var validSkillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// registerLearn adds the learning loop's proposal tools: list_proposals is
// open to any authorized caller (read-only), while approve_proposal and
// reject_proposal are superuser-gated — the human gate the spec's trust model
// requires. Approval is the ONLY place a proposal's guidance/skill fans out
// into standing memory and the fleet artifact store; nothing shapes agent
// behavior without a human clicking approve.
func registerLearn(s *mcp.Server, ls *learn.Store, mem *memory.Store, arts *artifacts.Store, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_proposals",
		Description: "See what the herd has surfaced for review — findings and lessons the sweep clustered into proposals awaiting a human decision. Filter by status (pending|approved|rejected); omit for all."},
		func(_ context.Context, _ *mcp.CallToolRequest, in listProposalsIn) (*mcp.CallToolResult, listProposalsOut, error) {
			ps, err := ls.List(in.Status)
			if err != nil {
				return nil, listProposalsOut{}, err
			}
			if ps == nil {
				ps = []learn.Proposal{}
			}
			return nil, listProposalsOut{Proposals: ps}, nil
		})

	// Retry-safe by CONVERGENCE, not rollback: the fan-out spans three stores
	// (memory files, artifacts SQLite, proposals SQLite) that cannot commit
	// atomically. Instead, every step is idempotent — memory.Add is an upsert
	// by slug (same name rewrites the same file), and artifacts.Put keeps the
	// current rev when the content is byte-identical — and ls.Approve runs
	// LAST. So if a middle step fails, the proposal stays pending (the tool
	// errors loudly), and the operator simply re-approves: already-landed
	// steps no-op, missing steps land, no duplicate artifact revisions are
	// minted. The status guard below closes the gate once approved, so a
	// completed approve can never be re-run. The fan-out itself lives in
	// ApproveProposal (below) so the UI's approve endpoint can call the exact
	// same logic as this tool — this handler is a thin wrapper.
	mcp.AddTool(s, &mcp.Tool{Name: "approve_proposal",
		Description: "Promote a proposal the herd surfaced: its guidance joins vetted memory (shapes future instructions) and its skill syncs fleet-wide. Superuser only — the human gate of the learning loop."},
		func(_ context.Context, req *mcp.CallToolRequest, in approveProposalIn) (*mcp.CallToolResult, approveProposalOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, approveProposalOut{}, fmt.Errorf("forbidden: superuser only (approval shapes fleet-wide behavior)")
			}
			res, err := ApproveProposal(ls, mem, arts, opts.Telemetry, in.ID, actorOf(req), in.GuidanceOnly, in.SkillOnly)
			if err != nil {
				return nil, approveProposalOut{}, err
			}
			return nil, approveProposalOut{OK: true, PromotedGuidanceSlug: res.PromotedGuidanceSlug, SkillPath: res.SkillPath, SkillRev: res.SkillRev}, nil
		})

	// reject_proposal is likewise a thin wrapper over RejectProposal, the
	// symmetric counterpart the UI's reject endpoint also calls.
	mcp.AddTool(s, &mcp.Tool{Name: "reject_proposal",
		Description: "Dismiss a proposal the herd surfaced. Its signature stays suppressed until the underlying evidence doubles, so rejecting noise doesn't reopen a nag loop. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in rejectProposalIn) (*mcp.CallToolResult, okOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, okOut{}, fmt.Errorf("forbidden: superuser only")
			}
			if err := RejectProposal(ls, in.ID, in.Reason); err != nil {
				return nil, okOut{}, err
			}
			return nil, okOut{OK: true}, nil
		})
}

// ApproveResult carries what a successful ApproveProposal promoted: the slug
// the guidance landed under in memory (empty when skill_only), and the skill
// artifact's path + revision (empty/zero when guidance_only or the proposal
// has no drafted skill).
type ApproveResult struct {
	PromotedGuidanceSlug string
	SkillPath            string
	SkillRev             int64
}

// ApproveProposal is the ONLY place a proposal's guidance/skill fans out into
// standing memory and the fleet artifact store — the human gate the learning
// loop's trust model requires. It is used by BOTH the approve_proposal MCP
// tool (registerLearn, above) and the UI's promote endpoint
// (cmd/corral/main.go wires ui.Deps.Promote to this function), so an operator
// clicking "approve" in the browser and an MCP client calling approve_proposal
// run the exact same fan-out.
//
// Retry-safe by CONVERGENCE, not rollback: see the comment on the
// approve_proposal tool registration above — every step here is idempotent
// and ls.Approve runs last, so a re-approve after a partial failure converges
// without duplicating work. Only a PENDING proposal may be approved; the
// status guard below closes the gate once approved.
func ApproveProposal(l *learn.Store, mem *memory.Store, arts *artifacts.Store, tel *telemetry.Store, id int64, actor string, guidanceOnly, skillOnly bool) (ApproveResult, error) {
	p, err := l.ByID(id)
	if err != nil {
		return ApproveResult{}, fmt.Errorf("no proposal %d: %w", id, err)
	}
	if p.Status != learn.StatusPending {
		return ApproveResult{}, fmt.Errorf("proposal #%d is already %s — only a pending proposal can be approved", p.ID, p.Status)
	}

	var out ApproveResult

	if !skillOnly {
		if mem != nil {
			rolesSlug := learnSlugify(p.Roles)
			sigSlug := learnSlugify(p.Signature)
			name := "guidance-" + rolesSlug + "-" + sigSlug
			slug, _, _, err := mem.Add(name, p.Guidance, "promoted guidance ("+p.Signature+")", "guidance", "default", "", true, actor)
			if err != nil {
				log.Printf("learn: approve #%d: guidance promotion (%s) FAILED, proposal stays pending — re-approve to retry: %v", p.ID, name, err)
				return ApproveResult{}, err
			}
			out.PromotedGuidanceSlug = slug
		}
	}

	if !guidanceOnly && p.SkillName != "" {
		if !validSkillName.MatchString(p.SkillName) {
			// Loud + pending, not silently dropped: an operator can inspect
			// and reject the proposal from here, but nothing gets written
			// to an artifact path built from an unvalidated LLM-drafted name.
			err := fmt.Errorf("proposal #%d: skill name %q is invalid — must match ^[a-z0-9][a-z0-9-]*$ (max 64 chars); the herd drafted something that can't become a skill path, reject or re-draft it", p.ID, p.SkillName)
			log.Printf("learn: approve #%d: REFUSED — %v", p.ID, err)
			return ApproveResult{}, err
		}
		if arts != nil {
			path := "skills/" + p.SkillName + "/SKILL.md"
			// updatedTS<=0 falls back to the artifact store's own clock — there
			// is no client mtime here, only the moment of promotion. Put is a
			// no-op (same rev) when the stored content already equals SkillBody,
			// which is what makes a re-approve after a partial failure converge
			// without minting a duplicate revision.
			rev, _, err := arts.Put(path, []byte(p.SkillBody), actor, 0)
			if err != nil {
				log.Printf("learn: approve #%d: skill artifact write (%s) FAILED after guidance landed, proposal stays pending — re-approve to retry (guidance re-add converges): %v", p.ID, path, err)
				return ApproveResult{}, err
			}
			out.SkillPath = path
			out.SkillRev = rev
		}
		if mem != nil {
			if _, _, _, err := mem.Add(p.SkillName, p.SkillBody, "skill: "+firstLine(p.SkillBody), "skill", "default", "", true, actor); err != nil {
				log.Printf("learn: approve #%d: skill memory mirror (%s) FAILED mid-fan-out, proposal stays pending — re-approve to retry: %v", p.ID, p.SkillName, err)
				return ApproveResult{}, err
			}
		}
	}

	if _, err := l.Approve(id); err != nil {
		log.Printf("learn: approve #%d: fan-out landed but the status flip FAILED, proposal stays pending — re-approve to retry (all steps converge): %v", p.ID, err)
		return ApproveResult{}, err
	}
	rec(tel, 0, "proposal_approved", actor, p.Signature, map[string]any{
		"id": p.ID, "guidance_only": guidanceOnly, "skill_only": skillOnly,
	})
	return out, nil
}

// RejectProposal dismisses a pending proposal, recording the reason as the
// suppression baseline for its signature (see learn.Store.Reject). Only a
// pending proposal may be rejected — used by both the reject_proposal MCP
// tool and the UI's reject endpoint.
func RejectProposal(l *learn.Store, id int64, reason string) error {
	p, err := l.ByID(id)
	if err != nil {
		return fmt.Errorf("no proposal %d: %w", id, err)
	}
	if p.Status != learn.StatusPending {
		return fmt.Errorf("proposal #%d is already %s — only a pending proposal can be rejected", p.ID, p.Status)
	}
	return l.Reject(id, reason)
}

type listProposalsIn struct {
	Status string `json:"status,omitempty" jsonschema:"filter: pending|approved|rejected (omit for all)"`
}
type listProposalsOut struct {
	Proposals []learn.Proposal `json:"proposals"`
}

type approveProposalIn struct {
	ID           int64 `json:"id"`
	GuidanceOnly bool  `json:"guidance_only,omitempty" jsonschema:"promote only the guidance, skip the skill"`
	SkillOnly    bool  `json:"skill_only,omitempty" jsonschema:"promote only the skill, skip the guidance"`
}
type approveProposalOut struct {
	OK                   bool   `json:"ok"`
	PromotedGuidanceSlug string `json:"promoted_guidance_slug,omitempty"`
	SkillPath            string `json:"skill_path,omitempty"`
	SkillRev             int64  `json:"skill_rev,omitempty"`
}

type rejectProposalIn struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason,omitempty"`
}

// learnSlugify lowercases s and collapses runs of non-alphanumeric characters
// to a single "-" — used to build the deterministic guidance memory-entry
// name from a proposal's roles + signature.
func learnSlugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// firstLine returns the first non-empty, non-heading-hash line of s — used as
// a short description when mirroring a skill body into memory.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimLeft(line, "# ")
	}
	return ""
}
