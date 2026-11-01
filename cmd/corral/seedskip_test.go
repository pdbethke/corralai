// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// REA's acceptance tests run dist/cli-runtime/cli.js (2.7 MiB). The seed left
// it out without a word, under a 1 MiB per-file limit, and five runs went
// COULD-NOT-GRADE before anyone knew why (2026-10-29). The limit is gone (the
// seed's total, --seed-limit, is what bounds the jail's disk), and every file
// the walk still leaves out is returned, with its size and the reason, so the
// run can say so.
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
	if len(files["dist/cli.js"]) != 1<<20+1 {
		t.Error("a text file over 1 MiB must be seeded whole: a test may run it")
	}
	want := map[string]seedSkip{
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
		"src/a.py":   "x = 1\n",
		"logo.png":   "\x89PNG\xff",
		"docs/b.png": "\x89PNG\xff\xff",
	})
	var out bytes.Buffer
	seed, err := buildRepoSeed("corral certify --repo", root, "python", namedIsolator("bwrap"), nil, false, seedLimitBuiltin, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.cleanup()
	got := out.String()
	for _, w := range []string{"corral certify --repo:", "leaves out 2 file(s)", "docs/b.png", "logo.png", "not UTF-8 text"} {
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
		skips = append(skips, seedSkip{Path: p, Size: 2 << 20, Why: skipNotText})
	}
	got := seedSkipsLine("corral certify --repo", skips)
	if !strings.Contains(got, "and 2 more") || strings.Contains(got, " d ") {
		t.Errorf("line %q should name three and count the rest", got)
	}
}

// With no per-file limit, a file bigger than the seed's remaining room is
// measured as a stream rather than read whole, so one huge file cannot take
// the walk's memory before the total refuses it. The refusal still states
// the tree's real text size, and a binary that size is still left out, not
// counted.
func TestLoadRepoFilesMeasuresAFileOverTheRemainingRoomWithoutHoldingIt(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"big.js": strings.Repeat("a", 3<<20)})
	_, _, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", SeedLimit: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "3.0 MiB of text") || !strings.Contains(err.Error(), "passed at big.js") {
		t.Fatalf("err = %v, want a refusal naming 3.0 MiB of text, passed at big.js", err)
	}

	bin := t.TempDir()
	writeTree(t, bin, map[string]string{"blob.dat": strings.Repeat("\xff", 3<<20), "a.py": "x = 1\n"})
	files, _, skipped, err := loadRepoFiles(bin, loadOpts{BackendName: "bwrap", SeedLimit: 1 << 20})
	if err != nil {
		t.Fatalf("a binary over the room is left out, not counted: %v", err)
	}
	if files["a.py"] == "" || len(skipped) != 1 || skipped[0] != (seedSkip{Path: "blob.dat", Size: 3 << 20, Why: skipNotText}) {
		t.Errorf("files = %d, skipped = %+v; want a.py seeded and blob.dat (3 MiB) left out", len(files), skipped)
	}
}

func TestStreamTextAgreesWithUTF8Valid(t *testing.T) {
	for _, in := range []string{"", "plain", "é and � are text", "\xff", "ok\xe2\x82", strings.Repeat("é", 70000)} {
		n, text, err := streamText(strings.NewReader(in))
		if err != nil || n != int64(len(in)) || text != utf8.ValidString(in) {
			t.Errorf("streamText(%.20q) = %d, %v, %v; want %d, %v", in, n, text, err, len(in), utf8.ValidString(in))
		}
	}
}

func TestSeedSkippedJSONSortsAndDedupes(t *testing.T) {
	if got := seedSkippedJSON(nil); got != "" {
		t.Errorf("nothing left out = %q, want empty", got)
	}
	got := seedSkippedJSON([]seedSkip{{Path: "b.png", Why: skipNotText}, {Path: "a.png", Why: skipNotText}, {Path: "b.png", Why: skipNotText}, {Path: ".env.test", Why: "gitignored"}})
	if got != `[{"path":".env.test","why":"gitignored"},{"path":"a.png","why":"not-text"},{"path":"b.png","why":"not-text"}]` {
		t.Errorf("seedSkippedJSON = %s", got)
	}
}

// A repo scan's entry reads its seeds: what each language's copy left out,
// once each.
func TestSeedCacheReportsWhatItsSeedsLeftOut(t *testing.T) {
	c := newSeedCache(func(lang string) (repoSeed, error) {
		return repoSeed{skipped: []seedSkip{{Path: "logo.png", Why: skipNotText}, {Path: lang + ".bin", Why: skipNotText}}, cleanup: func() {}}, nil
	})
	defer c.close()
	for _, l := range []string{"go", "python"} {
		if _, err := c.get(l); err != nil {
			t.Fatal(err)
		}
	}
	if got := seedSkippedJSON(c.skipped()); got != `[{"path":"go.bin","why":"not-text"},{"path":"logo.png","why":"not-text"},{"path":"python.bin","why":"not-text"}]` {
		t.Errorf("skipped = %s", got)
	}
}

// certify --local hands its seed's left-out files back through seedSink,
// from inside buildJailWiring, where the seed is.
func TestBuildJailWiringHandsTheSeedsLeftOutFilesToItsSink(t *testing.T) {
	repoDir := t.TempDir()
	shared := repoSeed{
		seedDir: repoDir,
		files:   map[string]string{"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n", "go.mod": "module x\n"},
		skipped: []seedSkip{{Path: "logo.png", Size: 5}},
		cleanup: func() {},
	}
	var got []seedSkip
	_, err := buildJailWiring(context.Background(), jailWiringInput{
		timeout:  time.Minute,
		codePath: "pkg/a.go", testPath: "pkg/a_test.go", repoDir: repoDir, langName: "go",
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte("package pkg\n"),
		devTest:   []byte("package pkg\n"),
		checkArgv: []string{"go", "test", "./..."},
		stdout:    io.Discard,
		seed:      &shared,
		seedSink:  func(s []seedSkip) { got = s },
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	if len(got) != 1 || got[0].Path != "logo.png" {
		t.Errorf("sink got %+v, want the seed's logo.png", got)
	}
}

// The line groups by reason, so a reason the build step adds (gitignored)
// reads as its own clause rather than being called "not text".
func TestSeedSkipsLineSaysEachReason(t *testing.T) {
	got := seedSkipsLine("corral certify --repo", []seedSkip{
		{Path: "logo.png", Size: 9, Why: skipNotText},
		{Path: ".env.test", Size: 3, Why: "gitignored"},
	})
	for _, w := range []string{"leaves out 2 file(s)", "1 not UTF-8 text (logo.png)", "1 gitignored (.env.test)"} {
		if !strings.Contains(got, w) {
			t.Errorf("line %q lacks %q", got, w)
		}
	}
}

// A repo scan builds one seed per language over the same tree, and REA's run
// printed the same left-out line twice (2026-10-30). The scan's seed output
// says a line once; a different line still prints.
func TestOnceLinesSaysEachLineOnce(t *testing.T) {
	var out bytes.Buffer
	w := newOnceLines(&out)
	for _, s := range []string{"a\n", "b\n", "a\n"} {
		if _, err := io.WriteString(w, s); err != nil {
			t.Fatal(err)
		}
	}
	if out.String() != "a\nb\n" {
		t.Errorf("out = %q, want each line once", out.String())
	}
}
