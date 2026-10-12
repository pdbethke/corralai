// SPDX-License-Identifier: Elastic-2.0

package pairing_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

// rustRules is the REAL Rust plugin's rule list, read through the registry —
// never a restatement. These tests exercise the content Shapes with the rules
// that ship, so a change to rustPlugin.TestRules cannot leave them passing
// against a copy that no longer matches (final review triage 9). They live
// in the external test package because pairing cannot import lang.
var rustRules = func() []pairing.Rule {
	p, ok := lang.ByName("rust")
	if !ok {
		panic("no rust plugin registered")
	}
	return p.TestRules()
}()

// modules is a Sources.TestModules over a fixed answer per path.
func modules(by map[string][]pairing.TestModule) func(string) ([]pairing.TestModule, error) {
	return func(rel string) ([]pairing.TestModule, error) { return by[rel], nil }
}

// A content rule cannot read the source: that is the caller's error to
// surface, never "this file declares no test module".
func TestResolveSurfacesTestModulesError(t *testing.T) {
	_, err := pairing.Resolve(rustRules, "src/lib.rs", pairing.Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: func(string) ([]pairing.TestModule, error) { return nil, errors.New("permission denied") },
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the read error", err)
	}
}

// A source with no test module does not pair to itself, and since Rust
// derives no named candidate there is no basename to search for: nothing
// is listed as tried, and no search is claimed (Review Focus 1).
func TestResolveRustSourceWithNoModuleIsAMiss(t *testing.T) {
	listed := false
	res, err := pairing.Resolve(rustRules, "src/plain.rs", pairing.Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { listed = true; return []string{"tests/plain.rs", "src/plain.rs"}, nil },
		TestModules: modules(nil),
	})
	if err != nil || res.Found || res.Path != "" || res.Label != "" || res.Shape != "" {
		t.Fatalf("res=%+v err=%v, want a miss", res, err)
	}
	if listed || len(res.Roots) != 0 {
		t.Fatalf("a search with no basename to look for ran or was reported: listed=%v roots=%v", listed, res.Roots)
	}
}

func TestResolveInlineLabelsTheSourceItself(t *testing.T) {
	res, err := pairing.Resolve(rustRules, "src/lib.rs", pairing.Sources{
		Exists:      func(rel string) bool { return rel == "src/lib.rs" },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: modules(map[string][]pairing.TestModule{"src/lib.rs": {{Name: "tests", Inline: true, CfgTest: true}}}),
	})
	if err != nil || !res.Found || res.Path != "src/lib.rs" || res.Shape != pairing.Inline || res.Label != "inline" || res.Rank != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// Declared's paths, by rustc's rule. Each row is one module declaration in
// the source; the path is tried with Exists, and both the hit and the label
// are asserted.
func TestResolveDeclaredFollowsRustc(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mod        pairing.TestModule
		want       string // "" = a miss
		label      string
		tried      []string
	}{
		{"path attr, non-mod.rs file: beside the declaring file", "crates/warp_core/src/app_id.rs",
			pairing.TestModule{Name: "tests", PathAttr: "app_id_tests.rs", CfgTest: true},
			"crates/warp_core/src/app_id_tests.rs", "path-attr", nil},
		{"path attr with ../ is cleaned", "crates/warp_cli/src/local_control/mod.rs",
			pairing.TestModule{Name: "tests", PathAttr: "../local_control_tests.rs", CfgTest: true},
			"crates/warp_cli/src/local_control_tests.rs", "path-attr", nil},
		{"default module of a non-mod.rs file: <stem>/<name>.rs", "src/bar.rs",
			pairing.TestModule{Name: "tests", CfgTest: true}, "src/bar/tests.rs", "default-module", nil},
		{"default module of mod.rs: beside it", "src/foo/mod.rs",
			pairing.TestModule{Name: "tests", CfgTest: true}, "src/foo/tests.rs", "default-module", nil},
		{"default module of lib.rs: beside it", "src/lib.rs",
			pairing.TestModule{Name: "tests"}, "src/tests.rs", "default-module", nil},
		{"default module of main.rs: beside it", "main.rs",
			pairing.TestModule{Name: "test"}, "test.rs", "default-module", nil},
		{"default module may be <name>/mod.rs", "src/baz.rs",
			pairing.TestModule{Name: "tests"}, "src/baz/tests/mod.rs", "default-module", nil},
		{"missing #[path] target is a miss, listed in Tried", "src/missing.rs",
			pairing.TestModule{Name: "tests", PathAttr: "missing_tests.rs"}, "", "", []string{"src/missing_tests.rs"}},
		{"#[path] escaping the root is never a pairing", "src/escape.rs",
			pairing.TestModule{Name: "tests", PathAttr: "../../outside_tests.rs"}, "", "", nil},
		{"an inline module is not pairing.Declared's", "src/inl.rs",
			pairing.TestModule{Name: "tests", Inline: true, PathAttr: "ignored.rs"}, "src/inl.rs", "inline", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exists := map[string]bool{tc.code: true}
			if tc.want != "" {
				exists[tc.want] = true
			}
			res, err := pairing.Resolve(rustRules, tc.code, pairing.Sources{
				Exists:      func(rel string) bool { return exists[rel] },
				Universe:    func() ([]string, error) { return nil, nil },
				TestModules: modules(map[string][]pairing.TestModule{tc.code: {tc.mod}}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Path != tc.want || res.Found != (tc.want != "") || res.Label != tc.label {
				t.Fatalf("got path=%q found=%v label=%q, want path=%q label=%q (res=%+v)", res.Path, res.Found, res.Label, tc.want, tc.label, res)
			}
			if tc.tried != nil && strings.Join(res.Tried, ",") != strings.Join(tc.tried, ",") {
				t.Fatalf("Tried = %v, want %v", res.Tried, tc.tried)
			}
			for _, p := range res.Tried {
				if strings.HasPrefix(p, "../") {
					t.Fatalf("Tried lists a path outside the root: %v", res.Tried)
				}
			}
		})
	}
}

// Primary's answer for Rust is the source itself: a new test is written into
// the file's own test module, never into a guessed file name.
func TestPrimaryForRustIsTheSource(t *testing.T) {
	c, ok := pairing.Primary(rustRules, "src/net/conn.rs")
	if !ok || c.Path != "src/net/conn.rs" || c.Shape != pairing.Inline {
		t.Fatalf("Primary = %+v, %v", c, ok)
	}
}

// TestDir: a directory alone makes a file a test, at any depth (a workspace
// member's tests/), while a src/ file is never one by name (Review Focus 2).
func TestTestDirMarksTestsAndOnlyThem(t *testing.T) {
	for rel, want := range map[string]bool{
		"tests/integration.rs":                    true,
		"crates/integration/tests/integration.rs": true,
		"crates/warp_core/src/app_id_tests.rs":    true,
		"src/foo/tests.rs":                        true,
		"src/lib.rs":                              false,
		"src/main.rs":                             false,
		"src/testing_utils.rs":                    false,
		"crates/warp_core/src/app_id.rs":          false,
		"src/foo/mod.rs":                          false,
	} {
		if got := pairing.IsTest(rustRules, rel); got != want {
			t.Errorf("IsTest(%q) = %v, want %v", rel, got, want)
		}
		if want && !pairing.MightBeTest([][]pairing.Rule{rustRules}, rel) {
			t.Errorf("MightBeTest(%q) = false for a test", rel)
		}
	}
	if got := pairing.Roots(rustRules); strings.Join(got, ",") != "tests" {
		t.Errorf("Roots = %v, want [tests]", got)
	}
}

// A source's own #[path] declaration is repository-controlled: an audited
// repository is untrusted, and the file a declaration names is read, seeded
// into the jail, shown to the models and (on certify --adversarial) uploaded
// as the developer's test. So a Declared path must stay inside the
// repository root. One that leaves it is not a candidate: Resolve does not
// check it and reports it in RefusedOutsideRoot, so a miss can say WHY the
// declared test was not used (final review I4, m6).

func declaredOnly(code string, mod pairing.TestModule) func(string) ([]pairing.TestModule, error) {
	return modules(map[string][]pairing.TestModule{code: {mod}})
}

// The lexical case, root-relative: dropped silently before; now refused and
// reported.
func TestResolveRefusesARelativeDeclaredPathOutsideTheRoot(t *testing.T) {
	res, err := pairing.Resolve(rustRules, "src/escape.rs", pairing.Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: declaredOnly("src/escape.rs", pairing.TestModule{Name: "tests", PathAttr: "../../outside_tests.rs"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || len(res.Tried) != 0 {
		t.Fatalf("res = %+v, want a miss that checked nothing", res)
	}
	if !slices.Equal(res.RefusedOutsideRoot, []string{"../outside_tests.rs"}) {
		t.Fatalf("RefusedOutsideRoot = %v, want [../outside_tests.rs]", res.RefusedOutsideRoot)
	}
}

// An absolute source (certify --local/--adversarial/doctor given an absolute
// --code) with no root: nothing to confine to, so the declared path is
// refused even though it "exists".
func TestResolveRefusesAnAbsoluteDeclaredPathWithNoRoot(t *testing.T) {
	code := "/r/repo/src/lib.rs"
	res, err := pairing.Resolve(rustRules, code, pairing.Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: declaredOnly(code, pairing.TestModule{Name: "tests", PathAttr: "../../outside/secret.txt", CfgTest: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found {
		t.Fatalf("an absolute #[path] with no root paired to %q", res.Path)
	}
	if !slices.Equal(res.RefusedOutsideRoot, []string{"/r/outside/secret.txt"}) {
		t.Fatalf("RefusedOutsideRoot = %v", res.RefusedOutsideRoot)
	}
}

// With the root the caller derives, an absolute source's declared path is
// held to it: inside pairs, outside is refused — the
// ../../outside/secret.txt case reproduced in the final review.
func TestResolveConfinesAnAbsoluteDeclaredPathToTheRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	code := filepath.ToSlash(filepath.Join(repo, "src", "lib.rs"))
	for _, tc := range []struct {
		attr, want string
		refused    bool
	}{
		{"lib_tests.rs", filepath.ToSlash(filepath.Join(repo, "src", "lib_tests.rs")), false},
		{"../../outside/secret.txt", "", true},
	} {
		res, err := pairing.Resolve(rustRules, code, pairing.Sources{
			Root:        repo,
			Exists:      func(string) bool { return true },
			Universe:    func() ([]string, error) { return nil, nil },
			TestModules: declaredOnly(code, pairing.TestModule{Name: "tests", PathAttr: tc.attr, CfgTest: true}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Path != tc.want || (len(res.RefusedOutsideRoot) > 0) != tc.refused {
			t.Errorf("#[path = %q]: res = %+v, want path %q refused=%v", tc.attr, res, tc.want, tc.refused)
		}
	}
}

// A symlink inside the repository that points out of it is outside the root
// too: the check is on the resolved path, not the spelling.
func TestResolveRefusesADeclaredPathThroughASymlinkOutOfTheRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(repo, "src"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "t.rs"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "src", "link")); err != nil {
		t.Fatal(err)
	}
	res, err := pairing.Resolve(rustRules, "src/lib.rs", pairing.Sources{
		Root: repo,
		Exists: func(rel string) bool {
			fi, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
			return err == nil && fi.Mode().IsRegular()
		},
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: declaredOnly("src/lib.rs", pairing.TestModule{Name: "tests", PathAttr: "link/t.rs"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || !slices.Equal(res.RefusedOutsideRoot, []string{"src/link/t.rs"}) {
		t.Fatalf("res = %+v, want src/link/t.rs refused as outside the root", res)
	}
}
