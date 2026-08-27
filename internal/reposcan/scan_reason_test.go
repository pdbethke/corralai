// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"errors"
	"fmt"
	"testing"
)

type reasonErr struct{ r string }

func (e reasonErr) Error() string      { return "boom" }
func (e reasonErr) ScanReason() string { return e.r }

// TestExecutorErrReasonPrefersTheSpecificOne pins that a precise refusal keeps
// its identity instead of being flattened into the catch-all.
func TestExecutorErrReasonPrefersTheSpecificOne(t *testing.T) {
	if got := executorErrReason(reasonErr{ReasonTestCmdCannotCollect}); got != ReasonTestCmdCannotCollect {
		t.Errorf("carried reason = %q, want %q", got, ReasonTestCmdCannotCollect)
	}
	// Wrapped, the way a caller would return it.
	wrapped := fmt.Errorf("audit: %w", reasonErr{ReasonTestCmdCannotCollect})
	if got := executorErrReason(wrapped); got != ReasonTestCmdCannotCollect {
		t.Errorf("wrapped reason = %q, want %q", got, ReasonTestCmdCannotCollect)
	}
	// No opinion, and plain errors, both fall back.
	if got := executorErrReason(reasonErr{"  "}); got != ReasonExecutorError {
		t.Errorf("blank reason = %q, want the catch-all", got)
	}
	if got := executorErrReason(errors.New("plain")); got != ReasonExecutorError {
		t.Errorf("plain error = %q, want the catch-all", got)
	}
}
