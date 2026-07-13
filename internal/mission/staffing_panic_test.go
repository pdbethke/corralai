// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// panicLLM always panics from Generate, simulating a broken Judge backend (a
// bad response shape, a nil-pointer deref deep in a vendor SDK, etc). Staff's
// per-attempt recover() must contain this rather than taking the caller down.
type panicLLM struct{}

func (p *panicLLM) Generate(ctx context.Context, system, prompt string) (string, error) {
	panic("boom: judge backend exploded")
}

func (p *panicLLM) Available() bool { return true }

// TestStaffPanicIsRecoveredAndReturnsError asserts that a panic inside the
// Judge call (e.g. a Judge backend that panics instead of returning an error)
// is recovered rather than crashing the process, and that Staff surfaces it as
// a plain error after exhausting its bounded retries.
func TestStaffPanicIsRecoveredAndReturnsError(t *testing.T) {
	mgr := &StaffingManager{
		LLM:        &panicLLM{},
		Perf:       &fakePerf{},
		RoleModels: rolemodel.New(),
	}

	err := mgr.Staff("add a wishlist feature")
	if err == nil {
		t.Fatal("expected Staff to return an error after a panicking Judge backend, got nil")
	}
	if !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("expected a give-up error, got: %v", err)
	}
}
