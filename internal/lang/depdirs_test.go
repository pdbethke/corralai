// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
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
		if got := mergeDependencyDirs(decls); !reflect.DeepEqual(got, want) {
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
	if got, want := AllDependencyDirs(), mergeDependencyDirs(decls); !reflect.DeepEqual(got, want) {
		t.Errorf("AllDependencyDirs() = %v, want %v", got, want)
	}
}

// TestMergeDependencyDirsUnionsCopyPerTree: vendor is Go's and PHP's, and
// only PHP's needs autoload.php and composer/ copied into each tree. The
// merged declaration carries every language's entries, sorted and once, in
// either order — copying an entry Go's vendor does not have is a no-op, and
// linking one Composer locates the project from would make every tree load
// the original checkout.
func TestMergeDependencyDirsUnionsCopyPerTree(t *testing.T) {
	php := []DepDir{{Name: "vendor", CopyPerTree: []string{"composer", "autoload.php"}}}
	gov := []DepDir{{Name: "vendor"}}
	other := []DepDir{{Name: "vendor", CopyPerTree: []string{"autoload.php", "x"}}}
	want := []DepDir{{Name: "vendor", CopyPerTree: []string{"autoload.php", "composer", "x"}}}
	for _, decls := range [][][]DepDir{{php, gov, other}, {other, gov, php}} {
		if got := mergeDependencyDirs(decls); !reflect.DeepEqual(got, want) {
			t.Errorf("mergeDependencyDirs(%v) = %v, want %v", decls, got, want)
		}
	}
}

// PHP declares the two Composer entries that locate the project by their own
// path; the rest of vendor/ is third-party code and stays linked.
func TestPHPVendorCopiesComposersLocatorPerTree(t *testing.T) {
	p, _ := ByName("php")
	want := []DepDir{{Name: "vendor", CopyPerTree: []string{"autoload.php", "composer"}}}
	if got := p.DependencyDirs(); !reflect.DeepEqual(got, want) {
		t.Errorf("php DependencyDirs() = %v, want %v", got, want)
	}
}
