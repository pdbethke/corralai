//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

// TestRepoindexNoHNSW verifies two invariants that keep repoindex deliberately
// brute-force by design (it is permanently mission_id-bounded; HNSW adds no
// value and would add complexity):
//
//  1. After indexing real files the chunks table has NO HNSW index.
//  2. The repoindex Go source files do NOT import the annindex package.
//
// If either assertion fails a developer has accidentally added HNSW wiring to
// repoindex — this test acts as a compile-time-and-runtime guard.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoindexNoHNSW(t *testing.T) {
	// ── Guard 1: no HNSW index in the chunks table ────────────────────────────
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "guard.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Index a tiny synthetic file so the chunks table is non-empty (an HNSW index
	// would only be created after rows exist, so this is the fair test point).
	if err := s.IndexFiles(1, []FileInput{{Path: "guard.go", Text: "package guard\nfunc Noop() {}\n"}}); err != nil {
		t.Fatalf("IndexFiles: %v", err)
	}

	var cnt int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM duckdb_indexes WHERE table_name='chunks'`,
	).Scan(&cnt); err != nil {
		t.Fatalf("duckdb_indexes query: %v", err)
	}
	if cnt != 0 {
		t.Errorf("repoindex must have NO indexes on chunks, found %d (HNSW wiring detected)", cnt)
	}

	// ── Guard 2: no annindex import in any repoindex source file ─────────────
	// Runtime file-system check: grep every *.go file in this package directory.
	pkgDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", pkgDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Skip _test.go files: they are outside the production import graph and a
		// guard test file may contain the import string as a literal for the check.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if strings.Contains(string(raw), `"github.com/pdbethke/corralai/internal/annindex"`) {
			t.Errorf("repoindex/%s must NOT import annindex (repoindex stays brute-force by design)", e.Name())
		}
	}
}
