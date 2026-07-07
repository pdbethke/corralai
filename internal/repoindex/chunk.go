// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"strings"
)

// LineChunk holds a contiguous window of source lines with optional symbol metadata.
type LineChunk struct {
	Seq       int
	StartLine int
	EndLine   int
	Text      string
	Symbol    string // populated by language-aware chunker; empty in fallback / preamble / gap
	Kind      string // e.g. "function", "type", "method"; empty in fallback / gap
	Lang      string // e.g. "go", "python"; tagged from file extension
}

// defSpan records a definition's line span before building LineChunks.
type defSpan struct {
	symbol string
	kind   string
	start  int // 1-indexed
	end    int // 1-indexed
}

// chunkFile dispatches by language: uses chunkSymbols for supported languages,
// falling back to chunkLines on parse error, empty capture, or unsupported lang.
// ALWAYS returns chunks — indexing never fails.
func chunkFile(path, text string) []LineChunk {
	lang := langForExt(path)
	if supported(lang) {
		if cs, err := chunkSymbols(text, lang); err == nil && len(cs) > 0 {
			return cs
		}
	}
	cs := chunkLines(text, 60, 15)
	for i := range cs {
		cs[i].Lang = lang
	}
	return cs
}



// subLines joins lines[from-1 : to] (1-indexed, inclusive) from a pre-split slice.
func subLines(lines []string, from, to int) string {
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from > to {
		return ""
	}
	return strings.Join(lines[from-1:to], "\n")
}

// chunkLines splits text into overlapping windows of `window` lines stepping by
// (window-overlap). Lines are 1-based; the final short window is included.
func chunkLines(text string, window, overlap int) []LineChunk {
	if window <= 0 {
		window = 60
	}
	if overlap < 0 || overlap >= window {
		overlap = window / 4
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // drop the empty tail from a trailing newline
	}
	if len(lines) == 0 {
		return nil
	}
	step := window - overlap
	var out []LineChunk
	seq := 0
	for start := 0; start < len(lines); start += step {
		end := start + window
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, LineChunk{Seq: seq, StartLine: start + 1, EndLine: end, Text: strings.Join(lines[start:end], "\n")})
		seq++
		if end == len(lines) {
			break
		}
	}
	return out
}
