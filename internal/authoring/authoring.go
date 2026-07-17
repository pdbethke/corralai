// SPDX-License-Identifier: Elastic-2.0

package authoring

import (
	"context"
	"fmt"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/testgen"
)

// authoringTestWriterSystem/authoringMutantSystem are TEMPORARY, byte-for-byte
// duplicates of the go plugin's internal/lang goPlugin.TestWriterSystem() /
// MutantSystem(). authoring cannot import internal/lang directly: lang ->
// controlgate -> authoring -> lang would be an import cycle (controlgate
// already imports authoring; lang already imports controlgate for
// LangScaffold). Task 4 ("wire brain/advpool seam") is expected to thread the
// resolved system prompt down from internal/brain (which can safely import
// both lang and controlgate) through controlgate's request type into
// authoring.Request, at which point these duplicates should be deleted.
// Flagged in task-3-report.md as an open architectural gap — do not let this
// duplication silently drift from the lang plugin's strings.
const authoringTestWriterSystem = `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Go test that verifies the code SATISFIES the goal.
- Same package as the target (white-box).
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library "testing" only. Deterministic, no network.
Return ONLY the raw Go test file content — no prose, no markdown fences.`

const authoringMutantSystem = `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same signature and package (a drop-in replacement that compiles) and must genuinely fail the goal — vary HOW it fails. No no-ops, no compile errors, no tests.
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`

// Request is the input to the authoring-tier loop: a control-owner goal to guard,
// the target source under that goal, and the workspace/command scaffolding
// needed to compile and run a candidate test against it.
type Request struct {
	Goal string
	Code string
	Lang string

	CodePath string
	TestPath string
	Base     map[string]string

	NMutants int
	BuildCmd []string
	TestCmd  []string
}

// Result is the authoring-tier loop's output: the candidate guard test, its
// adequacy report scored over only the mutants that survived compile-verify,
// and the IDs of any mutants discarded for failing to compile.
type Result struct {
	Test      string
	Report    adequacy.Report
	Discarded []string
	// Mutants is the valid, compile-verified mutants that were scored — the
	// invalid/non-compiling ones are in Discarded. Lets a caller recover a
	// surviving mutant's code by its ID for reviewer triage.
	Mutants []adequacy.Mutant
}

// Author runs the authoring-tier loop: extract the target's signature
// surface, write a candidate test against the goal, generate seeded-violation
// mutants, compile-verify them (discarding any that don't compile as a
// drop-in replacement), and score the candidate test's adequacy over only
// the mutants that survived that filter.
//
// LOAD-BEARING: a mutant that fails to compile is discarded by compileVerify
// BEFORE it ever reaches adequacy.Score — feeding a broken mutant to Score
// would count its `go build` failure as a "kill" the test never actually
// earned, inflating the kill rate and corrupting the control owner's adequacy signal.
func Author(ctx context.Context, m testgen.LLM, jail adequacy.Jail, req Request) (Result, error) {
	sigs, err := repoindex.ExtractSignatures(req.Code, req.Lang)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: extract signatures: %w", err)
	}
	test, err := testgen.WriteTest(ctx, m, authoringTestWriterSystem, req.Goal, req.Code, sigs)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: write test: %w", err)
	}
	mutants, err := testgen.GenerateMutants(ctx, m, authoringMutantSystem, req.Goal, req.Code, sigs, req.NMutants)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: generate mutants: %w", err)
	}
	valid, discarded, err := compileVerify(ctx, jail, req.Base, req.CodePath, mutants, req.BuildCmd)
	if err != nil {
		return Result{}, err
	}
	// Score the candidate test against the compliant code + ONLY the valid mutants.
	scoreBase := make(map[string]string, len(req.Base)+1)
	for k, v := range req.Base {
		scoreBase[k] = v
	}
	scoreBase[req.TestPath] = test
	rep, err := adequacy.Score(ctx, jail, scoreBase, req.CodePath, req.Code, valid, req.TestCmd)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: score: %w", err)
	}
	return Result{Test: test, Report: rep, Discarded: discarded, Mutants: valid}, nil
}
