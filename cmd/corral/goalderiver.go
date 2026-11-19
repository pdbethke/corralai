// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
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
//
// gp2 (ruling R28): the prompt text did not change, but what a cached row
// can mean did. Under gp1 an EMPTY derive reply was read as NONE and cached
// as ungoaled, so a cache written then can answer "no testable property"
// for a file whose seat simply produced nothing (B6), with no model asked.
// Since 85f5afae an empty reply is a seat failure and is never cached; the
// bump keys every gp1 row apart, so none of them is served.
const GoalPromptRev = "gp2"

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

// roleGoalDeriver names the derive seat's rows in scan_model_calls and on the
// end-of-scan cost line. The seat has no advpool role (it runs before the
// pool, once per candidate file), so it carries its own name.
const roleGoalDeriver = "goal-deriver"

// llmDeriver asks one model for a file's goal. usage, when set, records what
// every call that reached the provider spent, by candidate path. It used to
// read the reply's content and drop its Usage, so a repo scan's recorded cost
// silently left out one call per candidate file, the one seat in the scan
// whose spend nothing measured.
type llmDeriver struct {
	b     agentbackend.Backend
	model string
	usage *deriveUsage
	// budget is the run's --max-tokens cap, the SAME TokenBudget every
	// grading seat's meteredChatter consults: consulted before each call,
	// charged after it. nil = no cap. The deriver is a seat, and the flag
	// promises "every seat"; it used to be the one seat the cap never saw.
	budget *agentbackend.TokenBudget
}

// withDeriveBudget puts a deriver under the run's token cap. A deriver that
// is not an llmDeriver (a test double, a file source) is returned as is —
// it makes no model call for a cap to bound.
func withDeriveBudget(d reposcan.Deriver, budget *agentbackend.TokenBudget) reposcan.Deriver {
	if ld, ok := d.(llmDeriver); ok {
		ld.budget = budget
		return ld
	}
	return d
}

// usageReporter is implemented by a deriver that records its spend; a test
// double or an unmetered deriver simply does not, and contributes no rows.
type usageReporter interface {
	modelCallRows() []scanstore.ModelCall
}

// deriveUsage is the per-path record of the deriver's calls. Derivation can
// run concurrently across candidates, so it is guarded.
type deriveUsage struct {
	mu     sync.Mutex
	byPath map[string]*scanstore.ModelCall
}

func newMeteredDeriver(b agentbackend.Backend, model string) reposcan.Deriver {
	return llmDeriver{b: b, model: model, usage: &deriveUsage{byPath: map[string]*scanstore.ModelCall{}}}
}

// add records one call that reached the provider. A failed call is never
// passed here: it reported nothing, and a zero would read as measured.
func (u *deriveUsage) add(path, model string, got agentbackend.Usage, wall time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	row, ok := u.byPath[path]
	if !ok {
		row = &scanstore.ModelCall{Path: path, Role: roleGoalDeriver, Model: model}
		u.byPath[path] = row
	}
	row.Calls++
	row.WallMillis += wall.Milliseconds()
	if got.Unmetered {
		// The call ran and its wire reported nothing: counted, never summed
		// (agentbackend.Usage.Unmetered).
		row.UnmeteredCalls++
		return
	}
	row.InputTokens += int64(got.InputTokens)
	row.OutputTokens += int64(got.OutputTokens)
	row.CachedInputTokens = addCacheCount(row.CachedInputTokens, got.CachedInputTokens)
	row.CacheWriteInputTokens = addCacheCount(row.CacheWriteInputTokens, got.CacheWriteInputTokens)
}

// deriverSet is every deriver one scan built; its rows are all of theirs.
type deriverSet []reposcan.Deriver

func (s deriverSet) modelCallRows() []scanstore.ModelCall {
	var out []scanstore.ModelCall
	for _, d := range s {
		if r, ok := d.(usageReporter); ok {
			out = append(out, r.modelCallRows()...)
		}
	}
	return out
}

func (d llmDeriver) modelCallRows() []scanstore.ModelCall {
	if d.usage == nil {
		return nil
	}
	d.usage.mu.Lock()
	defer d.usage.mu.Unlock()
	out := make([]scanstore.ModelCall, 0, len(d.usage.byPath))
	for _, r := range d.usage.byPath {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// newLLMDeriver builds the derive seat from the routing the resolver already
// decided (resolveSeats), through the one constructor every grading seat
// uses (agentbackend.Build). The seat is NOT re-guessed here from its model's
// name or from MODEL_BACKEND: this door used to carry its own copy of that
// guesswork, which is how it came to honour a registry endpoint and a pinned
// gateway differently from the grading seats, and to read keys and URLs the
// resolver had been told not to (an OpenAI secret must never reach a pinned
// openrouter.ai). A seat with no usable routing refuses here, naming the flag.
func newLLMDeriver(rs agentbackend.ResolvedSeat) (reposcan.Deriver, error) {
	if rs.Kind == "agent" && rs.Endpoint != "" {
		// A coding agent is a CLI with its own login, so a daemon endpoint
		// cannot apply to it. Build ignores the field, so the contradiction
		// is refused here rather than silently dropped.
		return nil, fmt.Errorf("goal deriver: the registry places the derive seat on a local daemon (%s), but %q is an agent seat that runs as a CLI — remove the endpoint, or give this seat a local model", rs.Endpoint, rs.Spec)
	}
	b, err := agentbackend.Build(rs, agentbackend.SeatOptions{})
	if err != nil {
		return nil, fmt.Errorf("goal deriver: %w", err)
	}
	return newMeteredDeriver(b, rs.Spec), nil
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
	// The cap, exactly as meteredChatter applies it to the grading seats:
	// checked BEFORE the provider is dialled, so a refused call costs
	// nothing, and charged after with whatever the reply reported.
	if !d.budget.Allow() {
		return "", false, agentbackend.ErrTokenBudgetExhausted
	}
	start := time.Now()
	reply, err := d.b.Chat([]agentbackend.Message{
		{Role: "system", Content: goalDeriverSystem},
		{Role: "user", Content: user},
	}, nil)
	d.budget.Charge(reply.Usage)
	if err != nil {
		// Transport/provider failure — the caller turns this into
		// derive-failed, never ungoaled.
		return "", false, err
	}
	if d.usage != nil {
		d.usage.add(c.Path, d.model, reply.Usage, time.Since(start))
	}
	text := strings.TrimSpace(reply.Content)
	if text == "" {
		// The SEAT produced nothing — never the file's property. Only an
		// explicit NONE says there is no testable property; an empty reply
		// read as NONE once excluded a 41 KB service as "purely
		// declarative" (B6). An error makes the file derive-failed, naming
		// the seat.
		return "", false, fmt.Errorf("the derive seat %s returned an empty reply for %s", d.model, c.Path)
	}
	if strings.EqualFold(text, "NONE") {
		return "", false, nil
	}
	return text, true, nil
}
