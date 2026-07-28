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
const goalDeriverSystem = `You state the single most important correctness or security property that a piece of source code must satisfy.

Answer with ONE sentence naming the property, in the imperative — for example "must never return a negative balance" or "must reject a token whose signature does not verify".

Do not describe what the code does. Do not mention tests. Do not explain your reasoning. If the file has no meaningful correctness property (it is generated, trivial, or purely declarative), answer exactly: NONE`

type llmDeriver struct{ b agentbackend.Backend }

// newLLMDeriver routes to the vendor that owns the model and fails closed when
// that vendor's credential is absent.
func newLLMDeriver(model string) (reposcan.Deriver, error) {
	b, err := agentbackend.ForModel(model)
	if err != nil {
		return nil, fmt.Errorf("goal deriver: %w", err)
	}
	return llmDeriver{b: b}, nil
}

func (d llmDeriver) Derive(ctx context.Context, c reposcan.Candidate, source string) (string, bool, error) {
	user := fmt.Sprintf("File: %s\nLanguage: %s\n\n%s", c.Path, c.Lang, source)
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
