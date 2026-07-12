// SPDX-License-Identifier: Elastic-2.0

package cisogate

import (
	"context"
	"fmt"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/controlspec"
)

// CisoCheck is one CISO-vetted test run against a target's head code: the
// vetted test itself (Goal/Target/Test content), the head code for the
// target file at the PR's head commit, and the workspace paths for both.
type CisoCheck struct {
	Test     controlspec.GateTest // the vetted test (Test content, Goal, Target)
	HeadCode string               // the target file's content at the PR head commit
	CodePath string               // the target's filename in the workspace, e.g. "auth.go"
	TestPath string               // where the vetted test goes, e.g. "auth_ciso_test.go"
}

// CisoTestResult is one check's outcome: the durable Goal/Target it verifies
// and whether the vetted test passed against the head code.
type CisoTestResult struct {
	Goal, Target string
	Passed       bool
}

// CisoResult is the aggregate outcome of a running-tier CISO gate pass.
type CisoResult struct {
	Pass    bool // fail-closed: true ONLY if every check passed
	Results []CisoTestResult
}

// RunCisoGate runs each vetted test against its target's head code in the
// jail and aggregates. Pass is true only if ALL checks passed (empty checks
// → true, vacuously; the caller must treat an uncovered goal as a fail —
// this primitive only reports the checks it was given). A jail error aborts
// and propagates (never a silent pass).
func RunCisoGate(ctx context.Context, jail adequacy.Jail, base map[string]string, checks []CisoCheck, testCmd []string) (CisoResult, error) {
	res := CisoResult{Pass: true}
	for _, c := range checks {
		ws := make(map[string]string, len(base)+2)
		for k, v := range base {
			ws[k] = v
		}
		ws[c.CodePath] = c.HeadCode
		ws[c.TestPath] = c.Test.Test
		passed, err := jail.RunTest(ctx, ws, testCmd)
		if err != nil {
			return CisoResult{}, fmt.Errorf("cisogate: run vetted test for %s@%s: %w", c.Test.Goal, c.Test.Target, err)
		}
		res.Results = append(res.Results, CisoTestResult{Goal: c.Test.Goal, Target: c.Test.Target, Passed: passed})
		if !passed {
			res.Pass = false
		}
	}
	return res, nil
}
