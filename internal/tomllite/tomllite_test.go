// SPDX-License-Identifier: Elastic-2.0

package tomllite

import (
	"slices"
	"testing"
)

// TestTablesDeclared pins the forms a duplicate-table refusal recognises.
func TestTablesDeclared(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"[source.crates-io]\n", "source.crates-io"},
		{"[ source . \"crates-io\" ] # mirror\n", "source.crates-io"},
		{"source.crates-io.replace-with = \"m\"\n", "source.crates-io"},
		{"[source]\ncrates-io.replace-with = \"m\"\n", "source.crates-io"},
	} {
		if got := TablesDeclared(tc.text); !slices.Contains(got, tc.want) {
			t.Errorf("TablesDeclared(%q) = %v, want it to contain %q", tc.text, got, tc.want)
		}
	}
	if got := TablesDeclared("[alias]\nb = \"build\"\n[source.mirror]\nregistry = \"x\"\n"); slices.Contains(got, "source.crates-io") {
		t.Errorf("unrelated tables reported crates-io: %v", got)
	}
}

func TestStringValue(t *testing.T) {
	cases := []struct {
		name, text, table, key, want string
		ok                           bool
	}{
		{"package name", "[package]\nname = \"warp_core\"\nversion = \"0.1.0\"\n", "package", "name", "warp_core", true},
		{"single quotes and a comment", "[package]\nname = 'x-y' # the crate\n", "package", "name", "x-y", true},
		{"a dotted key opens the table", "package.name = \"dotted\"\n", "package", "name", "dotted", true},
		{"a quoted table header", "[\"package\"]\nname = \"q\"\n", "package", "name", "q", true},
		{"same key in another table is not the answer", "[dependencies]\nname = \"dep\"\n", "package", "name", "", false},
		{"a key after the table closed is not in it", "[package]\nversion = \"1\"\n[lib]\nname = \"libname\"\n", "package", "name", "", false},
		{"a non-string value is not a string", "[package]\nname = { workspace = true }\n", "package", "name", "", false},
		{"a virtual manifest has no package", "[workspace]\nmembers = [\"a\"]\n", "package", "name", "", false},
		{"a # inside the string is kept", "[package]\nname = \"a#b\"\n", "package", "name", "a#b", true},
	}
	for _, c := range cases {
		got, ok := StringValue(c.text, c.table, c.key)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// Paths names every header and every key by its full path, so a caller can
// ask whether a text declares something as a table or as a key alike.
func TestPaths(t *testing.T) {
	text := "# top\nname = \"x\"\n[package]\nversion = \"1\" # v\n[target.'cfg(unix)'.dependencies]\nlibc = \"0.2\"\n[[bin]]\nname = \"b\"\ndependencies.itoa = \"1\"\nnot a key\n"
	want := []string{"name", "package", "package.version", "target.cfg(unix).dependencies", "target.cfg(unix).dependencies.libc", "bin", "bin.name", "bin.dependencies.itoa"}
	if got := Paths(text); !slices.Equal(got, want) {
		t.Errorf("Paths = %q\nwant    %q", got, want)
	}
}
