// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentsetup"
)

// TestCheckMCPAgainstBuiltCorral proves the check against the real server, not
// a script that merely says what the check wants to hear.
func TestCheckMCPAgainstBuiltCorral(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH, so the corral binary cannot be built for this test")
	}
	gomod, err := exec.Command(goBin, "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(strings.TrimSpace(string(gomod)))
	bin := filepath.Join(t.TempDir(), "corral")
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/corral") // #nosec G204 -- fixed argv, test build
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// corral mcp opens the local findings store; keep it off the real state dir.
	t.Setenv("CORRALAI_STATE_DIR", t.TempDir())
	if err := agentsetup.CheckMCP(context.Background(), bin); err != nil {
		t.Fatalf("CheckMCP on the built corral: %v", err)
	}
	if err := agentsetup.CheckMCP(context.Background(), "/bin/true"); err == nil {
		t.Fatal("/bin/true is not an MCP server; CheckMCP must say so")
	}
}
