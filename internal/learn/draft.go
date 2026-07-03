// SPDX-License-Identifier: Elastic-2.0

package learn

import (
	"context"
	"fmt"
	"strings"
)

// draftSystemPrompt instructs the model to phrase — never decide — guidance
// and an optional skill for a clustered proposal. The strict reply format is
// part of the contract: Draft refuses to store anything it cannot parse,
// leaving the proposal untouched and pending.
const draftSystemPrompt = "You distill recurring evidence from a herd of coding agents into reusable knowledge. Reply EXACTLY in this format:\nGUIDANCE: <one sentence of role-scoped guidance>\nSKILL-NAME: <kebab-case-slug or NONE>\nSKILL:\n<markdown skill body, or empty when NONE>"

// Asker is the narrow LLM surface Draft needs; *llm.Client satisfies it.
type Asker interface {
	Ask(ctx context.Context, system, user string) (string, error)
}

// Draft asks a, the LLM, to phrase guidance (and optionally a skill) for p
// from its clustered evidence, then stores the result via s.SetDraft. The
// LLM only phrases text — it never decides whether the proposal is approved.
// Any failure to get a reply or to parse it leaves p untouched and pending;
// the error is surfaced to the caller.
func Draft(ctx context.Context, a Asker, s *Store, p Proposal) error {
	user := fmt.Sprintf("kind: %s\nsignature: %s\nroles: %s\nevidence:\n%s",
		p.Kind, p.Signature, p.Roles, p.Evidence)

	reply, err := a.Ask(ctx, draftSystemPrompt, user)
	if err != nil {
		return fmt.Errorf("learn: draft ask failed for proposal %d: %w", p.ID, err)
	}

	guidance, skillName, skillBody, err := parseDraftReply(reply)
	if err != nil {
		return fmt.Errorf("learn: draft reply for proposal %d unparsable: %w", p.ID, err)
	}

	if err := s.SetDraft(p.ID, guidance, skillName, skillBody); err != nil {
		return fmt.Errorf("learn: storing draft for proposal %d: %w", p.ID, err)
	}
	return nil
}

// parseDraftReply extracts the GUIDANCE, SKILL-NAME, and SKILL sections from
// a model reply following draftSystemPrompt's format. GUIDANCE is required.
// A missing SKILL-NAME/SKILL section, or a SKILL-NAME of NONE, is valid and
// yields empty skill fields — guidance-only proposals are allowed.
func parseDraftReply(reply string) (guidance, skillName, skillBody string, err error) {
	lines := strings.Split(reply, "\n")

	skillIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "GUIDANCE:"):
			guidance = strings.TrimSpace(strings.TrimPrefix(trimmed, "GUIDANCE:"))
		case strings.HasPrefix(trimmed, "SKILL-NAME:"):
			skillName = strings.TrimSpace(strings.TrimPrefix(trimmed, "SKILL-NAME:"))
		case trimmed == "SKILL:":
			skillIdx = i
		}
	}

	if guidance == "" {
		return "", "", "", fmt.Errorf("no GUIDANCE line found in reply")
	}

	if strings.EqualFold(skillName, "NONE") {
		skillName = ""
	}

	if skillName != "" && skillIdx >= 0 && skillIdx+1 < len(lines) {
		skillBody = strings.TrimSpace(strings.Join(lines[skillIdx+1:], "\n"))
	}
	if skillName == "" {
		skillBody = ""
	}

	return guidance, skillName, skillBody, nil
}
