// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A source's own #[path] declaration is repository-controlled: an audited
// repository is untrusted, and the file a declaration names is read, seeded
// into the jail, shown to the models and (on certify --adversarial) uploaded
// as the developer's test. So a Declared path must stay inside the
// repository root. One that leaves it is not a candidate: Resolve does not
// check it and reports it in RefusedOutsideRoot, so a miss can say WHY the
// declared test was not used (final review I4, m6).

func declaredOnly(code string, mod TestModule) func(string) ([]TestModule, error) {
	return modules(map[string][]TestModule{code: {mod}})
}

// The lexical case, root-relative: dropped silently before; now refused and
// reported.
func TestResolveRefusesARelativeDeclaredPathOutsideTheRoot(t *testing.T) {
	res, err := Resolve(rustRules, "src/escape.rs", Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: declaredOnly("src/escape.rs", TestModule{Name: "tests", PathAttr: "../../outside_tests.rs"}),
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
	res, err := Resolve(rustRules, code, Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: declaredOnly(code, TestModule{Name: "tests", PathAttr: "../../outside/secret.txt", CfgTest: true}),
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
		res, err := Resolve(rustRules, code, Sources{
			Root:        repo,
			Exists:      func(string) bool { return true },
			Universe:    func() ([]string, error) { return nil, nil },
			TestModules: declaredOnly(code, TestModule{Name: "tests", PathAttr: tc.attr, CfgTest: true}),
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
	res, err := Resolve(rustRules, "src/lib.rs", Sources{
		Root: repo,
		Exists: func(rel string) bool {
			fi, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
			return err == nil && fi.Mode().IsRegular()
		},
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: declaredOnly("src/lib.rs", TestModule{Name: "tests", PathAttr: "link/t.rs"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || !slices.Equal(res.RefusedOutsideRoot, []string{"src/link/t.rs"}) {
		t.Fatalf("res = %+v, want src/link/t.rs refused as outside the root", res)
	}
}
