//go:build !cgo

package repoindex

// supported reports whether lang has a tree-sitter grammar registered.
// Since CGO is disabled, no grammars are available, so always return false.
func supported(lang string) bool {
	return false
}
