// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// countingFailLLM always fails Judge's Generate call and counts how many times it
// was invoked. calls is read via atomics so a -race run stays clean even if a
// caller drives Staff concurrently.
type countingFailLLM struct {
	calls int64
}

func (c *countingFailLLM) Generate(ctx context.Context, system, prompt string) (string, error) {
	atomic.AddInt64(&c.calls, 1)
	return "", errors.New("boom")
}

func (c *countingFailLLM) Available() bool { return true }

// TestStaffGivesUpAfterMaxAttempts asserts that a permanently-failing staffing
// Judge is retried at most maxStaffAttempts times per Staff call — not
// indefinitely — so a bad probe backs off and gives up instead of burning a 30s
// LLM round-trip forever.
func TestStaffGivesUpAfterMaxAttempts(t *testing.T) {
	llm := &countingFailLLM{}
	mgr := &StaffingManager{
		LLM:        llm,
		Perf:       &fakePerf{},
		RoleModels: rolemodel.New(),
	}

	if err := mgr.Staff("add a wishlist feature"); err == nil {
		t.Fatal("expected Staff to return an error when the Judge backend always fails")
	}

	if got := atomic.LoadInt64(&llm.calls); got != maxStaffAttempts {
		t.Fatalf("staffing probed %d times; want exactly %d (bounded retry then give-up)", got, maxStaffAttempts)
	}
}
