// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"fmt"
	"os"
	"testing"
)

// TestMain redirects memory.Add's default target dir (internal/memory's
// lazy-resolved CORRALAI_MEMORY_DIR) to a throwaway temp dir before any test
// runs. Several tests in this package exercise add_memory/promote_memory
// paths (internal/brain/memory.go, learn.go, seeddocs.go) with targetDir=""
// — the normal production path — which without this redirect writes into
// the developer's real ~/.claude/projects/default/memory and pollutes future
// test runs and recall.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "corralai-brain-test-memory-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	_ = os.Setenv("CORRALAI_MEMORY_DIR", dir)

	code := m.Run()
	_ = os.RemoveAll(dir) // best-effort; os.Exit below skips defers
	os.Exit(code)
}
