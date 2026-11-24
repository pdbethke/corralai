// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"errors"

	plugins "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

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
	// Decisions are the line spans of the branch nodes Complexity counted —
	// every if/loop/case/catch/boolean operator in the symbol, as the
	// inclusive 1-based line range of the node's own subtree — so
	// len(Decisions) == Complexity-1. They are what lets an exam's coverage
	// be computed: a decision point is PROBED when some mutant's span
	// overlaps it. nil when unavailable (the nocgo path).
	Decisions []Decision
}

// Decision is one decision point's inclusive 1-based line span. A fault
// planted anywhere inside the span exercises that decision's path.
type Decision struct {
	Start, End int
}

// ErrUnsupportedLang is returned for a language with no signature extractor
// wired (never a silent empty result).
var ErrUnsupportedLang = errors.New("repoindex: no signature extractor for language")

// ErrIncompleteParse is returned when the grammar could not parse part of the
// file outside every function it read, so the functions read may not be all
// of them (unreadSpan says why that is refused rather than returned).
var ErrIncompleteParse = errors.New("repoindex: the grammar did not parse the whole file")

// ExtractSignatures returns the callable surface of text in lang. The concrete
// implementation is build-tagged: tree-sitter under cgo, a stub otherwise.
func ExtractSignatures(text, lang string) ([]Signature, error) {
	return extractSignatures(text, lang)
}

// ReadsTestModules reports whether this build can read lang's test-module
// declarations at all: false for a language with no modules reader, and for
// EVERY language in a no-cgo build (the parse needs the grammar). A caller
// whose language declares its tests (a content rule) must not take
// TestModules' nil from a build that cannot read as "declares none".
func ReadsTestModules(lang string) bool { return readsTestModules(lang) }

// TestModules returns the test modules src declares in itself, for a language
// whose row has a modules reader (Rust); every other language answers nil.
// Build-tagged like ExtractSignatures: the parse needs the grammar, so a
// no-cgo build answers nil.
func TestModules(lang string, src []byte) []pairing.TestModule {
	return testModules(lang, src)
}

// OwnTestLines returns the 1-based line spans of src that hold its OWN test
// code, for a language whose tests can live in the source (Rust): every test
// module and every test-only item the grammar placed, at any nesting level
// (TestModules reads only the top level, which is what pairing needs and
// not what a guard over the file's lines needs). They are the audit's
// evidence, so a grading run refuses a mutant that edits them
// (adequacy.WithEvidenceLines).
//
// unread is non-empty when the parse set code aside (an ERROR or MISSING
// node) outside every span it found and every function it placed: test code
// there is invisible to the walk, so the spans are no reading of where the
// tests are, and the caller must invalidate every mutant
// (adequacy.WithEvidenceUnread) rather than trust them. A language with no
// reader, and every language in a no-cgo build, answers none.
func OwnTestLines(lang string, src []byte) (lines []plugins.LineRange, unread string) {
	return ownTestLines(lang, src)
}
