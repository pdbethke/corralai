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
	"github.com/pdbethke/corralai/internal/reposcan"
)

func TestInlineTestLinesReadsTheInlineModuleOnly(t *testing.T) {
	src := []byte("pub fn f() {}\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n")
	read := func(langName string, code []byte) []lang.LineRange {
		t.Helper()
		got, err := inlineTestLines("x.rs", langName, code)
		if err != nil {
			t.Fatalf("inlineTestLines: %v", err)
		}
		return got
	}
	if got, want := read("rust", src), []lang.LineRange{{Start: 2, End: 6}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inlineTestLines = %v, want %v", got, want)
	}
	if got := read("rust", []byte("#[cfg(test)]\nmod tests;\n")); len(got) != 0 {
		t.Fatalf("a bare mod has no lines in this file, got %v", got)
	}
	if got := read("go", src); len(got) != 0 {
		t.Fatalf("a language with no reader passes nothing, got %v", got)
	}
	// Test code the old top-level reader could not see: a test module nested
	// in an inline module, one gated on cfg(all(test, …)), and a #[test] fn
	// outside any module.
	nested := []byte("mod imp {\n    #[cfg(test)]\n    mod tests {\n        #[test]\n        fn t() {}\n    }\n}\n#[cfg(all(test, feature = \"std\"))]\nmod checks {\n}\n#[test]\nfn loose() {}\n")
	if got, want := read("rust", nested), []lang.LineRange{{Start: 2, End: 6}, {Start: 8, End: 10}, {Start: 11, End: 12}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inlineTestLines = %v, want %v", got, want)
	}
	// A parse that set code aside where tests could hide is refused, never
	// handed to a scorer.
	_, err := inlineTestLines("x.rs", "rust", []byte("pub fn f() {}\n@@@\n#[cfg(test)]\nmod tests {\n}\n"))
	if rc, ok := err.(reposcan.ReasonCarrier); !ok || rc.ScanReason() != reposcan.ReasonOwnTestsUnreadable {
		t.Fatalf("a parse that set code aside: err = %v, want the own-tests refusal", err)
	}
}

// R2 GUARD: the rule "a mutant may not edit the source's own tests" must reach
// EVERY door that builds a grading scorer, not just the one somebody tested.
// buildJailWiring has three JailScorer constructions (workspace substrate,
// repo-aware jail, single-file jail); this builds each for a Rust source with
// an inline test module and asserts the scorer carries exactly that module's
// lines, so a literal that drops EvidenceLines fails here.
func TestBuildJailWiringHandsEveryScorerTheInlineTestLines(t *testing.T) {
	// The second source's line 2 is set aside by the grammar: every door
	// must refuse it, or one door grades on a reading that may have missed
	// the tests.
	for _, tc := range []struct {
		src    string
		want   []lang.LineRange
		unread bool
	}{
		{"pub fn f() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n    #[test]\n    fn t() { assert_eq!(f(), 1); }\n}\n", []lang.LineRange{{Start: 3, End: 8}}, false},
		{"pub fn f() -> i32 { 1 }\n@@@\n#[cfg(test)]\nmod tests {\n    use super::*;\n    #[test]\n    fn t() { assert_eq!(f(), 1); }\n}\n", []lang.LineRange{{Start: 3, End: 8}}, true},
	} {
		src, want := tc.src, tc.want
		check := func(t *testing.T, w jailWiring) {
			t.Helper()
			if !reflect.DeepEqual(w.scorer.EvidenceLines, want) {
				t.Fatalf("scorer.EvidenceLines = %v, want %v", w.scorer.EvidenceLines, want)
			}
		}
		done := func(t *testing.T, w jailWiring, err error) {
			t.Helper()
			if tc.unread {
				// Refused at the door that builds every scorer, with its
				// own reason, rather than handed lines that may miss tests.
				if rc, ok := err.(reposcan.ReasonCarrier); !ok || rc.ScanReason() != reposcan.ReasonOwnTestsUnreadable {
					t.Fatalf("buildJailWiring over a set-aside source: err = %v, want the own-tests refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildJailWiring: %v", err)
			}
			defer w.cleanup()
			check(t, w)
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
			done(t, w, err)
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
			done(t, w, err)
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
			done(t, w, err)
		})
	}
}
