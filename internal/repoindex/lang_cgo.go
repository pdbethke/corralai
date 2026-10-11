// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package repoindex

import sitter "github.com/smacker/go-tree-sitter"

// grammar returns lang's tree-sitter grammar from its langSpecs row, or nil
// when repoindex cannot parse lang.
func grammar(lang string) *sitter.Language {
	if s, ok := langSpecs[lang]; ok && s.grammar != nil {
		return s.grammar()
	}
	return nil
}

func supported(lang string) bool { return grammar(lang) != nil }
