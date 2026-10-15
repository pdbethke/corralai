// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestPoolLinksNestedDepDirs is webmozart/assert (2026-10-15): PHPUnit is
// installed under tools/phpunit/vendor, not the root vendor/, and the test
// command is ./tools/phpunit/vendor/bin/phpunit. Only top-level dep dirs were
// linked into a tree, so in every copy the command did not exist, the
// baseline failed in all six trees and the run fell back to one at a time. A
// git-ignored dep dir at any depth is linked; one a run writes into (.tox)
// still is not.
func TestPoolLinksNestedDepDirs(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"src/Assert.php":              "<?php\n",
		"tools/phpunit/composer.json": "{}\n",
		"pkg/py/a.py":                 "x=1\n",
	}, []string{"vendor/", ".tox/"})
	for _, d := range []string{"vendor/autoload", "tools/phpunit/vendor/bin", "pkg/py/.tox/env"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "tools/phpunit/vendor/bin/phpunit"), "#!/bin/sh\n")

	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 2 {
		t.Fatalf("disclosure = %+v", d)
	}
	if want := []string{"tools/phpunit/vendor", "vendor"}; !reflect.DeepEqual(d.Shared, want) {
		t.Errorf("Shared = %q, want %q", d.Shared, want)
	}
	for _, tree := range p.treeRoots() {
		link := filepath.Join(tree, "tools", "phpunit", "vendor")
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("%s: tools/phpunit/vendor must be a link to the checkout's: %v", tree, err)
		}
		if target != filepath.Join(root, "tools", "phpunit", "vendor") {
			t.Errorf("%s: link -> %q", tree, target)
		}
		mustExist(t, filepath.Join(link, "bin", "phpunit"))
		mustExist(t, filepath.Join(tree, "tools", "phpunit", "composer.json"))
		mustNotExist(t, filepath.Join(tree, "pkg", "py", ".tox"))
	}
}

// A TRACKED file under a nested linked dir is never copied: copying it would
// write through the link into the operator's checkout.
func TestCopyTreeNeverWritesThroughANestedLink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "tools", "x", "vendor", "kept.txt"), "original\n")
	tree := t.TempDir()
	if _, err := copyTree(root, tree, []string{"tools/x/vendor/kept.txt"}, []string{"tools/x/vendor"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "tools", "x", "vendor", "kept.txt"))
	if err != nil || string(b) != "original\n" {
		t.Fatalf("checkout file = %q, %v", b, err)
	}
	if fi, err := os.Lstat(filepath.Join(tree, "tools", "x", "vendor")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("tools/x/vendor in the tree must be the link: %v", err)
	}
}
