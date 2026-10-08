// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The console release public key has ONE source, the committed
// deploy/console-release.pub. A secret holding a second copy can be rotated
// alone, and then a release and a deploy verify the console against
// different keys. Derived from the tree: every workflow is checked.
func TestConsoleReleaseKeyHasOneSource(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "secrets.CORRALAI_CONSOLE_PUBKEY") {
			t.Errorf("%s reads the console key from a secret; read deploy/console-release.pub", filepath.Base(p))
		}
	}
}
