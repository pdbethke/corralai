// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package adequacy

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/statedir"
)

// prodStagingDir creates a staging dir where production stages vendoring
// (cmd/corral's vendorStagingDir: statedir's Cache root + /vendor, which on
// macOS is ~/Library/Caches/corral/vendor, UNDER the $HOME the jail denies),
// and removes it — and any parent this test created — afterwards.
func prodStagingDir(t *testing.T) string {
	t.Helper()
	cache, err := statedir.Dir(statedir.Cache)
	if err != nil {
		t.Fatalf("statedir cache root: %v", err)
	}
	base := filepath.Join(cache, "vendor")
	var created []string // deepest last
	for _, d := range []string{cache, base} {
		if _, err := os.Stat(d); errors.Is(err, fs.ErrNotExist) {
			created = append(created, d)
		}
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "test-stage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		for i := len(created) - 1; i >= 0; i-- {
			_ = os.Remove(created[i]) // only if still empty
		}
	})
	return dir
}

// TestFetchStepVendorsGoOnMacOS: the networked fetch step, through the real
// sandbox-exec jail, resolves a module over TLS and writes vendor/ into a
// staging dir at the PRODUCTION location (statedir's cache root, under
// ~/Library/Caches), which lies under the $HOME the profile denies.
// Exercises layer 8 (network + trustd.agent), that the workspace allow
// (layer 6) re-opens exactly that staging dir after the home deny (layer 4),
// and — with a file planted directly in $HOME — that it re-opens nothing
// else: the same step that writes vendor/ cannot read its home sibling.
// (cargo vendor waits on Rust part 2's RUSTUP_HOME pin; see the spec, D4.)
func TestFetchStepVendorsGoOnMacOS(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "sandbox-exec"})
	if err != nil {
		t.Fatalf("resolve sandbox-exec: %v", err)
	}
	dir := prodStagingDir(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("no home directory: %v", err)
	}
	if !strings.HasPrefix(dir, home+string(filepath.Separator)) && os.Getenv(statedir.Cache.OverrideVar()) == "" {
		t.Fatalf("staging dir %s is not under $HOME %s: the test no longer covers the production shape", dir, home)
	}
	sib, err := os.CreateTemp(home, ".corral-test-home-sibling-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sib.Name()) })
	if _, err := sib.WriteString("home-sibling-secret\n"); err != nil {
		t.Fatal(err)
	}
	_ = sib.Close()
	if b, err := os.ReadFile(sib.Name()); err != nil || !strings.Contains(string(b), "home-sibling-secret") {
		t.Fatalf("control failed: the home sibling is not readable outside the jail: %v", err)
	}

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

	// The same step, in the same staging dir: it writes vendor/ and cannot
	// read the file beside it in $HOME.
	probe := "cat " + shellQuote(sib.Name()) + " || echo SIBLING-DENIED; echo w > vendor/probe && echo VENDOR-WRITTEN"
	out, res, err := RunFetchStep(ctx, iso, dir, []string{"sh", "-c", probe}, time.Minute)
	if err != nil {
		t.Fatalf("probe: err=%v exit=%d output=%s", err, res.ExitCode, res.Output)
	}
	if strings.Contains(out+res.Output, "home-sibling-secret") || !strings.Contains(out, "SIBLING-DENIED") {
		t.Errorf("the fetch step read a file in $HOME beside its staging dir:\nstdout=%s\noutput=%s", out, res.Output)
	}
	if !strings.Contains(res.Output, "Operation not permitted") {
		t.Errorf("the home sibling must be refused by the profile (Operation not permitted):\n%s", res.Output)
	}
	if !strings.Contains(out, "VENDOR-WRITTEN") {
		t.Errorf("the fetch step could not write vendor/ in its staging dir:\nstdout=%s\noutput=%s", out, res.Output)
	}
}
