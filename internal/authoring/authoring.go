// SPDX-License-Identifier: Elastic-2.0

package authoring

import (
	"context"
	"fmt"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/testgen"
)

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
	goP, _ := lang.ByName("go")
	test, err := testgen.WriteTest(ctx, m, goP.TestWriterSystem(), req.Goal, req.Code, sigs)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: write test: %w", err)
	}
	mutants, err := testgen.GenerateMutants(ctx, m, goP.MutantSystem(), req.Goal, req.Code, sigs, req.NMutants)
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
