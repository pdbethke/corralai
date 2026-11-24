//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

func TestInlineTestLinesReadsTheInlineModuleOnly(t *testing.T) {
	src := []byte("pub fn f() {}\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n")
	got := inlineTestLines("rust", src)
	if want := []lang.LineRange{{Start: 2, End: 6}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inlineTestLines = %v, want %v", got, want)
	}
	if got := inlineTestLines("rust", []byte("#[cfg(test)]\nmod tests;\n")); len(got) != 0 {
		t.Fatalf("a bare mod has no lines in this file, got %v", got)
	}
	if got := inlineTestLines("go", src); len(got) != 0 {
		t.Fatalf("a language with no reader passes nothing, got %v", got)
	}
}

// R2 GUARD: the rule "a mutant may not edit the source's own tests" must reach
// EVERY door that builds a grading scorer, not just the one somebody tested.
// buildJailWiring has three JailScorer constructions (workspace substrate,
// repo-aware jail, single-file jail); this builds each for a Rust source with
// an inline test module and asserts the scorer carries exactly that module's
// lines, so a literal that drops EvidenceLines fails here.
func TestBuildJailWiringHandsEveryScorerTheInlineTestLines(t *testing.T) {
	src := "pub fn f() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n    #[test]\n    fn t() { assert_eq!(f(), 1); }\n}\n"
	want := []lang.LineRange{{Start: 3, End: 8}}
	check := func(t *testing.T, w jailWiring) {
		t.Helper()
		if !reflect.DeepEqual(w.scorer.EvidenceLines, want) {
			t.Fatalf("scorer.EvidenceLines = %v, want %v", w.scorer.EvidenceLines, want)
		}
	}

	t.Run("workspace substrate", func(t *testing.T) {
		root := t.TempDir()
		for _, f := range []string{"src/lib.rs"} {
			if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, f), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		w, err := buildJailWiring(context.Background(), jailWiringInput{
			substrate: substrateWorkspace, repoDir: root, timeout: time.Minute,
			codePath: "src/lib.rs", testPath: "src/lib.rs", langName: "rust",
			checkArgv: []string{"cargo", "test"},
			code:      []byte(src), devTest: []byte(src),
			fsPath: func(q string) string { return filepath.Join(root, q) },
			stdout: io.Discard,
		})
		if err != nil {
			t.Fatalf("buildJailWiring: %v", err)
		}
		defer w.cleanup()
		check(t, w)
	})

	t.Run("repo-aware jail", func(t *testing.T) {
		root := t.TempDir()
		seed := repoSeed{seedDir: root, files: map[string]string{"src/lib.rs": src}, cleanup: func() {}}
		w, err := buildJailWiring(context.Background(), jailWiringInput{
			iso: nil, timeout: time.Minute, repoDir: root,
			codePath: "src/lib.rs", testPath: "src/lib.rs", langName: "rust",
			checkArgv: []string{"cargo", "test"},
			code:      []byte(src), devTest: []byte(src),
			fsPath: func(q string) string { return filepath.Join(root, q) },
			stdout: io.Discard, seed: &seed,
		})
		if err != nil {
			t.Fatalf("buildJailWiring: %v", err)
		}
		defer w.cleanup()
		check(t, w)
	})

	t.Run("single-file jail", func(t *testing.T) {
		w, err := buildJailWiring(context.Background(), jailWiringInput{
			iso: nil, timeout: time.Minute,
			codePath: "lib.rs", testPath: "lib.rs", langName: "rust",
			checkArgv: []string{"cargo", "test"},
			code:      []byte(src), devTest: []byte(src),
			fsPath: func(q string) string { return q },
			stdout: io.Discard,
		})
		if err != nil {
			t.Fatalf("buildJailWiring: %v", err)
		}
		defer w.cleanup()
		check(t, w)
	})
}
