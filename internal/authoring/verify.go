// SPDX-License-Identifier: Elastic-2.0

// Package authoring composes the corral control-gate authoring loop: signature
// extraction, mutant generation, and adequacy scoring converge here into the
// test-authoring pipeline that produces and validates guard tests.
package authoring

import (
	"context"
	"fmt"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// compileVerify builds each violation-mutant in the jail and discards the
// ones that don't compile.
//
// LOAD-BEARING: a non-compiling mutant fed to adequacy.Score would make the
// test command's `go build` step fail before the test itself ever runs, so
// `go test` would exit non-zero for a reason that has nothing to do with the
// candidate test catching anything — that gets miscounted as a KILL, which
// inflates the kill rate and corrupts the control owner's adequacy signal. Filtering
// mutants down to ones that genuinely compile keeps every subsequent kill
// attributable to the test, not to a broken mutant.
//
// Determinism: valid and discarded are both built by appending in mutants'
// input order — no map iteration, no reordering — so the filtered set is
// reproducible across runs.
func compileVerify(ctx context.Context, jail adequacy.Jail, base map[string]string, codePath string, mutants []adequacy.Mutant, buildCmd []string) ([]adequacy.Mutant, []string, error) {
	var valid []adequacy.Mutant
	var discarded []string
	for _, mut := range mutants {
		ws := make(map[string]string, len(base)+1)
		for k, v := range base {
			ws[k] = v
		}
		ws[codePath] = mut.Code

		compiles, err := jail.RunTest(ctx, ws, buildCmd)
		if err != nil {
			return nil, nil, fmt.Errorf("authoring: compile-verify mutant %s: %w", mut.ID, err)
		}
		if compiles {
			valid = append(valid, mut)
		} else {
			discarded = append(discarded, mut.ID)
		}
	}
	return valid, discarded, nil
}
