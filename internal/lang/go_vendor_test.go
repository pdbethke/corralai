// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestGoVendor pins when the Go plugin asks for vendoring: a module with no
// vendor/ only. It never runs the command; it only names it.
func TestGoVendor(t *testing.T) {
	var v Vendorer = goPlugin{}

	mod := t.TempDir()
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	step, ok := v.Vendor(mod)
	if !ok || !slices.Equal(step.Argv, []string{"go", "mod", "vendor"}) || step.StdoutAppend != "" {
		t.Fatalf("module without vendor/: got %+v, %v", step, ok)
	}

	if err := os.MkdirAll(filepath.Join(mod, "vendor"), 0o750); err != nil {
		t.Fatal(err)
	}
	if step, ok := v.Vendor(mod); ok || step.Argv != nil {
		t.Fatalf("already vendored: got %+v, %v", step, ok)
	}

	if step, ok := v.Vendor(t.TempDir()); ok || step.Argv != nil {
		t.Fatalf("no go.mod: got %+v, %v", step, ok)
	}
}
