// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestGoVendorCmd pins when the Go plugin asks for vendoring: a module with no
// vendor/ only. It never runs the command; it only names it.
func TestGoVendorCmd(t *testing.T) {
	var v Vendorer = goPlugin{}

	mod := t.TempDir()
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	argv, ok := v.VendorCmd(mod)
	if !ok || !slices.Equal(argv, []string{"go", "mod", "vendor"}) {
		t.Fatalf("module without vendor/: got %v, %v", argv, ok)
	}

	if err := os.MkdirAll(filepath.Join(mod, "vendor"), 0o750); err != nil {
		t.Fatal(err)
	}
	if argv, ok := v.VendorCmd(mod); ok || argv != nil {
		t.Fatalf("already vendored: got %v, %v", argv, ok)
	}

	if argv, ok := v.VendorCmd(t.TempDir()); ok || argv != nil {
		t.Fatalf("no go.mod: got %v, %v", argv, ok)
	}
}
