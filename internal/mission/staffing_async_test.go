// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
)

// countingFailLLM always fails Judge's Generate call and counts how many times it
// was invoked. calls is touched by the async staffing goroutine and read by the
// test, so it uses atomics to stay race-clean.
type countingFailLLM struct {
	calls int64
}

func (c *countingFailLLM) Generate(ctx context.Context, system, prompt string) (string, error) {
	atomic.AddInt64(&c.calls, 1)
	return "", errors.New("boom")
}

func (c *countingFailLLM) Available() bool { return true }

// TestStaffingDoesNotReprobeEveryTickOnFailure asserts that a permanently-failing
// staffing Judge is retried at most maxStaffAttempts times across many ticks —
// not once per tick — so a bad probe backs off and gives up instead of burning a
// 30s LLM round-trip every tick and head-of-line blocking other missions.
func TestStaffingDoesNotReprobeEveryTickOnFailure(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	llm := &countingFailLLM{}
	e.Staffing = &StaffingManager{
		LLM:        llm,
		Perf:       &fakePerf{},
		RoleModels: rolemodel.New(),
	}

	for i := 0; i < 10; i++ {
		_ = e.Tick()
		e.waitStaffingIdle() // block until the async staffing pass settles
	}

	if got := atomic.LoadInt64(&llm.calls); got > maxStaffAttempts {
		t.Fatalf("staffing probed %d times across 10 ticks; want <= %d (backoff/give-up)", got, maxStaffAttempts)
	}
}
