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
}

// ErrUnsupportedLang is returned for a language with no signature extractor
// wired (never a silent empty result).
var ErrUnsupportedLang = errors.New("repoindex: no signature extractor for language")

// ExtractSignatures returns the callable surface of text in lang. The concrete
// implementation is build-tagged: tree-sitter under cgo, a stub otherwise.
func ExtractSignatures(text, lang string) ([]Signature, error) {
	return extractSignatures(text, lang)
}
