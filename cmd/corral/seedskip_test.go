// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// REA's acceptance tests run dist/cli-runtime/cli.js (2.7 MiB). The seed left
// it out without a word, and five runs went COULD-NOT-GRADE before anyone
// knew why (2026-10-29). Every file the walk leaves out is now returned, with
// its size and the reason, so the run can say so.
func TestLoadRepoFilesReturnsWhatItLeftOut(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/a.py":       "x = 1\n",
		"dist/cli.js":    strings.Repeat("a", 1<<20+1),
		"assets/bin.dat": "\xff\xfe\x00",
	})
	files, _, skipped, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", SeedLimit: seedLimitBuiltin})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["dist/cli.js"]; ok {
		t.Error("an over-limit file must not be seeded")
	}
	want := map[string]seedSkip{
		"dist/cli.js":    {Path: "dist/cli.js", Size: 1<<20 + 1, Why: skipOverSize},
		"assets/bin.dat": {Path: "assets/bin.dat", Size: 3, Why: skipNotText},
	}
	if len(skipped) != len(want) {
		t.Fatalf("skipped = %+v, want exactly %+v", skipped, want)
	}
	for _, s := range skipped {
		if want[s.Path] != s {
			t.Errorf("skipped %+v, want %+v", s, want[s.Path])
		}
	}
}

func TestBuildRepoSeedSaysWhatTheJailCopyLeavesOut(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/a.py":    "x = 1\n",
		"dist/cli.js": strings.Repeat("a", 3<<20),
		"logo.png":    "\x89PNG\xff",
	})
	var out bytes.Buffer
	seed, err := buildRepoSeed("corral certify --repo", root, "python", namedIsolator("bwrap"), nil, false, seedLimitBuiltin, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.cleanup()
	got := out.String()
	for _, w := range []string{"corral certify --repo:", "leaves out 2 file(s)", "dist/cli.js 3.0 MiB", skipOverSize, "logo.png", skipNotText} {
		if !strings.Contains(got, w) {
			t.Errorf("output %q lacks %q", got, w)
		}
	}
}

func TestBuildRepoSeedIsSilentWhenNothingIsLeftOut(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/a.py": "x = 1\n"})
	var out bytes.Buffer
	seed, err := buildRepoSeed("corral certify --repo", root, "python", namedIsolator("bwrap"), nil, false, seedLimitBuiltin, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.cleanup()
	if out.Len() != 0 {
		t.Errorf("nothing was left out, but the seed printed %q", out.String())
	}
}

func TestSeedSkipsLineCapsTheNamesItLists(t *testing.T) {
	var skips []seedSkip
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		skips = append(skips, seedSkip{Path: p, Size: 2 << 20, Why: skipOverSize})
	}
	got := seedSkipsLine("corral certify --repo", skips)
	if !strings.Contains(got, "and 2 more") || strings.Contains(got, " d ") {
		t.Errorf("line %q should name three and count the rest", got)
	}
}
