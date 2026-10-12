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
