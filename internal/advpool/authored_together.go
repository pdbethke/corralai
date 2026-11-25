// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"log"
	"strings"

	"github.com/pdbethke/corralai/internal/clip"
	golang "github.com/pdbethke/corralai/internal/lang"
)

// The values of Verdict.AuthoredTogether. "" is the third: not checked.
const (
	AuthoredTogetherPass = "pass"
	AuthoredTogetherFail = "fail"
)

// CompliantRunner is the optional Scorer extension that runs one test file
// against the UNMUTATED code with the authored command and says whether it
// passed, with the runner's output. JailScorer implements it; a Scorer that
// does not leaves the together check unrun, never passed.
type CompliantRunner interface {
	CompliantRun(ctx context.Context, codePath, code, test, testCmd string) (pass bool, out string, err error)
}

// checkAuthoredTogether runs the merged proven file once, whole, on the
// unmutated code. Per-survivor proofs each run their test alone, which is
// what makes a proof name its survivor — and is also why two proven tests
// that interfere (shared global state, a fixture one leaves behind) were
// never seen together. One extra suite run per file with two or more proven
// tests; a disclosure on the verdict, never a change to what was proven.
func (d *Driver) checkAuthoredTogether(ctx context.Context, run *runState) {
	if run.provenMissed < 2 || strings.TrimSpace(run.authoredTest) == "" {
		return
	}
	cr, ok := d.Scorer.(CompliantRunner)
	if !ok {
		return
	}
	pass, out, err := cr.CompliantRun(ctx, run.rs.CodePath, run.rs.Code, run.authoredTest, run.rs.TestCmd)
	if err != nil {
		// Could not run it: not checked, and said so — never a pass.
		log.Printf("advpool: %s: could not run the %d proven tests together on the unmutated code: %v — not checked",
			run.rs.CodePath, run.provenMissed, err)
		return
	}
	if pass {
		run.authoredTogether = AuthoredTogetherPass
		return
	}
	run.authoredTogether = AuthoredTogetherFail
	run.authoredTogetherOutput = authoredTogetherOutput(out)
	log.Printf("advpool: %s: the %d proven tests each pass alone but FAIL when run together on the unmutated code (interference, or a broken merge); see authored_together_output",
		run.rs.CodePath, run.provenMissed)
}

// authoredTogetherOutput is what a failing together check keeps for the
// record (authored_together_output): the last 2000 bytes of what the runner
// printed (lang.PrintedOutput), without the record a record hook prints
// after the run, which on its own would fill those 2000 bytes.
func authoredTogetherOutput(out string) string {
	return clip.Tail(strings.TrimSpace(golang.PrintedOutput(out)), 2000)
}
