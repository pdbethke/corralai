//go:build !cgo

package repoindex

import (
	"fmt"
)

// chunkSymbols is a pure-Go dummy fallback when tree-sitter is unavailable.
// It returns an error, forcing chunkFile to use the line-window chunker (chunkLines).
func chunkSymbols(text, lang string) ([]LineChunk, error) {
	return nil, fmt.Errorf("tree-sitter grammar parsing unavailable without CGO")
}
