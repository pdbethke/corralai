// SPDX-License-Identifier: Elastic-2.0

package memory

import (
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// TestMemoryConcurrentBuildSearchNoRace hammers EnsureBuilt+Search (+Add) from
// several goroutines concurrently. Regression test for H-2: Store had no
// mutex guarding its build state (lastDirs) and the underlying DB connection
// pool, so concurrent EnsureBuilt/Build/Add/SetShared calls (reached from 3
// MCP handlers + 2 UI paths in production) raced. Run with -race.
func TestMemoryConcurrentBuildSearchNoRace(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "proj", "memory")
	writeEntry(t, mem, "race-note", "a note used to seed the corpus", "", "Some body text about racing goroutines.")

	s, err := Open(filepath.Join(root, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Setenv("CORRALAI_MEMORY_DIR", mem)

	// Deliberately do NOT pre-build: the corpus is empty at Open, so every
	// goroutine's EnsureBuilt() sees count()==0 and races to call Build
	// concurrently — the scenario that actually hits production (3 MCP
	// handlers + 2 UI paths reaching the same Store).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = s.EnsureBuilt()
			_, _ = s.Search("race", "", "", 5, false)
			_, _, _, _ = s.Add("race-add-"+strconv.Itoa(n), "concurrent add body", "d", "reference", "default", mem, false, "")
		}(i)
	}
	wg.Wait()
}
