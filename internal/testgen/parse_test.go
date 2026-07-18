// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"encoding/hex"
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

const srOrig = "package target\n\nfunc F() int {\n\treturn 1\n}\n\nfunc G() int {\n\treturn 2\n}\n"

func srBlock(n, search, replace string) string {
	return "===MUTATION_" + n + "===\n" + srSearchHead + "\n" + search + "\n" + srDivider + "\n" + replace + "\n" + srReplaceEnd + "\n"
}

func TestParseMutants_AppliesSearchReplaceHunks(t *testing.T) {
	resp := srBlock("1", "\treturn 1", "\treturn 99") + srBlock("2", "\treturn 2", "\treturn -2") + "\n===MUTATION_2_END==="
	muts := parseMutants(resp, srOrig)
	if len(muts) != 2 {
		t.Fatalf("got %d mutants, want 2: %+v", len(muts), muts)
	}
	// Each mutant is the FULL original with exactly one region changed.
	want1 := strings.Replace(srOrig, "\treturn 1", "\treturn 99", 1)
	if muts[0].ID != "m1" || muts[0].Code != want1 {
		t.Errorf("m1:\n got %q\nwant %q", muts[0].Code, want1)
	}
	want2 := strings.Replace(srOrig, "\treturn 2", "\treturn -2", 1)
	if muts[1].ID != "m2" || muts[1].Code != want2 {
		t.Errorf("m2:\n got %q\nwant %q", muts[1].Code, want2)
	}
	// Tamper-evident: each mutant carries the hash of the EXACT original it
	// derives from (the trust link the user asked for).
	wantHash := hex.EncodeToString(sha256Sum(srOrig))
	if muts[0].ParentSHA256 != wantHash || muts[1].ParentSHA256 != wantHash {
		t.Errorf("ParentSHA256 must equal sha256(original) %s; got %s / %s", wantHash, muts[0].ParentSHA256, muts[1].ParentSHA256)
	}
}

func TestParseMutants_DropsUnappliableHunks(t *testing.T) {
	// "\treturn 1" occurs twice here -> an ambiguous anchor that must be dropped.
	orig := "func F() int {\n\treturn 1\n}\nfunc H() int {\n\treturn 1\n}\nfunc G() bool {\n\treturn true\n}\n"
	resp := srBlock("1", "\treturn 1", "\treturn 2") + // ambiguous anchor -> drop
		srBlock("2", "\treturn 404", "\treturn 0") + // anchor not found -> drop
		srBlock("3", "func G() bool {", "func G() bool {") + // no-op -> drop
		srBlock("4", "\treturn true", "\treturn false") // unique + real -> keep
	muts := parseMutants(resp, orig)
	if len(muts) != 1 {
		t.Fatalf("want 1 kept mutant (ambiguous/not-found/no-op dropped), got %d: %+v", len(muts), muts)
	}
	if !strings.Contains(muts[0].Code, "\treturn false") || strings.Contains(muts[0].Code, "\treturn true") {
		t.Errorf("kept mutant should apply the unique real edit: %q", muts[0].Code)
	}
	// IDs renumber over KEPT blocks only.
	if muts[0].ID != "m1" {
		t.Errorf("kept mutant ID = %q, want m1", muts[0].ID)
	}
}

func TestApplyMutation_IntegrityGuarantees(t *testing.T) {
	orig := "abc\ndef\nghi\n"
	if m, ok := applyMutation(orig, "def", "DEF"); !ok || m != "abc\nDEF\nghi\n" {
		t.Fatalf("unique real edit: got (%q, %v)", m, ok)
	}
	if _, ok := applyMutation(orig, "def", "def"); ok {
		t.Error("no-op (REPLACE == SEARCH) must be rejected")
	}
	if _, ok := applyMutation(orig, "", "x"); ok {
		t.Error("empty SEARCH must be rejected")
	}
	if _, ok := applyMutation(orig, "zzz", "y"); ok {
		t.Error("anchor-not-found must be rejected")
	}
	if _, ok := applyMutation("aa\naa\n", "aa", "bb"); ok {
		t.Error("ambiguous (non-unique) anchor must be rejected")
	}
}
