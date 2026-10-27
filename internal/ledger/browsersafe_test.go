// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// assertBrowserSafe fails if pkg's dependency closure under GOOS=js
// GOARCH=wasm contains anything that cannot run in a browser: the cgo
// database, the keyring, age, os/exec, or an internal package not named in
// allowInternal. (Copied from internal/reviewrecord: a test helper
// cannot be shared across packages without a non-test package, and adding
// one for ten lines is not worth it.)
func assertBrowserSafe(t *testing.T, pkg string, allowInternal ...string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}
	forbidden := []string{
		"github.com/marcboeker/go-duckdb",
		"github.com/zalando/go-keyring",
		"filippo.io/age",
		"os/exec",
		"github.com/pdbethke/corralai/internal/",
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == pkg || allowed(dep, allowInternal) {
			continue
		}
		for _, f := range forbidden {
			if strings.HasPrefix(dep, f) {
				t.Errorf("%s depends on %s (forbidden prefix %s)", pkg, dep, f)
			}
		}
	}
}

func allowed(dep string, allow []string) bool {
	for _, a := range allow {
		if dep == a {
			return true
		}
	}
	return false
}

func TestLedgerIsBrowserSafe(t *testing.T) {
	assertBrowserSafe(t, "github.com/pdbethke/corralai/internal/ledger",
		"github.com/pdbethke/corralai/internal/reviewrecord",
		"github.com/pdbethke/corralai/internal/seatrecord")
}
