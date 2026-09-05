// SPDX-License-Identifier: Elastic-2.0

package wranglerd

import (
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// TestLearnSweepIgnoresDismissedAndOperationalFindings: the sweep fed EVERY
// finding to the recurrence detector — one an operator had dismissed as
// wrong, and "ops" events (a worker could not reach its model) that are
// not defects — so three model-unreachable notices on one target opened a
// proposal and bought an LLM draft of a lesson nobody learned.
func TestLearnSweepIgnoresDismissedAndOperationalFindings(t *testing.T) {
	fs := []queue.Finding{
		{Type: "bug", Target: "auth.go", Status: queue.FindingOpen, Reporter: "w1", Evidence: "real"},
		{Type: "bug", Target: "auth.go", Status: queue.FindingDismissed, Reporter: "w1", Evidence: "wrong"},
		{Type: "ops", Target: "auth.go", Status: queue.FindingOpen, Reporter: "w2", Evidence: "model unreachable"},
		{Type: "ops", Target: "auth.go", Status: queue.FindingOpen, Reporter: "w2", Evidence: "model unreachable"},
		{Type: "ops", Target: "auth.go", Status: queue.FindingOpen, Reporter: "w2", Evidence: "model unreachable"},
	}
	got := learnSignalsFrom(fs, func(r string) string { return "role-" + r })
	if len(got) != 1 || got[0].Evidence != "real" || got[0].Role != "role-w1" {
		t.Fatalf("signals = %+v, want exactly the one open, non-operational finding", got)
	}
}
