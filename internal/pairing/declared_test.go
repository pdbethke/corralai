// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package pairing_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// These run the REAL Rust plugin's rules over fixture trees on disk
// (pairingtest/rust; its README says where each file came from), with the
// module declarations read by the real parser, repoindex.TestModules — the
// same three sources reposcan's adapters wire. Shape semantics are covered
// path-by-path in shape_test.go; this is the end-to-end statement that a
// file's own declaration, as rustc reads it, decides its test.
//
// cgo-only because the parse needs the tree-sitter grammar: in a no-cgo
// build TestModules answers nil for everything, which these trees would read
// as "no file declares a test".

func rustPluginRules(t *testing.T) []pairing.Rule {
	t.Helper()
	p, ok := lang.ByName("rust")
	if !ok {
		t.Fatal("no rust plugin registered")
	}
	return p.TestRules()
}

// diskSources is a Sources over the fixture tree at root.
func diskSources(t *testing.T, root string) pairing.Sources {
	t.Helper()
	return pairing.Sources{
		Exists: func(rel string) bool {
			fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
			return err == nil && fi.Mode().IsRegular()
		},
		Universe: func() ([]string, error) {
			var out []string
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
				return err
			})
			return out, err
		},
		TestModules: func(rel string) ([]pairing.TestModule, error) {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G304 -- fixture tree under this package
			if err != nil {
				return nil, err
			}
			return repoindex.TestModules("rust", src), nil
		},
	}
}

func TestRustFixturesPairByTheirOwnDeclaration(t *testing.T) {
	rules := rustPluginRules(t)
	for _, tc := range []struct {
		tree, code  string
		want, label string // want "" = a miss, with label ""
		shape       pairing.Shape
	}{
		// Warp: the #[path] lines are verbatim from the fork.
		{"warp", "crates/warp_core/src/app_id.rs", "crates/warp_core/src/app_id_tests.rs", "path-attr", pairing.Declared},
		{"warp", "crates/warp_cli/src/local_control/mod.rs", "crates/warp_cli/src/local_control_tests.rs", "path-attr", pairing.Declared},
		// rustc's default module file, for a mod.rs and a non-mod.rs file.
		{"layouts", "src/foo/mod.rs", "src/foo/tests.rs", "default-module", pairing.Declared},
		{"layouts", "src/bar.rs", "src/bar/tests.rs", "default-module", pairing.Declared},
		// #[path] is relative to the declaring file's directory: a name no
		// filename convention would guess.
		{"layouts", "src/a/b.rs", "src/elsewhere/x.rs", "path-attr", pairing.Declared},
		{"layouts", "src/lib.rs", "src/lib.rs", "inline", pairing.Inline},
		// Review Focus 1: no test module, so not paired — least of all to itself.
		{"layouts", "src/plain.rs", "", "", ""},
		{"layouts", "src/missing.rs", "", "", ""},
		{"layouts", "src/escape.rs", "", "", ""},
	} {
		t.Run(tc.tree+"/"+tc.code, func(t *testing.T) {
			root := filepath.Join("pairingtest", "rust", tc.tree)
			res, err := pairing.Resolve(rules, tc.code, diskSources(t, root))
			if err != nil {
				t.Fatal(err)
			}
			if res.Path != tc.want || res.Found != (tc.want != "") || res.Label != tc.label || res.Shape != tc.shape {
				t.Fatalf("got path=%q label=%q shape=%q found=%v, want path=%q label=%q shape=%q (res=%+v)",
					res.Path, res.Label, res.Shape, res.Found, tc.want, tc.label, tc.shape, res)
			}
			if !res.Found && res.Path == tc.code {
				t.Fatalf("a source with no test module paired to itself: %+v", res)
			}
		})
	}
}

// The missing #[path] target is named in Tried, so a "no test found" message
// says what the file asked for; the escaping one is not tried at all.
func TestRustFixtureMissesSayWhatWasTried(t *testing.T) {
	rules := rustPluginRules(t)
	root := filepath.Join("pairingtest", "rust", "layouts")
	res, err := pairing.Resolve(rules, "src/missing.rs", diskSources(t, root))
	if err != nil || len(res.Tried) != 1 || res.Tried[0] != "src/missing_tests.rs" {
		t.Fatalf("missing: Tried=%v err=%v", res.Tried, err)
	}
	res, err = pairing.Resolve(rules, "src/escape.rs", diskSources(t, root))
	if err != nil || len(res.Tried) != 0 {
		t.Fatalf("escape: Tried=%v err=%v, want nothing tried outside the root", res.Tried, err)
	}
}

// Review Focus 2, over the fixture trees: every test file is IsTest, and no
// source is — a source wrongly called a test is silently never audited.
func TestRustFixturesIsTest(t *testing.T) {
	rules := rustPluginRules(t)
	for rel, want := range map[string]bool{
		"warp/crates/warp_core/src/app_id.rs":             false,
		"warp/crates/warp_core/src/app_id_tests.rs":       true,
		"warp/crates/warp_cli/src/local_control/mod.rs":   false,
		"warp/crates/warp_cli/src/local_control_tests.rs": true,
		"warp/crates/integration/tests/integration.rs":    true,
		"layouts/tests/integration.rs":                    true,
		"layouts/src/foo/mod.rs":                          false,
		"layouts/src/foo/tests.rs":                        true,
		"layouts/src/bar.rs":                              false,
		"layouts/src/bar/tests.rs":                        true,
		"layouts/src/lib.rs":                              false,
		"layouts/src/plain.rs":                            false,
		"layouts/src/a/b.rs":                              false,
	} {
		// Trim the tree name: IsTest sees repository-relative paths.
		_, inTree, _ := strings.Cut(rel, "/")
		if _, err := os.Stat(filepath.Join("pairingtest", "rust", filepath.FromSlash(rel))); err != nil {
			t.Fatalf("fixture %s missing: %v", rel, err)
		}
		if got := pairing.IsTest(rules, inTree); got != want {
			t.Errorf("IsTest(%q) = %v, want %v", inTree, got, want)
		}
	}
}
