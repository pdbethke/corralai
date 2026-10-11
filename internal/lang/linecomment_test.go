// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// TestEveryPluginDeclaresALineComment: the authored-test record and the
// shard preamble both write comments in the audited language; a plugin with
// no marker (or a wrong one) writes a line that does not parse.
func TestEveryPluginDeclaresALineComment(t *testing.T) {
	want := map[string]string{"go": "//", "javascript": "//", "typescript": "//", "php": "//", "python": "#", "ruby": "#"}
	for _, name := range Names() {
		got := registry[name].LineComment()
		if got == "" {
			t.Errorf("%s: LineComment is empty", name)
		}
		if w, ok := want[name]; ok && got != w {
			t.Errorf("%s: LineComment = %q, want %q (the value both advpool helpers agreed on before B3)", name, got, w)
		}
	}
}
