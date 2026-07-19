// SPDX-License-Identifier: Elastic-2.0

package repoindex

import "errors"

type Param struct {
	Name string
	Type string
}

type Signature struct {
	Name     string
	Kind     string
	Receiver string
	Params   []Param
	Results  []string
	Exported bool
	Line     int
	// Complexity is a cyclomatic-style difficulty measure for this symbol:
	// 1 + the number of branch, loop, case, catch, and boolean-operator nodes
	// in its subtree. It is the difficulty CONTROL for model-effectiveness
	// comparison — a model that is fine on getters and collapses on
	// branch-heavy code reads as merely "average" when yield is pooled across
	// difficulty. Also the bin-packing weight for shard balancing.
	//
	// The branch-node set is per-language, so complexity numbers are NOT
	// strictly comparable ACROSS languages; band within a corpus instead.
	// 0 when unavailable (the nocgo path returns no signatures at all).
	Complexity int
	// Lines is the symbol's inclusive line span (end row - start row + 1).
	Lines int
}

// ErrUnsupportedLang is returned for a language with no signature extractor
// wired (never a silent empty result).
var ErrUnsupportedLang = errors.New("repoindex: no signature extractor for language")

// ExtractSignatures returns the callable surface of text in lang. The concrete
// implementation is build-tagged: tree-sitter under cgo, a stub otherwise.
func ExtractSignatures(text, lang string) ([]Signature, error) {
	return extractSignatures(text, lang)
}
