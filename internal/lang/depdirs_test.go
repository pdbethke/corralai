// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

// TestMergeDependencyDirsRunWritesIsSticky: a name declared by two languages
// is RunWrites if EITHER declaration says so, in either order — a directory
// some language's run writes into must never be shared read-only. Fake
// declarations, so the rule is pinned independently of which real plugins
// happen to collide today.
func TestMergeDependencyDirsRunWritesIsSticky(t *testing.T) {
	writes := []DepDir{{Name: "shared", RunWrites: true}}
	reads := []DepDir{{Name: "shared"}, {Name: "only-reads"}}
	want := []DepDir{{Name: "only-reads"}, {Name: "shared", RunWrites: true}}
	for _, decls := range [][][]DepDir{{writes, reads}, {reads, writes}} {
		if got := mergeDependencyDirs(decls); !slices.Equal(got, want) {
			t.Errorf("mergeDependencyDirs(%v) = %v, want %v", decls, got, want)
		}
	}
}

// TestAllDependencyDirsIsTheMergeOfEveryPlugin: AllDependencyDirs is exactly
// mergeDependencyDirs over every registered plugin's declarations.
func TestAllDependencyDirsIsTheMergeOfEveryPlugin(t *testing.T) {
	var decls [][]DepDir
	for _, name := range Names() {
		p, _ := ByName(name)
		decls = append(decls, p.DependencyDirs())
	}
	if got, want := AllDependencyDirs(), mergeDependencyDirs(decls); !slices.Equal(got, want) {
		t.Errorf("AllDependencyDirs() = %v, want %v", got, want)
	}
}
