// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// MutantParseDiag accounts for every block a generator emitted and why each
// was rejected.
//
// WHY THIS EXISTS. parseMutants drops a block at three different gates — the
// marker is unclosed, a SEARCH/DIVIDER/REPLACE marker is missing, or the
// anchor does not apply — and every one of them used to collapse into the
// single sentence "generator returned no parseable, cleanly-applying
// mutations". Those are wildly different diagnoses: the first two mean the
// model is emitting the wrong FORMAT, the third means the format was perfect
// and the bytes did not line up.
//
// The case that motivated this was a model whose blocks were flawless and
// whose mutations were sound, but which emitted every SEARCH line with one
// extra leading tab. Every block was correctly refused, and finding out why
// meant replaying the prompt against the live model by hand. WhitespaceOnly
// exists so that costs one line of output instead.
//
// NOTE THE DELIBERATE NON-FIX: a whitespace-only mismatch is REPORTED, never
// silently accepted. The anchor must match exact bytes because that is what
// makes a mutant a provable single-point edit of known source; a
// whitespace-tolerant match could anchor to the wrong span and every mutant
// downstream would inherit the doubt. Diagnose, do not auto-correct.
type MutantParseDiag struct {
	Blocks int // blocks the response contained
	Kept   int // blocks that produced a usable mutant

	// Malformed covers both format gates: an unclosed "===MUTATION_n===" marker
	// and a missing SEARCH/DIVIDER/REPLACE marker. Both mean "wrong format".
	Malformed int
	// NoOp is an empty SEARCH, or a REPLACE identical to it.
	NoOp int
	// AnchorNotFound means SEARCH is absent from the original's bytes.
	AnchorNotFound int
	// AnchorNotUnique means SEARCH occurs more than once, so the edit site is
	// ambiguous.
	AnchorNotUnique int
	// IntegrityFailed means the round-trip check refused the edit: undoing it
	// did not reproduce the original byte-for-byte.
	IntegrityFailed int
	// WhitespaceOnly is the subset of AnchorNotFound whose SEARCH WOULD match
	// if leading whitespace were normalized on both sides — i.e. the model
	// understood the code and got the indentation wrong.
	WhitespaceOnly int
}

// Error returns nil when at least one mutant was kept, and otherwise an error
// naming what actually went wrong.
func (d MutantParseDiag) Error() error {
	if d.Kept > 0 {
		return nil
	}
	if d.Blocks == 0 {
		return fmt.Errorf("testgen: generator emitted no %q blocks at all — the model is not following the output format", "===MUTATION_n===")
	}
	var why []string
	if d.Malformed > 0 {
		why = append(why, fmt.Sprintf("%d malformed (missing or unclosed SEARCH/REPLACE markers)", d.Malformed))
	}
	if d.NoOp > 0 {
		why = append(why, fmt.Sprintf("%d no-op (empty SEARCH, or REPLACE identical to it)", d.NoOp))
	}
	if d.AnchorNotUnique > 0 {
		why = append(why, fmt.Sprintf("%d anchor-not-unique (SEARCH occurs more than once)", d.AnchorNotUnique))
	}
	if d.IntegrityFailed > 0 {
		why = append(why, fmt.Sprintf("%d failed the integrity round-trip", d.IntegrityFailed))
	}
	if d.AnchorNotFound > 0 {
		why = append(why, fmt.Sprintf("%d anchor-not-found (SEARCH is not in the file's bytes)", d.AnchorNotFound))
	}
	msg := fmt.Sprintf("testgen: %d block(s) emitted, none usable: %s", d.Blocks, strings.Join(why, "; "))
	if d.WhitespaceOnly > 0 {
		msg += fmt.Sprintf(". NOTE: %d of the anchor-not-found block(s) WOULD match if leading whitespace were normalized — this model is not reproducing indentation exactly, which the prompt requires. The anchor is deliberately byte-exact and is not relaxed; use a model that copies indentation faithfully", d.WhitespaceOnly)
	}
	return fmt.Errorf("%s", msg)
}

// stripLeading removes leading spaces and tabs from every line, for comparing
// two texts that may differ only in indentation.
func stripLeading(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimLeft(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// parseMutantsDiag is parseMutants with the rejection accounting kept. It is
// the single implementation; parseMutants discards the diagnosis.
func parseMutantsDiag(resp, original string) ([]adequacy.Mutant, MutantParseDiag) {
	const mark = "===MUTATION_"
	parentHash := hex.EncodeToString(sha256Sum(original))
	var out []adequacy.Mutant
	var d MutantParseDiag

	strippedOriginal := stripLeading(original)
	for _, p := range strings.Split(resp, mark)[1:] { // [0] is any preamble
		d.Blocks++
		close := strings.Index(p, "===")
		if close < 0 {
			d.Malformed++
			continue
		}
		search, replace, ok := parseSearchReplace(p[close+3:])
		if !ok {
			d.Malformed++
			continue
		}
		switch {
		case search == "" || search == replace:
			d.NoOp++
			continue
		case !strings.Contains(original, search):
			d.AnchorNotFound++
			// Would it have matched but for indentation? That is the difference
			// between "this model cannot do the task" and "this model is one
			// tab off", and the operator cannot see it from the output.
			if s := stripLeading(search); s != "" && strings.Contains(strippedOriginal, s) {
				d.WhitespaceOnly++
			}
			continue
		}
		// The hunk is VALIDATED here and then KEPT as the mutant. Apply is the
		// same algorithm that used to run at this point and materialise the
		// whole file; the difference is that the file it produces is thrown
		// away and re-made inside the jail, so nothing between here and there
		// carries a copy of the source.
		m := adequacy.Mutant{
			ID:           fmt.Sprintf("m%d", len(out)+1),
			ParentSHA256: parentHash,
			// VERBATIM, whatever the model emitted. stripLeading above is a
			// DIAGNOSIS and never an anchor: a SEARCH whose indentation was
			// mangled is counted and dropped, never matched loosely. So the
			// only anchors that reach a Mutant are ones that matched these
			// exact bytes, and storing them as-is reproduces this same splice.
			Search:  search,
			Replace: replace,
		}
		if _, aerr := m.Apply(original); aerr != nil {
			// Apply refused after the anchor was known present, so the
			// remaining causes are non-uniqueness and the integrity round-trip.
			i := strings.Index(original, search)
			if strings.Contains(original[i+len(search):], search) {
				d.AnchorNotUnique++
			} else {
				d.IntegrityFailed++
			}
			continue
		}
		m.Span = adequacy.HunkSpan(original, search)
		d.Kept++
		out = append(out, m)
	}
	return out, d
}
