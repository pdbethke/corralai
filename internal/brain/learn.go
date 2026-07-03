// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/memory"
)

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
	// completed approve can never be re-run.
	mcp.AddTool(s, &mcp.Tool{Name: "approve_proposal",
		Description: "Promote a proposal the herd surfaced: its guidance joins vetted memory (shapes future instructions) and its skill syncs fleet-wide. Superuser only — the human gate of the learning loop."},
		func(_ context.Context, req *mcp.CallToolRequest, in approveProposalIn) (*mcp.CallToolResult, approveProposalOut, error) {
			if !opts.isAdmin(req) {
				return nil, approveProposalOut{}, fmt.Errorf("forbidden: superuser only (approval shapes fleet-wide behavior)")
			}
			p, err := ls.ByID(in.ID)
			if err != nil {
				return nil, approveProposalOut{}, fmt.Errorf("no proposal %d: %w", in.ID, err)
			}
			if p.Status != learn.StatusPending {
				return nil, approveProposalOut{}, fmt.Errorf("proposal #%d is already %s — only a pending proposal can be approved", p.ID, p.Status)
			}

			actorName := actorOf(req)
			var out approveProposalOut

			if !in.SkillOnly {
				if mem != nil {
					rolesSlug := learnSlugify(p.Roles)
					sigSlug := learnSlugify(p.Signature)
					name := "guidance-" + rolesSlug + "-" + sigSlug
					slug, _, _, err := mem.Add(name, p.Guidance, "promoted guidance ("+p.Signature+")", "guidance", "default", "", true, actorName)
					if err != nil {
						log.Printf("learn: approve #%d: guidance promotion (%s) FAILED, proposal stays pending — re-approve to retry: %v", p.ID, name, err)
						return nil, approveProposalOut{}, err
					}
					out.PromotedGuidanceSlug = slug
				}
			}

			if !in.GuidanceOnly && p.SkillName != "" {
				if arts != nil {
					path := "skills/" + p.SkillName + "/SKILL.md"
					// updatedTS<=0 falls back to the artifact store's own clock — there
					// is no client mtime here, only the moment of promotion. Put is a
					// no-op (same rev) when the stored content already equals SkillBody,
					// which is what makes a re-approve after a partial failure converge
					// without minting a duplicate revision.
					rev, _, err := arts.Put(path, []byte(p.SkillBody), actorName, 0)
					if err != nil {
						log.Printf("learn: approve #%d: skill artifact write (%s) FAILED after guidance landed, proposal stays pending — re-approve to retry (guidance re-add converges): %v", p.ID, path, err)
						return nil, approveProposalOut{}, err
					}
					out.SkillPath = path
					out.SkillRev = rev
				}
				if mem != nil {
					if _, _, _, err := mem.Add(p.SkillName, p.SkillBody, "skill: "+firstLine(p.SkillBody), "skill", "default", "", true, actorName); err != nil {
						log.Printf("learn: approve #%d: skill memory mirror (%s) FAILED mid-fan-out, proposal stays pending — re-approve to retry: %v", p.ID, p.SkillName, err)
						return nil, approveProposalOut{}, err
					}
				}
			}

			if _, err := ls.Approve(in.ID); err != nil {
				log.Printf("learn: approve #%d: fan-out landed but the status flip FAILED, proposal stays pending — re-approve to retry (all steps converge): %v", p.ID, err)
				return nil, approveProposalOut{}, err
			}
			rec(opts.Telemetry, 0, "proposal_approved", actorName, p.Signature, map[string]any{
				"id": p.ID, "guidance_only": in.GuidanceOnly, "skill_only": in.SkillOnly,
			})
			out.OK = true
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reject_proposal",
		Description: "Dismiss a proposal the herd surfaced. Its signature stays suppressed until the underlying evidence doubles, so rejecting noise doesn't reopen a nag loop. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in rejectProposalIn) (*mcp.CallToolResult, okOut, error) {
			if !opts.isAdmin(req) {
				return nil, okOut{}, fmt.Errorf("forbidden: superuser only")
			}
			p, err := ls.ByID(in.ID)
			if err != nil {
				return nil, okOut{}, fmt.Errorf("no proposal %d: %w", in.ID, err)
			}
			if p.Status != learn.StatusPending {
				return nil, okOut{}, fmt.Errorf("proposal #%d is already %s — only a pending proposal can be rejected", p.ID, p.Status)
			}
			if err := ls.Reject(in.ID, in.Reason); err != nil {
				return nil, okOut{}, err
			}
			return nil, okOut{OK: true}, nil
		})
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
