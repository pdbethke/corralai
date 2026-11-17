// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// workTargetRejection is the plain-words reason a test that names the
// sandbox's working directory is refused, as the writer is told it. It opens
// every such rejection (WorkTargetError), and compileRepairBlock reads that
// opening to word the repair prompt as a rejection, not a compile error.
const workTargetRejection = "the test names the sandbox's working directory (" + sandbox.WorkTarget + "), which does not exist on the developer's machine; use a path relative to the test file or the repository"

// WorkTargetError is an authored test refused because its source names
// sandbox.WorkTarget. Every jailed run sees its tree there, so such a test
// passes in every run corral makes and fails on the developer's machine;
// before the workspace had one fixed path, a random temp path made it fail
// its next run, and now nothing would. It is a rejection like a compile
// failure: it spends one writer attempt, is fed back to the writer, and the
// test is never proven.
type WorkTargetError struct {
	// Line is the first line of the test that names the path, as written.
	Line string
}

func (e *WorkTargetError) Error() string {
	return fmt.Sprintf("%s. The line: %s", workTargetRejection, e.Line)
}

// checkWorkTarget refuses a test whose source names sandbox.WorkTarget.
func checkWorkTarget(test string) error {
	for _, line := range strings.Split(test, "\n") {
		if strings.Contains(line, sandbox.WorkTarget) {
			return &WorkTargetError{Line: strings.TrimSpace(line)}
		}
	}
	return nil
}

// checkAuthoredTest is every writer door's acceptance check of an authored
// test before it is proven: the sandbox-path refusal, then the language's
// compile check. The primary and challenger writers, batched and per
// survivor, all go through it, so no door accepts a test another refuses.
func (d *Driver) checkAuthoredTest(ctx context.Context, run *runState, test string) error {
	if err := checkWorkTarget(test); err != nil {
		return err
	}
	return d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, test)
}

// writerFeedback is what a rejected test's repair prompt quotes: the
// compiler's own output when the check that refused it ran one, otherwise
// the rejection's own words.
func writerFeedback(err error) string {
	var ce *CompileError
	if errors.As(err, &ce) && strings.TrimSpace(ce.Output) != "" {
		return ce.Output
	}
	return err.Error()
}
