// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadRepoFilesBindsDepDirs(t *testing.T) {
	root := t.TempDir()
	// a big node_modules that would blow the 64 MiB cap if copied
	big := strings.Repeat("x", 1<<20) // 1 MiB
	tree := map[string]string{"src/index.js": "code"}
	for i := 0; i < 80; i++ {
		tree[fmt.Sprintf("node_modules/pkg%d/i.js", i)] = big
	} // 80 MiB
	writeTree(t, root, tree)

	files, binds, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap"})
	if err != nil {
		t.Fatalf("should NOT hit the cap (node_modules bound, not copied): %v", err)
	}
	if _, ok := files["src/index.js"]; !ok {
		t.Fatal("source file missing from seed")
	}
	for k := range files {
		if strings.HasPrefix(k, "node_modules/") {
			t.Fatalf("node_modules leaked into the copied seed: %s", k)
		}
	}
	if len(binds) != 1 || binds[0].Rel != "node_modules" {
		t.Fatalf("want 1 node_modules bind, got %+v", binds)
	}
	if !filepath.IsAbs(binds[0].Host) {
		t.Fatalf("bind Host must be absolute: %q", binds[0].Host)
	}
}

func TestLoadRepoFilesNoBindDepsCopiesAndHitsCap(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 1<<20)
	tree := map[string]string{"src/index.js": "code"}
	for i := 0; i < 80; i++ {
		tree[fmt.Sprintf("node_modules/pkg%d/i.js", i)] = big
	}
	writeTree(t, root, tree)
	_, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", NoBindDeps: true})
	if err == nil {
		t.Fatal("with --no-bind-deps the 80 MiB node_modules is copied and must hit the 64 MiB cap")
	}
}

func TestLoadRepoFilesExtraBindDir(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/a.go": "x", "thirdparty/lib.go": "y"})
	_, binds, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", ExtraBindDir: []string{"thirdparty"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range binds {
		if b.Rel == "thirdparty" {
			found = true
		}
	}
	if !found {
		t.Fatalf("--bind-dir thirdparty not bound: %+v", binds)
	}
}

// TestLoadRepoFilesContainerBackendCopiesNonWorldReadableDep proves the
// container-backend degrade-loudly fallback: a dep dir that is NOT
// world-readable can't be traversed by the container's (different) uid via a
// read-only bind, so it must be copied into the seed instead of bound — even
// though it's an auto-detected dep dir name.
func TestLoadRepoFilesContainerBackendCopiesNonWorldReadableDep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on windows")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/index.js":          "code",
		"node_modules/pkg/i.js": "dep code",
	})
	depDir := filepath.Join(root, "node_modules")
	if err := os.Chmod(depDir, 0o700); err != nil {
		t.Fatal(err)
	}

	files, binds, err := loadRepoFiles(root, loadOpts{BackendName: "container"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("non-world-readable dep dir must NOT be bound on container backend, got %+v", binds)
	}
	if _, ok := files["node_modules/pkg/i.js"]; !ok {
		t.Fatalf("non-world-readable dep dir must be copied into the seed instead: %v", keysOf(files))
	}
}

// TestLoadRepoFilesSandboxExecBackendCopiesDeps proves the macOS-broken-bind
// fix (whole-branch review finding #1): sandbox-exec (and any backend other
// than bwrap/container) has no primitive to RELOCATE a dir into the jail
// workspace — it only grants file-read* at the dir's ORIGINAL host path — so
// a dep dir must be COPIED into the seed on that backend, never bound, even
// though it's an auto-detected (and world-readable) dep dir name.
func TestLoadRepoFilesSandboxExecBackendCopiesDeps(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/index.js":          "code",
		"node_modules/pkg/i.js": "dep code",
	})

	files, binds, err := loadRepoFiles(root, loadOpts{BackendName: "sandbox-exec"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("sandbox-exec cannot relocate a dir into the workspace — must NOT bind, got %+v", binds)
	}
	if _, ok := files["node_modules/pkg/i.js"]; !ok {
		t.Fatalf("dep dir must be copied into the seed on sandbox-exec: %v", keysOf(files))
	}
}

// TestLoadRepoFilesBindDirNonCanonicalMatches proves the fail-closed
// normalization fix (whole-branch review finding #2): a --bind-dir entry
// like "./thirdparty" or "thirdparty/" must still match the walk's clean
// slash-separated rel and get bound, not silently no-op.
func TestLoadRepoFilesBindDirNonCanonicalMatches(t *testing.T) {
	cases := []string{"./thirdparty", "thirdparty/", "thirdparty"}
	for _, entry := range cases {
		t.Run(entry, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, map[string]string{"src/a.go": "x", "thirdparty/lib.go": "y"})
			_, binds, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", ExtraBindDir: []string{entry}})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, b := range binds {
				if b.Rel == "thirdparty" {
					found = true
				}
			}
			if !found {
				t.Fatalf("--bind-dir %q not bound (normalization missing): %+v", entry, binds)
			}
		})
	}
}
