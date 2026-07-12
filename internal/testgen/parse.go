// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// extractCode pulls Go source out of a model response. It handles a
// ```go-fenced block (fence + optional language tag on the fence's own
// line), a bare ```-fenced block, and a no-fence response (returned
// trimmed as-is). Only the first fenced block is considered.
//
// A fence is only recognized when the ``` starts its own line — a real
// markdown fence is line-delimited, whereas a ``` embedded mid-line (e.g.
// inside a Go raw string literal or a comment) is not. This keeps an
// embedded ``` in the generated code from truncating the extraction early.
func extractCode(resp string) string {
	start := lineFenceIndex(resp, 0)
	if start == -1 {
		return strings.TrimSpace(resp)
	}
	rest := resp[start+3:]
	// Skip an optional language tag up to the end of the fence's own line.
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		rest = rest[nl+1:]
	} else {
		rest = ""
	}
	end := lineFenceIndex(rest, 0)
	if end == -1 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// lineFenceIndex returns the byte offset (relative to s) of the first ```
// that starts a line within s[from:], or -1 if none. A fence "starts a
// line" if it is at the beginning of s or immediately preceded by '\n'.
func lineFenceIndex(s string, from int) int {
	const fence = "```"
	for i := from; ; {
		rel := strings.Index(s[i:], fence)
		if rel == -1 {
			return -1
		}
		idx := i + rel
		if idx == 0 || s[idx-1] == '\n' {
			return idx
		}
		i = idx + len(fence)
	}
}

// parseMutants splits a "===MUTATION_N===" delimited response into mutants.
// The code for each mutation is the text between its marker and the next
// marker (or end), fence-stripped and trimmed. Empty blocks are skipped; IDs
// are assigned sequentially m1, m2, ... over the kept blocks.
func parseMutants(resp string) []adequacy.Mutant {
	const mark = "===MUTATION_"
	var out []adequacy.Mutant
	parts := strings.Split(resp, mark)
	for _, p := range parts[1:] { // parts[0] is any preamble before the first marker
		// p looks like "1===\n<code>...": drop up to and including the marker's closing "==="
		close := strings.Index(p, "===")
		if close < 0 {
			continue
		}
		body := p[close+3:]
		// A trailing "..._END===" (or the next marker, already split off) may remain — cut at any residual "===".
		if e := strings.Index(body, "==="); e >= 0 {
			body = body[:e]
		}
		code := extractCode(body)
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, adequacy.Mutant{ID: fmt.Sprintf("m%d", len(out)+1), Code: code})
	}
	return out
}
