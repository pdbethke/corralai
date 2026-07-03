// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestReportActivityRecordsAndCaps(t *testing.T) {
	ring := NewActivityRing()
	tel, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	// Directly exercise the recording helper this task adds (not the MCP
	// round-trip — that's covered by the wire suite): recordActivity is the
	// function registerActivity's handler calls after ring.Add.
	for i := 0; i < agentActivityCap+5; i++ {
		recordActivity(tel, ring, 42, Activity{Agent: "bee1", Role: "builder", Tool: "run_command", Detail: "go build"})
	}
	n, err := tel.CountKind(42, "agent_activity")
	if err != nil {
		t.Fatal(err)
	}
	if n != agentActivityCap {
		t.Fatalf("agent_activity must be capped at %d, got %d", agentActivityCap, n)
	}
}
