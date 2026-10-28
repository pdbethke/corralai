// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// ReadTreeFile reads one file of a run's tree as the jail sees it: the
// seed's own files first, then the dependency directory mounted at that
// path — where node_modules is on a real run, so the unit evidence pass can
// read an installed tool's version without running it. A read never
// leaves the mounted directory: a symlink inside it that stays inside it
// (pnpm's node_modules/vitest -> .pnpm/...) is followed, one that leaves
// it is refused, and so is a path that climbs out of the tree.
func TestReadTreeFile(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "package.json"), []byte(`{"version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nm := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(nm, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("vitest/package.json", `{"version":"5.0.3"}`)
	write(".pnpm/vite@8/node_modules/vite/package.json", `{"version":"8.3.5"}`)
	if err := os.Symlink(".pnpm/vite@8/node_modules/vite", filepath.Join(nm, "vite")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(nm, "evil")); err != nil {
		t.Fatal(err)
	}
	nested := t.TempDir()
	if err := os.MkdirAll(filepath.Join(nested, "vitest"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "vitest", "package.json"), []byte(`{"version":"5.0.4"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	j := bwrapJail{binds: []DepBind{{Host: nm, Rel: "node_modules"}, {Host: nested, Rel: "packages/a/node_modules"}}}
	files := map[string]string{"package.json": `{"name":"app"}`, "node_modules/seeded.txt": "from the seed"}

	for _, c := range []struct{ rel, want string }{
		{"package.json", `{"name":"app"}`},
		{"node_modules/seeded.txt", "from the seed"},
		{"node_modules/vitest/package.json", `{"version":"5.0.3"}`},
		{"node_modules/vite/package.json", `{"version":"8.3.5"}`},
		{"packages/a/node_modules/vitest/package.json", `{"version":"5.0.4"}`},
	} {
		got, err := j.ReadTreeFile(files, c.rel)
		if err != nil || string(got) != c.want {
			t.Errorf("ReadTreeFile(%q) = %q, %v; want %q", c.rel, got, err, c.want)
		}
	}
	for _, rel := range []string{"node_modules/evil/package.json", "../package.json", "node_modules/../../x", "/etc/passwd"} {
		if got, err := j.ReadTreeFile(files, rel); err == nil {
			t.Errorf("ReadTreeFile(%q) = %q, want a refusal", rel, got)
		}
	}
	for _, rel := range []string{"node_modules/absent/package.json", "src/absent.ts"} {
		if _, err := j.ReadTreeFile(files, rel); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadTreeFile(%q) err = %v, want fs.ErrNotExist", rel, err)
		}
	}
}
