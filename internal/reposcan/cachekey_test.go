package reposcan

import (
	"os"
	"path/filepath"
	"testing"
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

// DigestFile must return consistent hashes for identical content.
func TestDigestFileIsConsistent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"test.txt": "hello",
	})
	path := filepath.Join(root, "test.txt")

	d1, err := DigestFile(path)
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}
	d2, err := DigestFile(path)
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
	path := filepath.Join(root, "test.txt")

	d1, err := DigestFile(path)
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}

	// Modify the file
	if err := os.WriteFile(path, []byte("world"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestFile(path)
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}

	if d1 == d2 {
		t.Fatal("different content produced identical digest")
	}
}

// DigestFile must return an error for missing files.
func TestDigestFilePropagatesReadError(t *testing.T) {
	_, err := DigestFile("/nonexistent/path/file.txt")
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

	d1, err := DigestDir(root)
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}
	d2, err := DigestDir(root)
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

	d1, err := DigestDir(root)
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Add a file to the directory
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestDir(root)
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

	d1, err := DigestDir(root)
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Remove a file from the directory
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	d2, err := DigestDir(root)
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

	d1, err := DigestDir(root)
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Modify a file in the directory
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestDir(root)
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

	d1, err := DigestDir(root)
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	// Modify files in the subdirectory
	if err := os.WriteFile(filepath.Join(root, "subdir/x.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d2, err := DigestDir(root)
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
	d1, err := DigestDir(root1)
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
	d2, err := DigestDir(root2)
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}

	if d1 == d2 {
		t.Fatal("DigestDir field boundaries are ambiguous: name/content concatenation collision")
	}
}

// DigestDir must propagate read errors.
func TestDigestDirPropagatesReadError(t *testing.T) {
	_, err := DigestDir("/nonexistent/directory")
	if err == nil {
		t.Fatal("DigestDir should error on nonexistent directory")
	}
}
