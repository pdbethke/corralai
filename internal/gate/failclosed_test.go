// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestFailClosed: FailClosed must record a Passed=false run for the head AND
// post the given non-success state + msg — the single fail-closed exit both
// gate.Runner.fail and brain's controlRunner.fail delegate to.
func TestFailClosed(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	status := &fakeStatusPoster{}
	pr := testPR()
	now := func() time.Time { return time.Unix(2000, 0) }

	err = FailClosed(context.Background(), store, status, "https://github.com/o/r", "o/r", pr, "corral/gate", "http://x/target", "failure", "boom", now)
	if err != nil {
		t.Fatalf("FailClosed: %v", err)
	}

	if len(status.states) != 1 || status.states[0] != "failure" {
		t.Fatalf("expected one posted state 'failure', got %v", status.states)
	}
	if len(status.descs) != 1 || status.descs[0] != "boom" {
		t.Fatalf("expected posted description 'boom', got %v", status.descs)
	}

	run, ok, err := store.GetBySHA("o/r", pr.HeadSHA)
	if err != nil || !ok {
		t.Fatalf("expected a stored run: ok=%v err=%v", ok, err)
	}
	if run.Passed {
		t.Fatalf("FAIL-CLOSED VIOLATION: stored Passed=true, got %+v", run)
	}
}
