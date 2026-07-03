// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"fmt"
	"os"
	"testing"
)

// TestMain redirects memory.Add's default target dir (internal/memory's
// lazy-resolved CORRALAI_MEMORY_DIR) to a throwaway temp dir before any test
// runs. TestProposalApproveEndpointPromotesViaCallback (ui_test.go) drives
// brain.ApproveProposal end-to-end, which calls memory.Add with targetDir=""
// — the normal production path — which without this redirect writes into
// the developer's real ~/.claude/projects/default/memory and pollutes future
// test runs and recall.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "corralai-ui-test-memory-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ui TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	_ = os.Setenv("CORRALAI_MEMORY_DIR", dir)

	code := m.Run()
	_ = os.RemoveAll(dir) // best-effort; os.Exit below skips defers
	os.Exit(code)
}
