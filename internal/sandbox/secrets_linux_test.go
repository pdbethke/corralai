// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestBwrapMasksSystemCargoSecrets: the bwrap jail applies the masks after
// every bind, so a cargo home's credentials and checkouts read as nothing
// while its bin/ stays usable. Run for real: the stand-in for /usr is bound
// read-only exactly as /usr is.
func TestBwrapMasksSystemCargoSecrets(t *testing.T) {
	iso, err := Resolve(Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: bwrap unavailable: %v", err)
	}
	usr, ch := fakeSystemCargoHome(t)
	t.Setenv("CARGO_HOME", ch)
	old := wholeBoundTrees
	wholeBoundTrees = []string{usr}
	t.Cleanup(func() { wholeBoundTrees = old })

	script := "cat " + filepath.Join(ch, "credentials.toml") + "; cat " + filepath.Join(ch, "config.toml") +
		"; ls -A " + filepath.Join(ch, "git") + "; echo BIN=$(head -c 9 " + filepath.Join(ch, "bin", "cargo") + ")"
	res := Run(context.Background(), script, Options{
		Workspace:     t.TempDir(),
		Backend:       iso,
		ReadOnlyBinds: []Bind{{Host: usr, Target: usr}},
	})
	if strings.Contains(res.Output, "secret") || strings.Contains(res.Output, "checkouts") {
		t.Errorf("a masked secret was readable in the jail:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "BIN=#!/bin/sh") {
		t.Errorf("bin/ must stay readable:\n%s", res.Output)
	}
}
