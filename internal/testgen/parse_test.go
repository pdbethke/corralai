// SPDX-License-Identifier: Elastic-2.0

package testgen

import "testing"

func TestExtractCode(t *testing.T) {
	cases := map[string]string{
		"```go\npackage p\nfunc T(){}\n```":       "package p\nfunc T(){}",
		"here you go:\n```\npackage p\n```\ndone": "package p",
		"package p\nfunc T(){}":                   "package p\nfunc T(){}", // no fence → trimmed as-is
	}
	for in, want := range cases {
		if got := extractCode(in); got != want {
			t.Errorf("extractCode(%q) = %q, want %q", in, got, want)
		}
	}
}
