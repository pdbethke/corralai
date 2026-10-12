// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

// TestAllBuildDirsIsEveryBuildDirerInTheRegistry: AllBuildDirs is exactly the
// concatenation, in Names() order, of every registered plugin that implements
// BuildDirer — derived, so a language that declares one reaches the seed
// walkers without anyone editing a list.
func TestAllBuildDirsIsEveryBuildDirerInTheRegistry(t *testing.T) {
	var want []BuildDir
	for _, name := range Names() {
		if b, ok := registry[name].(BuildDirer); ok {
			want = append(want, b.BuildDirs()...)
		}
	}
	if got := AllBuildDirs(); !slices.Equal(got, want) {
		t.Errorf("AllBuildDirs() = %v, want %v", got, want)
	}
}

// TestRustBuildDirIsTargetBesideCargoToml: cargo's own definition of its
// default build directory — `target` in the directory holding the manifest.
// The marker is what keeps a directory merely NAMED target (a Python package,
// a Go package) in the seed.
func TestRustBuildDirIsTargetBesideCargoToml(t *testing.T) {
	p, ok := ByName("rust")
	if !ok {
		t.Fatal("rust is not registered")
	}
	b, ok := p.(BuildDirer)
	if !ok {
		t.Fatal("rust does not implement BuildDirer")
	}
	if got, want := b.BuildDirs(), []BuildDir{{Name: "target", Marker: "Cargo.toml"}}; !slices.Equal(got, want) {
		t.Errorf("rust BuildDirs() = %v, want %v", got, want)
	}
}

// TestEveryBuildDirNamesAMarker: a BuildDir with no marker would be a
// basename-only skip, which drops real source that happens to share the name
// (a Python package called build). Refused for every registered declaration.
func TestEveryBuildDirNamesAMarker(t *testing.T) {
	for _, d := range AllBuildDirs() {
		if d.Name == "" || d.Marker == "" {
			t.Errorf("BuildDir %+v: both Name and Marker are required", d)
		}
	}
}
