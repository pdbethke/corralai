// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package reposcan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestFindTestConfinesAnAbsoluteCodesDeclaredTestToTheRoot is the door the
// final review executed (I4): certify --adversarial, certify --local and
// doctor resolve an operator's ABSOLUTE --code against the working directory
// ("."), and a repository-controlled `#[path = "../../outside/secret.txt"]`
// paired to that host file. The root those doors already use is the root the
// declared path is held to.
func TestFindTestConfinesAnAbsoluteCodesDeclaredTestToTheRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "fr-escape", "repo")
	for name, body := range map[string]string{
		filepath.Join(repo, "src", "lib.rs"):                      "pub fn f() {}\n\n#[cfg(test)]\n#[path = \"../../outside/secret.txt\"]\nmod tests;\n",
		filepath.Join(repo, "src", "ok.rs"):                       "pub fn g() {}\n\n#[cfg(test)]\n#[path = \"ok_tests.rs\"]\nmod tests;\n",
		filepath.Join(repo, "src", "ok_tests.rs"):                 "#[test]\nfn t() {}\n",
		filepath.Join(base, "fr-escape", "outside", "secret.txt"): "host secret\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := lang.ByName("rust")
	t.Chdir(repo)

	res, err := FindTest(p, ".", filepath.Join(repo, "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || len(res.RefusedOutsideRoot) != 1 {
		t.Fatalf("absolute --code with an escaping #[path]: res = %+v, want a miss with the path refused", res)
	}
	res, err = FindTest(p, ".", filepath.Join(repo, "src", "ok.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.Path != filepath.ToSlash(filepath.Join(repo, "src", "ok_tests.rs")) {
		t.Fatalf("absolute --code with an in-repo #[path]: res = %+v, want ok_tests.rs", res)
	}
}
