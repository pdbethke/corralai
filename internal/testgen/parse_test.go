// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"strings"
	"testing"
)

func TestExtractCode(t *testing.T) {
	cases := map[string]string{
		"```go\npackage p\nfunc T(){}\n```":       "package p\nfunc T(){}",
		"here you go:\n```\npackage p\n```\ndone": "package p",
		"package p\nfunc T(){}":                   "package p\nfunc T(){}", // no fence → trimmed as-is
		// A fence containing an embedded ``` mid-line (e.g. inside a Go raw
		// string literal) must NOT truncate early — only a ``` that starts
		// its own line closes the fence.
		"```go\npackage p\nconst s = `has ``` inside`\nfunc T(){}\n```": "package p\nconst s = `has ``` inside`\nfunc T(){}",
	}
	for in, want := range cases {
		if got := extractCode(in); got != want {
			t.Errorf("extractCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMutants(t *testing.T) {
	resp := "===MUTATION_1===\npackage target\nfunc F() int { return 1 }\n" +
		"===MUTATION_2===\n```go\npackage target\nfunc F() int { return 2 }\n```\n" +
		"===MUTATION_3===\npackage target\nfunc F() int { return 3 }\n===MUTATION_3_END==="
	muts := parseMutants(resp)
	if len(muts) != 3 {
		t.Fatalf("got %d mutants, want 3: %+v", len(muts), muts)
	}
	if muts[0].ID != "m1" || !strings.Contains(muts[0].Code, "return 1") {
		t.Errorf("m1 wrong: %+v", muts[0])
	}
	if muts[1].ID != "m2" || strings.Contains(muts[1].Code, "```") || !strings.Contains(muts[1].Code, "return 2") {
		t.Errorf("m2 wrong (fence not stripped?): %+v", muts[1])
	}
	if muts[2].ID != "m3" || strings.Contains(muts[2].Code, "MUTATION_3_END") {
		t.Errorf("m3 wrong (trailing marker leaked?): %+v", muts[2])
	}
}
