// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// goalDeriverSystem asks for ONE property, in the register corral's mutant
// generator consumes. It deliberately never mentions tests: the deriver is
// given source only, and asking "what does this code guarantee" rather than
// "what is tested" is what keeps the kill rate measuring a real gap.
// EDITING THIS TEXT REQUIRES BUMPING GoalPromptRev (below): the prompt
// revision is part of the goal cache's key, so a prompt edit that did not
// bump it would let the new prompt silently serve goals the OLD prompt
// produced — a cache hit indistinguishable from a fresh answer, for a
// question that was never actually asked under the new wording.
const goalDeriverSystem = `You state the single most important correctness or security property that a piece of source code must satisfy.

Answer with ONE sentence naming the property, in the imperative — for example "must never return a negative balance" or "must reject a token whose signature does not verify".

Do not describe what the code does. Do not mention tests. Do not explain your reasoning. If the file has no meaningful correctness property (it is generated, trivial, or purely declarative), answer exactly: NONE`

// GoalPromptRev versions the WHOLE prompt a model actually sees —
// goalDeriverSystem above AND goalDeriverUserTemplate below, both. Part of
// the goal cache's key (alongside path, source digest and model) precisely
// so a prompt edit can never miss the bump: any change to either text
// without bumping this constant would let a cached goal from the OLD
// prompt masquerade as an answer to the new one.
const GoalPromptRev = "gp1"

// goalDeriverPromptDigest pins sha256(goalDeriverSystem +
// goalDeriverUserTemplate) — see TestGoalDeriverPromptDigestIsPinned, which
// recomputes that hash and fails CI the moment EITHER text changes without
// this constant changing to match. THE RITUAL, in order: edit the prompt
// text; bump GoalPromptRev above (a bump the goal cache's key needs, which
// nothing in this file can enforce by itself); recompute this digest
// (sha256 of goalDeriverSystem+goalDeriverUserTemplate concatenated,
// hex-encoded) and paste the new value here, in the SAME commit. The test
// enforces only the last step — a prompt edit that changed the text but
// left this digest stale fails CI immediately, forcing the editor back to
// the ritual instead of shipping a prompt whose cache key silently no
// longer matches what it asks.
const goalDeriverPromptDigest = "f044983472f64cacf3a7811747a17b2da099a5487c8835f8f3811e369ec0c14d"

type llmDeriver struct{ b agentbackend.Backend }

// newLLMDeriver routes to the backend that serves the model and fails closed
// when a cloud vendor's credential is absent.
//
// THE SAME RULES AS EVERY GRADING SEAT (localChatterFor). This seat used to
// call ForModelOrLocal directly, which ignores MODEL_BACKEND — so with the
// operator's gateway pinned (MODEL_BACKEND=openrouter, OPENAI_BASE_URL=…),
// the three grading seats went through the gateway and the derive seat
// dialled the vendor directly, demanding the vendor's own key and sending
// every candidate's SOURCE to a host the operator had pinned everything
// away from. And a registry endpoint for this seat was printed, then
// discarded.
//
// In order: endpoint (the registry placed this seat on a daemon) wins; a
// pinned base backend serves a cloud name it fronts (its own vendor, or a
// gateway that fronts every vendor); otherwise the vendor that owns the
// name, or the local daemon for a local name — goal derivation was once
// the only seat that demanded a cloud vendor, against corral's own
// local-first claim, and a summarizing task is one a local model handles.
func newLLMDeriver(model, endpoint string) (reposcan.Deriver, error) {
	if endpoint != "" {
		return llmDeriver{b: agentbackend.NewOllamaBackend(endpoint, model)}, nil
	}
	if v := agentbackend.VendorOf(model); v != "" && backendPinned() && (baseVendor() == "" || baseVendor() == v) {
		base := agentbackend.FromEnv()
		if sw, ok := base.(agentbackend.ModelSwitcher); ok {
			return llmDeriver{b: sw.WithModel(model)}, nil
		}
		return llmDeriver{b: base}, nil
	}
	b, err := agentbackend.ForModelOrLocal(model)
	if err != nil {
		return nil, fmt.Errorf("goal deriver: %w", err)
	}
	return llmDeriver{b: b}, nil
}

// goalDeriverUserTemplate is the format string Derive fills in with the
// candidate's own path/language/source below. EDITING THIS TEXT REQUIRES
// BUMPING GoalPromptRev, exactly like goalDeriverSystem above: the prompt
// revision is part of the goal cache's key over BOTH halves of the prompt a
// model actually sees, not just the system half — a user-template edit that
// did not bump it would let the new wording silently serve goals the OLD
// wording produced, the same silent-drift shape a system-prompt edit could.
// TestGoalDeriverPromptDigestIsPinned hashes goalDeriverSystem plus this
// literal and fails CI if either changes without the constant beside it
// also changing, so this comment is not the only thing standing between an
// editor and a missed bump.
const goalDeriverUserTemplate = "File: %s\nLanguage: %s\n\n%s"

func (d llmDeriver) Derive(ctx context.Context, c reposcan.Candidate, source string) (string, bool, error) {
	user := fmt.Sprintf(goalDeriverUserTemplate, c.Path, c.Lang, source)
	reply, err := d.b.Chat([]agentbackend.Message{
		{Role: "system", Content: goalDeriverSystem},
		{Role: "user", Content: user},
	}, nil)
	if err != nil {
		// Transport/provider failure — the caller turns this into
		// derive-failed, never ungoaled.
		return "", false, err
	}
	text := strings.TrimSpace(reply.Content)
	if text == "" || strings.EqualFold(text, "NONE") {
		return "", false, nil
	}
	return text, true, nil
}
