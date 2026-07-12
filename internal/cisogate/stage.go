// SPDX-License-Identifier: Elastic-2.0

// Package cisogate is the composition tier that ties the CISO-gate loop
// together: it authors a candidate test (internal/authoring), triages the
// mutants that survived it with an INDEPENDENT reviewer seat
// (internal/testgen), and stages the result — unvetted — for CISO review
// (internal/controlspec). Nothing in this package auto-promotes a
// candidate to gating; that's the human-gate surface (Task 2's Promote),
// out of scope here.
package cisogate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/authoring"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/testgen"
)

// StageRequest is the input to StageCandidate: the authoring-tier request
// (Goal/Code/Lang/CodePath/TestPath/Base/NMutants/BuildCmd/TestCmd) plus the
// CISO-facing identity of the resulting candidate — who owns it, which
// durable control-spec Goal it verifies, and what target it's staged
// against. Now is caller-stamped: StageCandidate never calls time.Now()
// itself, which keeps the composition deterministic under test.
type StageRequest struct {
	authoring.Request
	Owner  string
	GoalID string
	Target string
	Now    time.Time
}

// StageCandidate authors a candidate test, triages its surviving mutants
// with an INDEPENDENT reviewer seat, and stores the candidate UNVETTED for
// CISO review. Returns the stored (unvetted) GateTest.
//
// INDEPENDENCE: writer and reviewer are separate testgen.LLM seats — the
// reviewer never sees the writer's prompt, and vice versa. That separation
// is what makes the triage a genuine third read of the survivors rather
// than the writer grading its own homework.
func StageCandidate(ctx context.Context, writer, reviewer testgen.LLM, jail adequacy.Jail, store *controlspec.Store, req StageRequest) (controlspec.GateTest, error) {
	res, err := authoring.Author(ctx, writer, jail, req.Request)
	if err != nil {
		return controlspec.GateTest{}, err
	}

	// Recover the surviving mutants' code (by ID) from the valid scored set.
	byID := make(map[string]adequacy.Mutant, len(res.Mutants))
	for _, m := range res.Mutants {
		byID[m.ID] = m
	}
	var survivors []adequacy.Mutant
	for _, id := range res.Report.Survived {
		if m, ok := byID[id]; ok {
			survivors = append(survivors, m)
		}
	}

	verdicts, err := testgen.TriageSurvivors(ctx, reviewer, req.Goal, req.Code, res.Test, survivors)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	vj, err := json.Marshal(verdicts) // nil verdicts → "null"; harmless (opaque to the store)
	if err != nil {
		return controlspec.GateTest{}, err
	}

	gt := controlspec.GateTest{
		Owner: req.Owner, Goal: req.GoalID, Target: req.Target,
		Test: res.Test, KillRate: res.Report.KillRate(),
		Survived: res.Report.Survived, Discarded: res.Discarded,
		VerdictsJSON: string(vj), CreatedTS: req.Now,
	}
	if err := store.SaveCandidate(gt); err != nil {
		return controlspec.GateTest{}, err
	}
	return gt, nil
}
