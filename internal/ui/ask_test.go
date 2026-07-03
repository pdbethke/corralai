// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/memory"
)

func TestBuildTrailIncludesAttributedMemories(t *testing.T) {
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	// No SetEmbedder => keyword path (graceful degradation); the slug still matches.
	mem.Add("eval-vuln", "eval() on unsanitized parser input", "a vuln", "lesson", "default", filepath.Join(dir, "mem"), true, "Hawk")

	s := &Server{mem: mem}
	trail := s.buildTrail("Hawk", "pentester", "eval unsanitized parser")
	if !strings.Contains(trail, "eval-vuln") || !strings.Contains(trail, "your own") {
		t.Fatalf("trail should include Hawk's own memory flagged 'your own':\n%s", trail)
	}
}
