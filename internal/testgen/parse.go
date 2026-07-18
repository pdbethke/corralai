// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"crypto/sha256"
	"encoding/hex"
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

// SEARCH/REPLACE hunk markers — the industry-standard format for applying
// LLM-generated code edits (Aider, str_replace): a uniquely-anchored find/
// replace, cheap regardless of file size. Emitting the whole file per mutant
// does not scale (a 600-line file × N mutants overruns the model, which then
// returns one); a hunk is ~a few lines whatever the file size.
const (
	srSearchHead = "<<<<<<< SEARCH"
	srDivider    = "======="
	srReplaceEnd = ">>>>>>> REPLACE"
)

// parseMutants splits a "===MUTATION_N===" delimited response into mutants,
// each block a SEARCH/REPLACE hunk applied to `original`. Splitting on the
// literal "===MUTATION_" marker is safe next to the 7-equals SEARCH/REPLACE
// divider: the divider never contains "MUTATION_". IDs are assigned m1, m2, …
// over the kept (successfully-applied) blocks; a block whose hunk is malformed
// or does not apply cleanly is DROPPED, never scored.
func parseMutants(resp, original string) []adequacy.Mutant {
	const mark = "===MUTATION_"
	parentHash := hex.EncodeToString(sha256Sum(original))
	var out []adequacy.Mutant
	for _, p := range strings.Split(resp, mark)[1:] { // [0] is any preamble
		// p looks like "1===\n<hunk>…": drop up to and including the marker's closing "===".
		close := strings.Index(p, "===")
		if close < 0 {
			continue
		}
		search, replace, ok := parseSearchReplace(p[close+3:])
		if !ok {
			continue
		}
		mutant, ok := applyMutation(original, search, replace)
		if !ok {
			continue
		}
		out = append(out, adequacy.Mutant{
			ID:           fmt.Sprintf("m%d", len(out)+1),
			Code:         mutant,
			ParentSHA256: parentHash,
		})
	}
	return out
}

// parseSearchReplace pulls the SEARCH and REPLACE bodies out of one mutation
// block. The bodies are taken VERBATIM between the marker lines (only the
// single newline that ends each marker line is consumed), because SEARCH must
// match the original's exact bytes — indentation included — for a unique
// anchor. Anything after ">>>>>>> REPLACE" (a stray _END marker, prose) is
// ignored. ok=false if any of the three markers is absent.
func parseSearchReplace(block string) (search, replace string, ok bool) {
	si := strings.Index(block, srSearchHead)
	if si < 0 {
		return "", "", false
	}
	rest := afterMarkerLine(block[si+len(srSearchHead):])
	di := strings.Index(rest, "\n"+srDivider)
	if di < 0 {
		return "", "", false
	}
	search = rest[:di]
	rest = afterMarkerLine(rest[di+1+len(srDivider):])
	ri := strings.Index(rest, "\n"+srReplaceEnd)
	if ri < 0 {
		return "", "", false
	}
	replace = rest[:ri]
	return search, replace, true
}

// afterMarkerLine drops everything up to and including the first newline (the
// remainder of a marker's own line — e.g. a trailing "\r" or stray spaces).
func afterMarkerLine(s string) string {
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		return s[nl+1:]
	}
	return ""
}

// applyMutation applies one SEARCH/REPLACE hunk to original and returns the
// full mutant, GUARANTEEING it is original with exactly one contiguous region
// changed: SEARCH must be non-empty and occur EXACTLY once (a unique anchor),
// REPLACE must differ from SEARCH (a real mutation), and reversing the single
// splice must reproduce original byte-for-byte. Any violation returns ok=false
// so the caller drops the mutant — corral never scores a mutant it cannot prove
// is a faithful single-point derivative of the exact code under audit.
func applyMutation(original, search, replace string) (mutant string, ok bool) {
	if search == "" || search == replace {
		return "", false
	}
	i := strings.Index(original, search)
	if i < 0 {
		return "", false // anchor not found
	}
	if strings.Contains(original[i+len(search):], search) {
		return "", false // anchor not unique
	}
	mutant = original[:i] + replace + original[i+len(search):]
	// Integrity round-trip: undo the one change and demand the EXACT original
	// back — nothing outside the replaced span may have moved.
	if mutant[:i]+search+mutant[i+len(replace):] != original {
		return "", false
	}
	return mutant, true
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
