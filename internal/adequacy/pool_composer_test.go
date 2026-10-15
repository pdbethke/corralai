// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPoolComposerAutoloaderFindsTheTree is webmozart/assert's second layer
// (2026-10-15). PHP resolves __DIR__ through symlinks, and Composer locates
// the project as two levels above vendor/composer/. With vendor/ linked
// whole, every tree's autoloader therefore pointed at the ORIGINAL checkout's
// src/, every mutant written into a tree went unseen, and the pool's canary
// (rightly) refused to run in parallel. vendor/autoload.php and
// vendor/composer/ are now copied into each tree; every other vendor/ entry
// is third-party code and stays a link.
func TestPoolComposerAutoloaderFindsTheTree(t *testing.T) {
	root := newGitRepo(t, map[string]string{"src/A.php": "<?php\n"}, []string{"vendor/"})
	// The shape Composer writes: autoload.php requires composer/, and
	// composer/ finds the project from its own directory.
	writeFile(t, filepath.Join(root, "vendor", "autoload.php"), "<?php require __DIR__ . '/composer/where.php';\n")
	writeFile(t, filepath.Join(root, "vendor", "composer", "where.php"), "<?php echo realpath(dirname(__DIR__, 2) . '/src'), \"\\n\";\n")
	writeFile(t, filepath.Join(root, "vendor", "phpunit", "phpunit", "README"), "third party\n")

	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 2 || len(d.Shared) != 1 || d.Shared[0] != "vendor" {
		t.Fatalf("disclosure = %+v", d)
	}
	for _, tree := range p.treeRoots() {
		for _, copied := range []string{"vendor", "vendor/autoload.php", "vendor/composer"} {
			fi, err := os.Lstat(filepath.Join(tree, copied))
			if err != nil || fi.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("%s: %s must be the tree's own, not a link: %v", tree, copied, err)
			}
		}
		target, err := os.Readlink(filepath.Join(tree, "vendor", "phpunit"))
		if err != nil || target != filepath.Join(root, "vendor", "phpunit") {
			t.Fatalf("%s: vendor/phpunit must stay a link to the checkout's: %q, %v", tree, target, err)
		}
		mustExist(t, filepath.Join(tree, "vendor", "phpunit", "phpunit", "README"))

		php, lerr := exec.LookPath("php")
		if lerr != nil {
			continue // the layout above is the property; the run below is the proof
		}
		cmd := exec.Command(php, "vendor/autoload.php") // #nosec G204 -- php via LookPath, literal args, test fixture
		cmd.Dir = tree
		out, rerr := cmd.CombinedOutput()
		if rerr != nil {
			t.Fatalf("%s: %v\n%s", tree, rerr, out)
		}
		want, _ := filepath.EvalSymlinks(filepath.Join(tree, "src"))
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("the autoloader in %s located the project at %q, want the tree's own %q", tree, got, want)
		}
	}
}
