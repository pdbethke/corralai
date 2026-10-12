// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sparseGiB creates a 1 GiB sparse file at p: no disk blocks, but a copy that
// reads it streams a full GiB of zeros, which is what makes a skipped
// directory measurably different from a copied one.
func sparseGiB(t *testing.T, p string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	f, err := os.Create(p) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCopyTreeSkipsCargoTargetBesideManifest: a crate's build directory is
// cargo's own output, often gigabytes, and nothing the audit reads. The
// staging copy must not carry it — proven by timing a tree whose target/
// holds a 1 GiB (sparse) artifact.
func TestCopyTreeSkipsCargoTargetBesideManifest(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "Cargo.toml"), "[package]\nname = \"x\"\n")
	mustWrite(t, filepath.Join(src, "src", "lib.rs"), "pub fn f() {}\n")
	sparseGiB(t, filepath.Join(src, "target", "debug", "big.rlib"))

	dst := t.TempDir()
	start := time.Now()
	if err := copyTreeSkipGit(src, dst); err != nil {
		t.Fatalf("copyTreeSkipGit: %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("copy took %v: target/ beside Cargo.toml was copied, not skipped", el)
	}
	if _, err := os.Stat(filepath.Join(dst, "target")); !os.IsNotExist(err) {
		t.Errorf("target/ beside Cargo.toml must be skipped; stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "src", "lib.rs")); err != nil {
		t.Errorf("src/lib.rs was not copied: %v", err)
	}
}

// TestCopyTreeCopiesTargetWithoutManifest is the negative control: a
// directory merely NAMED target, with no Cargo.toml beside it, is somebody's
// source and is copied.
func TestCopyTreeCopiesTargetWithoutManifest(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "target", "__init__.py"), "x = 1\n")

	dst := t.TempDir()
	if err := copyTreeSkipGit(src, dst); err != nil {
		t.Fatalf("copyTreeSkipGit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "target", "__init__.py")); err != nil {
		t.Errorf("target/ without a sibling Cargo.toml must be copied: %v", err)
	}
}

// TestLoadRepoFilesSkipsCargoTargetBesideManifest: the jail seed leaves out a
// crate's build directory — at the root and in a workspace member — and
// keeps a directory merely named target.
func TestLoadRepoFilesSkipsCargoTargetBesideManifest(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Cargo.toml"), "[workspace]\n")
	mustWrite(t, filepath.Join(root, "target", "debug", "x.d"), "deps\n")
	mustWrite(t, filepath.Join(root, "member", "Cargo.toml"), "[package]\nname = \"m\"\n")
	mustWrite(t, filepath.Join(root, "member", "target", "y.d"), "deps\n")
	mustWrite(t, filepath.Join(root, "member", "src", "lib.rs"), "pub fn f() {}\n")
	mustWrite(t, filepath.Join(root, "py", "target", "__init__.py"), "x = 1\n")

	files, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap"})
	if err != nil {
		t.Fatalf("loadRepoFiles: %v", err)
	}
	for _, skipped := range []string{"target/debug/x.d", "member/target/y.d"} {
		if _, ok := files[skipped]; ok {
			t.Errorf("%s is under a target/ beside Cargo.toml and must not be seeded", skipped)
		}
	}
	for _, kept := range []string{"member/src/lib.rs", "py/target/__init__.py", "Cargo.toml"} {
		if _, ok := files[kept]; !ok {
			t.Errorf("%s must be seeded", kept)
		}
	}
}

// TestLoadRepoFilesBindsCrateVendorDir: `cargo vendor`'s output beside
// Cargo.toml is NOT a build directory — the offline jail resolves the crate's
// dependencies from it — so it is bound read-only as a DepBind (the rust
// plugin declares "vendor" a dependency dir), never skipped and never copied.
func TestLoadRepoFilesBindsCrateVendorDir(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"x\"\n")
	mustWrite(t, filepath.Join(root, "vendor", "libc", "Cargo.toml"), "[package]\nname = \"libc\"\n")

	files, binds, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap"})
	if err != nil {
		t.Fatalf("loadRepoFiles: %v", err)
	}
	if len(binds) != 1 || binds[0].Rel != "vendor" || binds[0].Host != filepath.Join(root, "vendor") {
		t.Errorf("binds = %+v, want exactly vendor bound from %s", binds, filepath.Join(root, "vendor"))
	}
	if _, ok := files["vendor/libc/Cargo.toml"]; ok {
		t.Error("a bound vendor/ must not also be copied into the seed")
	}
}
