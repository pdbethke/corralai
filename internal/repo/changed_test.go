// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestChangedFiles(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // helper from repo_test.go
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	_ = e.Checkout(ctx, dest, "feature")
	if err := os.WriteFile(filepath.Join(dest, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "add new"); err != nil {
		t.Fatal(err)
	}
	changed, err := e.ChangedFiles(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range changed {
		if f == "new.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ChangedFiles missing new.go: %v", changed)
	}
}

func TestChangedFilesRange(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // helper from repo_test.go
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	_ = e.Checkout(ctx, dest, "feature")
	// Two phase commits, mirroring how the mission engine commits per-phase.
	if err := os.WriteFile(filepath.Join(dest, "phase1.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "phase1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "phase2.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "phase2"); err != nil {
		t.Fatal(err)
	}
	// ChangedFiles (last commit only) must miss phase1.go.
	last, err := e.ChangedFiles(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range last {
		if f == "phase1.go" {
			t.Fatalf("ChangedFiles unexpectedly includes phase1.go (should be last-commit only): %v", last)
		}
	}
	// ChangedFilesRange against base must see BOTH phase commits.
	all, err := e.ChangedFilesRange(ctx, dest, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"phase1.go": false, "phase2.go": false}
	for _, f := range all {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("ChangedFilesRange missing %s: %v", f, all)
		}
	}
}
