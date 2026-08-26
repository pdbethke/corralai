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
