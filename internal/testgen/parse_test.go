// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
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
	// A mutant IS its hunk: the anchor and its replacement are retained
	// VERBATIM, and the full file exists only when Apply materialises it.
	if muts[0].Search != "\treturn 1" || muts[0].Replace != "\treturn 99" {
		t.Errorf("m1 hunk = %q -> %q, want the verbatim SEARCH/REPLACE bodies", muts[0].Search, muts[0].Replace)
	}
	if muts[1].Search != "\treturn 2" || muts[1].Replace != "\treturn -2" {
		t.Errorf("m2 hunk = %q -> %q", muts[1].Search, muts[1].Replace)
	}
	// Each mutant applies to the FULL original with exactly one region changed.
	want1 := strings.Replace(srOrig, "\treturn 1", "\treturn 99", 1)
	got1, err := muts[0].Apply(srOrig)
	if muts[0].ID != "m1" || err != nil || got1 != want1 {
		t.Errorf("m1:\n got %q (err=%v)\nwant %q", got1, err, want1)
	}
	want2 := strings.Replace(srOrig, "\treturn 2", "\treturn -2", 1)
	got2, err := muts[1].Apply(srOrig)
	if muts[1].ID != "m2" || err != nil || got2 != want2 {
		t.Errorf("m2:\n got %q (err=%v)\nwant %q", got2, err, want2)
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
	code, err := muts[0].Apply(orig)
	if err != nil {
		t.Fatalf("kept mutant does not apply: %v", err)
	}
	if !strings.Contains(code, "\treturn false") || strings.Contains(code, "\treturn true") {
		t.Errorf("kept mutant should apply the unique real edit: %q", code)
	}
	// IDs renumber over KEPT blocks only.
	if muts[0].ID != "m1" {
		t.Errorf("kept mutant ID = %q, want m1", muts[0].ID)
	}

	// An empty SEARCH is now OVERLOADED: adequacy.Mutant treats Search=="" as
	// the v1 WHOLE-FILE shape, where Replace IS the entire file. That shape is
	// only ever CONSTRUCTED by the v1 reader. A model that emits an empty
	// SEARCH must still be refused here as a no-op — accepting it would turn a
	// hunk the generator botched into a whole-file mutant whose "Replace" is
	// three lines of code, silently replacing the file under audit with them.
	t.Run("an empty SEARCH is a no-op, never the whole-file shape", func(t *testing.T) {
		ms, d := parseMutantsDiag(srBlock("5", "", "x"), orig)
		if len(ms) != 0 {
			t.Fatalf("an empty SEARCH must produce NO mutant, got %+v", ms)
		}
		if d.NoOp != 1 {
			t.Fatalf("diag = %+v, want the empty SEARCH counted as 1 no-op", d)
		}
	})
}

// TestWhitespaceMangledSearchIsRefusedNotAnchored pins the subtlety the hunk
// representation depends on: stripLeading is a DIAGNOSIS. A SEARCH whose
// leading whitespace the model mangled is counted as WhitespaceOnly and the
// block is DROPPED — it is never re-anchored on the stripped bytes. That is
// what makes it safe for Mutant.Search to store the model's verbatim SEARCH:
// the only anchors that ever reach a Mutant are ones that matched exactly.
func TestWhitespaceMangledSearchIsRefusedNotAnchored(t *testing.T) {
	original := "def f():\n    return 1\n"
	// A tab where the file has four spaces: identical once leading whitespace
	// is normalized, and absent from the file's actual bytes.
	resp := srBlock("1", "\treturn 1", "\treturn 99")
	muts, d := parseMutantsDiag(resp, original)
	if len(muts) != 0 {
		t.Fatalf("a whitespace-mangled anchor must produce NO mutant, got %+v", muts)
	}
	if d.AnchorNotFound != 1 || d.WhitespaceOnly != 1 {
		t.Fatalf("diag = %+v, want 1 anchor-not-found of which 1 whitespace-only", d)
	}
}

func TestParsedMutantsCarryTheirSpan(t *testing.T) {
	// Use the existing SEARCH/REPLACE parsing test's fixture shape; assert
	// every kept mutant has a non-zero Span within the original's line count.
	original := "def f():\n    return 1\n\ndef g():\n    return 2\n"
	raw := "===MUTATION_1===\n<<<<<<< SEARCH\n    return 2\n=======\n    return 3\n>>>>>>> REPLACE\n"
	ms, _ := parseMutantsDiag(raw, original)
	if len(ms) != 1 || ms[0].Span != (lang.LineRange{Start: 5, End: 5}) {
		t.Fatalf("got %+v", ms)
	}
	if ms[0].Search != "    return 2" || ms[0].Replace != "    return 3" {
		t.Fatalf("hunk must be retained verbatim, got %q -> %q", ms[0].Search, ms[0].Replace)
	}
}
