// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func baseInputs() KeyInputs {
	return KeyInputs{
		SourceDigest:      "src",
		PackageDigest:     "pkg",
		GoalDigest:        "goal",
		TestSurfaceDigest: "tests",
		EngineVersion:     "v0.2.0",
		ModelSet:          "claude,gemini",
		AuditConfig:       "mutants=10",
		Substrate:         "jail",
	}
}

func TestCacheKeyStableForIdenticalInputs(t *testing.T) {
	if baseInputs().CacheKey() != baseInputs().CacheKey() {
		t.Fatal("identical inputs produced different keys")
	}
}

// Every field must participate. A field left out of the hash is an
// under-invalidation bug: a stale verdict served as if it were current.
func TestCacheKeyChangesWhenAnyFieldChanges(t *testing.T) {
	mutators := map[string]func(*KeyInputs){
		"SourceDigest":      func(k *KeyInputs) { k.SourceDigest = "x" },
		"PackageDigest":     func(k *KeyInputs) { k.PackageDigest = "x" },
		"GoalDigest":        func(k *KeyInputs) { k.GoalDigest = "x" },
		"TestSurfaceDigest": func(k *KeyInputs) { k.TestSurfaceDigest = "x" },
		"EngineVersion":     func(k *KeyInputs) { k.EngineVersion = "x" },
		"ModelSet":          func(k *KeyInputs) { k.ModelSet = "x" },
		"AuditConfig":       func(k *KeyInputs) { k.AuditConfig = "x" },
		"Substrate":         func(k *KeyInputs) { k.Substrate = "x" },
	}
	want := baseInputs().CacheKey()
	for field, mutate := range mutators {
		got := baseInputs()
		mutate(&got)
		if got.CacheKey() == want {
			t.Errorf("changing %s did not change the cache key", field)
		}
	}
}

// Field values must not be able to bleed across boundaries — "ab"+"c" and
// "a"+"bc" are different inputs and must hash differently.
func TestCacheKeyIsUnambiguous(t *testing.T) {
	a := baseInputs()
	a.SourceDigest, a.PackageDigest = "ab", "c"
	b := baseInputs()
	b.SourceDigest, b.PackageDigest = "a", "bc"
	if a.CacheKey() == b.CacheKey() {
		t.Fatal("field boundaries are ambiguous: concatenation collision")
	}
}

// A verdict earned in the jail must not silently satisfy a claim of runner
// provenance, or a seal could be assembled from a mix of substrates without
// saying so.
func TestCacheKeySeparatesSubstrates(t *testing.T) {
	jail := baseInputs()
	jail.Substrate = "jail"
	runner := baseInputs()
	runner.Substrate = "workspace"
	if jail.CacheKey() == runner.CacheKey() {
		t.Fatal("jail and workspace verdicts share a cache key")
	}
}

// openRoot opens an *os.Root on dir for the digest helpers, which read only
// through a root so a symlink cannot take the scan outside the repository.
func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// DigestFile must return consistent hashes for identical content.
func TestDigestFileIsConsistent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"test.txt": "hello",
	})
	r := openRoot(t, root)

	d1, err := DigestFile(r, "test.txt")
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}
	d2, err := DigestFile(r, "test.txt")
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}
	if d1 != d2 {
		t.Errorf("identical file produced different digests: %s vs %s", d1, d2)
	}
}

// DigestFile must change when content changes.
func TestDigestFileChangesWithContent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"test.txt": "hello",
	})
	r := openRoot(t, root)

	d1, err := DigestFile(r, "test.txt")
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}

	// Modify the file
	if err := os.WriteFile(filepath.Join(root, "test.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestFile(r, "test.txt")
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}

	if d1 == d2 {
		t.Fatal("different content produced identical digest")
	}
}

// DigestFile must return an error for missing files.
func TestDigestFilePropagatesReadError(t *testing.T) {
	_, err := DigestFile(openRoot(t, t.TempDir()), "nonexistent/file.txt")
	if err == nil {
		t.Fatal("DigestFile should error on nonexistent file")
	}
}

// DigestDir must be consistent for identical directory contents.
func TestDigestDirIsConsistent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "alpha",
		"b.txt": "beta",
	})

	d1, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}
	d2, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}
	if d1 != d2 {
		t.Errorf("identical directory produced different digests: %s vs %s", d1, d2)
	}
}

// DigestDir must change when a file is added to the directory.
func TestDigestDirChangesWhenFileAdded(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "alpha",
	})

	d1, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Add a file to the directory
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	if d1 == d2 {
		t.Fatal("adding a file to directory did not change digest")
	}
}

// DigestDir must change when a file is removed from the directory.
func TestDigestDirChangesWhenFileRemoved(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "alpha",
		"b.txt": "beta",
	})

	d1, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Remove a file from the directory
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	d2, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	if d1 == d2 {
		t.Fatal("removing a file from directory did not change digest")
	}
}

// DigestDir must change when file content changes.
func TestDigestDirChangesWhenContentChanges(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "alpha",
		"b.txt": "beta",
	})

	d1, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Modify a file in the directory
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	if d1 == d2 {
		t.Fatal("changing file content in directory did not change digest")
	}
}

// DigestDir must not descend into subdirectories — it should hash only
// files directly in the target directory.
func TestDigestDirDoesNotDescendSubdirectories(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt":        "alpha",
		"subdir/x.txt": "x",
		"subdir/y.txt": "y",
	})

	d1, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Modify files in the subdirectory
	if err := os.WriteFile(filepath.Join(root, "subdir/x.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Digest should be unchanged because changes are in a subdirectory
	if d1 != d2 {
		t.Fatal("DigestDir should not descend into subdirectories")
	}
}

// DigestDir field boundaries must not collide: name/content pairs with swapped
// boundaries ("ab"+"c" vs "a"+"bc") must hash differently. Uses length-prefixed
// format to prevent concatenation collision.
func TestDigestDirIsUnambiguous(t *testing.T) {
	// Directory 1: files with content that could collide if not length-prefixed
	//   file1: "ab", content: "c"
	//   file2: "d", content: "e"
	root1 := writeTree(t, map[string]string{
		"ab": "c",
		"d":  "e",
	})
	d1, err := DigestDir(openRoot(t, root1), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Directory 2: files with names/contents swapped at boundaries
	//   file1: "a", content: "bc"
	//   file2: "d", content: "e"
	root2 := writeTree(t, map[string]string{
		"a": "bc",
		"d": "e",
	})
	d2, err := DigestDir(openRoot(t, root2), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	if d1 == d2 {
		t.Fatal("DigestDir field boundaries are ambiguous: name/content concatenation collision")
	}
}

// DigestDir must propagate read errors.
func TestDigestDirPropagatesReadError(t *testing.T) {
	_, err := DigestDir(openRoot(t, t.TempDir()), "nonexistent")
	if err == nil {
		t.Fatal("DigestDir should error on nonexistent directory")
	}
}

// TestDigestFileRefusesSymlinkOutOfTree: the digest path is the one that reads
// bytes, so root confinement has to hold HERE too, not only in enumeration.
func TestDigestFileRefusesSymlinkOutOfTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := writeTree(t, map[string]string{"a.txt": "alpha"})
	if err := os.Symlink(outside, filepath.Join(root, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := DigestFile(openRoot(t, root), "leak.txt"); err == nil {
		t.Fatal("DigestFile followed a symlink pointing outside the tree")
	}
}

// TestDigestDirSkipsNonRegularEntries: a symlink's target is not this repo's
// content, and a FIFO in the directory would block the digest forever. Both
// are skipped, and the digest is exactly the digest of the regular files.
func TestDigestDirSkipsNonRegularEntries(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := writeTree(t, map[string]string{"a.txt": "alpha"})
	want, err := DigestDir(openRoot(t, plain), ".")
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	mixed := writeTree(t, map[string]string{"a.txt": "alpha"})
	if err := os.Symlink(outside, filepath.Join(mixed, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(mixed, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan struct{})
	var got string
	var derr error
	go func() {
		got, derr = DigestDir(openRoot(t, mixed), ".")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DigestDir blocked — it opened the FIFO")
	}
	if derr != nil {
		t.Fatalf("DigestDir: %v", derr)
	}
	if got != want {
		t.Errorf("non-regular entries changed the digest: %s vs %s", got, want)
	}
}

// TestDigestFileStreamsLargeFile: contents go into the hash a chunk at a time,
// so one huge fixture is not one huge allocation. Asserted by MEASURING peak
// heap growth across the call, not by inspecting the implementation.
func TestDigestFileStreamsLargeFile(t *testing.T) {
	const size = 64 << 20 // 64 MiB
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := 0; i < size>>20; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := DigestFile(openRoot(t, root), "big.bin"); err != nil {
		t.Fatalf("DigestFile: %v", err)
	}
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > size/4 {
		t.Errorf("DigestFile allocated %d bytes for a %d-byte file — it is not streaming", grew, size)
	}
}
