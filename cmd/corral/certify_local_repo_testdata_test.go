// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSeedCarriesBinaryTestdataFixtures pins the fix for suites that read
// binary fixtures failing their baseline inside the jail.
//
// The seed walk dropped every non-UTF8 file ("binary — the jail workspace is
// text-only"), SILENTLY. A Go suite whose tests open testdata/*.zip then failed
// its unmutated baseline with `no such file or directory`, and the whole repo
// came back `ungradable: baseline-failed` — measured on spf13/afero, where 13
// of 16 files were lost this way.
//
// testdata is the established carve-out in this codebase (see the test-surface
// digest's carve-out and its rationale): a golden fixture is part of what a
// suite measures, so it belongs in the jail.
func TestSeedCarriesBinaryTestdataFixtures(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel string, b []byte) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A PK zip header — genuinely not valid UTF-8.
	zipBytes := []byte{0x50, 0x4b, 0x03, 0x04, 0xff, 0xfe, 0x00, 0x01}

	mustWrite("main.go", []byte("package main\n"))
	mustWrite("zipfs/testdata/small.zip", zipBytes)
	mustWrite("assets/logo.png", zipBytes) // binary NOT under testdata

	files, _, err := loadRepoFiles(root, buildLoadOpts("", nil, false))
	if err != nil {
		t.Fatalf("loadRepoFiles: %v", err)
	}

	if _, ok := files["main.go"]; !ok {
		t.Error("text source was not seeded")
	}
	got, ok := files["zipfs/testdata/small.zip"]
	if !ok {
		t.Fatal("binary fixture under testdata/ was NOT seeded — the baseline will fail on it")
	}
	if got != string(zipBytes) {
		t.Errorf("fixture bytes altered in the seed: got %q, want %q", got, zipBytes)
	}
	if _, ok := files["assets/logo.png"]; ok {
		t.Error("binary OUTSIDE testdata/ was seeded — the carve-out must stay narrow")
	}
}

// The testdata carve-out is Go's convention, and the other languages keep
// fixtures in their test trees: pylint's suite reads
// tests/regrtest_data/unicode/pep_bidirectional_utf_32_bom.txt (UTF-32, not
// valid UTF-8), the seed dropped it, and two tests failed inside the jail
// on unmutated code (pylint-dev/pylint@5e998f68, 2026-10-21). A non-UTF-8
// file in a directory any language names as a test directory is the suite's
// own fixture and is seeded; one outside every test tree still is not.
func TestSeedCarriesNonUTF8FixturesInATestTree(t *testing.T) {
	root := t.TempDir()
	utf32 := []byte{0xff, 0xfe, 0x00, 0x00, 0x41, 0x00, 0x00, 0x00}
	for rel, b := range map[string][]byte{
		"pkg/mod.py": []byte("x = 1\n"),
		"tests/regrtest_data/unicode/pep_bidirectional_utf_32_bom.txt": utf32,
		"spec/fixtures/blob.bin": utf32,
		"src/__tests__/snap.bin": utf32,
		"assets/logo.png":        utf32,
		"contests/data.bin":      utf32, // "tests" as a substring is not a test directory
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, _, err := loadRepoFiles(root, buildLoadOpts("", nil, false))
	if err != nil {
		t.Fatalf("loadRepoFiles: %v", err)
	}
	for _, rel := range []string{"tests/regrtest_data/unicode/pep_bidirectional_utf_32_bom.txt", "spec/fixtures/blob.bin", "src/__tests__/snap.bin"} {
		if got, ok := files[rel]; !ok || got != string(utf32) {
			t.Errorf("%s: a test-tree fixture was not seeded byte for byte (present=%v)", rel, ok)
		}
	}
	for _, rel := range []string{"assets/logo.png", "contests/data.bin"} {
		if _, ok := files[rel]; ok {
			t.Errorf("%s: a non-UTF-8 file outside every test tree was seeded", rel)
		}
	}
}
