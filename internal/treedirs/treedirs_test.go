// SPDX-License-Identifier: Elastic-2.0

package treedirs

import (
	"maps"
	"sort"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestCompositions(t *testing.T) {
	set := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	deps := []string{"node_modules", "vendor", ".venv", "venv", "site-packages", ".tox", ".bundle"}
	cases := []struct {
		name string
		got  map[string]bool
		want map[string]bool
	}{
		{"Dependencies", Dependencies(), set(deps...)},
		// .tox is excluded: the run writes into it (adequacy/pool.go). site-packages is NEW
		// to the bind/symlink sets — a read-only dependency tree like .venv.
		{"SharedDependencies", SharedDependencies(), set("node_modules", "vendor", ".venv", "venv", "site-packages", ".bundle")},
		// Identical to reposcan's skipDirs before B3.
		{"NeverWalked", NeverWalked(), set(append(append([]string{}, deps...), ".git", ".corral", "dist", "build", "target", "testdata")...)},
		// Identical to reposcan's invisibleDirs before B3.
		{"Unaccounted", Unaccounted(), set(append(append([]string{}, deps...), ".git", ".corral")...)},
		// internal/repo skipped only .git, node_modules, vendor; it now also skips .corral and
		// the python/ruby dependency trees.
		{"NotRead", NotRead(), set(append(append([]string{}, deps...), ".git", ".corral")...)},
		// review skipped .git node_modules vendor .corral testdata dist build .venv __pycache__;
		// it now also skips venv, site-packages, .tox, .bundle and target. review_plan's
		// extra "site" (corral's own website dir) is dropped.
		{"NotReviewed", NotReviewed(), set(append(append([]string{}, deps...), ".git", ".corral", "dist", "build", "target", "testdata", "__pycache__")...)},
	}
	for _, c := range cases {
		if !maps.Equal(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, keys(c.got), keys(c.want))
		}
	}
}

// Every registered plugin's dependency dirs reach Dependencies(), so a new
// language's dirs flow into every composition without editing this package.
func TestDependenciesCoverEveryPlugin(t *testing.T) {
	got := Dependencies()
	for _, name := range lang.Names() {
		p, _ := lang.ByName(name)
		for _, d := range p.DependencyDirs() {
			if !got[d.Name] {
				t.Errorf("%s declares %q but Dependencies() lacks it", name, d.Name)
			}
		}
	}
}

// Compositions return fresh maps: a caller mutating one must not corrupt the next.
func TestCompositionsAreFresh(t *testing.T) {
	a := NotReviewed()
	a["x"] = true
	if NotReviewed()["x"] {
		t.Error("NotReviewed shares its map between calls")
	}
}

// No directory a run writes into is shared: derived from the plugins'
// declarations, so a new RunWrites dir is checked without editing this test.
// A shared RunWrites dir would be N pool trees writing through one link into
// the operator's checkout, or a read-only bind the suite fails to write.
func TestSharedDependenciesExcludeRunWrites(t *testing.T) {
	shared := SharedDependencies()
	for _, d := range lang.AllDependencyDirs() {
		if d.RunWrites && shared[d.Name] {
			t.Errorf("%q is declared RunWrites but SharedDependencies() includes it", d.Name)
		}
		if !d.RunWrites && !shared[d.Name] {
			t.Errorf("%q is a read-only dependency dir but SharedDependencies() lacks it", d.Name)
		}
	}
}
