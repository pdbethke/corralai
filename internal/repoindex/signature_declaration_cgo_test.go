//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// signatureSamples is one source file per language: one function with one
// branch, the least a reader must find to split, prompt and count by.
var signatureSamples = map[string]string{
	"go":         "package a\n\nfunc F(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
	"python":     "def f(x):\n    if x > 0:\n        return 1\n    return 0\n",
	"ruby":       "def f(x)\n  if x > 0\n    1\n  else\n    0\n  end\nend\n",
	"javascript": "function f(x) {\n  if (x > 0) { return 1; }\n  return 0;\n}\n",
	"typescript": "function f(x: number): number {\n  if (x > 0) { return 1; }\n  return 0;\n}\n",
	"php":        "<?php\nfunction f($x) {\n  if ($x > 0) { return 1; }\n  return 0;\n}\n",
	"rust":       "pub fn f(x: i32) -> i32 {\n    if x > 0 {\n        return 1;\n    }\n    0\n}\n",
	"swift":      "public func f(_ x: Int) -> Int {\n    if x > 0 {\n        return 1\n    }\n    return 0\n}\n",
}

// TestSignatureDeclarationMatchesTheReader: lang.SignatureReader is a
// declaration, because the reader lives here and this package imports lang.
// So the reader is run on a sample for every registered language, and the
// declaration must say what it did: a function found, with its branch.
// Like reposcan's check of lang.EvidencePromoter, it checks samples, one
// per language, and not every shape a real file can take.
func TestSignatureDeclarationMatchesTheReader(t *testing.T) {
	for _, n := range lang.Names() {
		p, _ := lang.ByName(n)
		src, ok := signatureSamples[n]
		if !ok {
			t.Fatalf("%s has no sample here; add one", n)
		}
		sigs, err := ExtractSignatures(src, n)
		reads := false
		for _, s := range sigs {
			if err == nil && s.Name != "" && len(s.Decisions) > 0 {
				reads = true
			}
		}
		if declared := lang.ReadsSignatures(p); declared != reads {
			t.Errorf("%s: lang declares signatures %v, but the reader found a function with its branch: %v (%d signature(s), err %v)", n, declared, reads, len(sigs), err)
		}
	}
}
