// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pdbethke/corralai/internal/authoring"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/testgen"
)

// stager stages a candidate from a fully-built request. In production it's a
// closure over controlgate.StageCandidate bound to the model + jail; tests stub it.
type stager func(ctx context.Context, req controlgate.StageRequest) (controlspec.GateTest, error)

type stageControlOut struct {
	Goal     string            `json:"goal"`
	Target   string            `json:"target"`
	KillRate float64           `json:"kill_rate"`
	Survived []string          `json:"survived"`
	Triage   []testgen.Verdict `json:"triage"`
	Vetted   bool              `json:"vetted"`
}

// stageControl looks up the goal, builds the StageRequest (goal INTENT + the
// LangScaffold recipe + the workspace paths), stages a candidate, and shapes a
// summary. The candidate is stored UNVETTED by StageCandidate.
func stageControl(ctx context.Context, store *controlspec.Store, stage stager,
	owner, goalID, target, code, lang, codePath, testPath string, nMutants int, now time.Time) (stageControlOut, error) {
	g, ok, err := store.GetGoal(owner, goalID)
	if err != nil {
		return stageControlOut{}, err
	}
	if !ok {
		return stageControlOut{}, fmt.Errorf("controltools: no goal %q for this owner", goalID)
	}
	base, testCmd, ok := controlgate.LangScaffold(lang)
	if !ok {
		return stageControlOut{}, fmt.Errorf("controltools: unsupported lang %q", lang)
	}
	if nMutants <= 0 {
		nMutants = 5
	}
	req := controlgate.StageRequest{
		Request: authoring.Request{
			Goal: g.Intent, Code: code, Lang: lang, CodePath: codePath, TestPath: testPath,
			Base: base, TestCmd: testCmd, NMutants: nMutants,
		},
		Owner: owner, GoalID: goalID, Target: target, Now: now,
	}
	gt, err := stage(ctx, req)
	if err != nil {
		return stageControlOut{}, err
	}
	var verdicts []testgen.Verdict
	if gt.VerdictsJSON != "" {
		_ = json.Unmarshal([]byte(gt.VerdictsJSON), &verdicts)
	}
	return stageControlOut{Goal: goalID, Target: target, KillRate: gt.KillRate,
		Survived: gt.Survived, Triage: verdicts, Vetted: false}, nil
}

// getControl returns one PENDING (unvetted) candidate by (goal,target) for the
// owner to read as code. ListPending returns full rows (test + survivors +
// verdicts); GetVetted can't serve unvetted rows, so we filter ListPending.
func getControl(store *controlspec.Store, owner, goal, target string) (controlspec.GateTest, error) {
	pend, err := store.ListPending(owner)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	for _, gt := range pend {
		if gt.Goal == goal && gt.Target == target {
			return gt, nil
		}
	}
	return controlspec.GateTest{}, fmt.Errorf("controltools: no pending candidate %s@%s", goal, target)
}
