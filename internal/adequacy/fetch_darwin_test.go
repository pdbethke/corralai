// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestFetchStepVendorsGoOnMacOS: the networked fetch step, through the real
// sandbox-exec jail, resolves a module over TLS and writes vendor/ into its
// staging workspace. Exercises layer 8 (network + trustd.agent) and that the
// workspace stays writable although it sits under the $HOME deny when staged
// in ~/Library/Caches. (cargo vendor waits on Rust part 2's RUSTUP_HOME pin;
// see the spec, D4.)
func TestFetchStepVendorsGoOnMacOS(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "sandbox-exec"})
	if err != nil {
		t.Fatalf("resolve sandbox-exec: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module p\ngo 1.21\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte("package p\nimport _ \"golang.org/x/text/language\"\n"), 0o600); err != nil {
		t.Fatalf("write p.go: %v", err)
	}
	ctx := context.Background()
	for _, cmd := range [][]string{{"go", "mod", "tidy"}, {"go", "mod", "vendor"}} {
		if _, res, err := RunFetchStep(ctx, iso, dir, cmd, 3*time.Minute); err != nil || res.ExitCode != 0 {
			t.Fatalf("%v: err=%v exit=%d output=%s", cmd, err, res.ExitCode, res.Output)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "modules.txt")); err != nil {
		t.Fatalf("vendor/modules.txt not written: %v", err)
	}
}
