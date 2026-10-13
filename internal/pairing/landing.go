// SPDX-License-Identifier: Elastic-2.0

package pairing

// Landing is where an authored test for a source is WRITTEN — the fourth
// question pairing answers, beside Primary (what a new test file is called),
// Resolve (which test already exists) and BestClaim (who keeps a contested
// one). It exists because for a content language those answers differ: a
// Rust source whose own declaration loads app_id_tests.rs is PAIRED to that
// file, and an authored test belongs in it too — appended, as a module the
// project's test module holds — not in a new file no `mod` declaration
// would ever compile.
type Landing struct {
	// Path is the file the authored test is written to, in the frame the
	// source path was given in (root-relative and slash-separated for a
	// scan). "" only when no rule names any file for the source.
	Path string
	// Shape and Label are the rule that chose Path, as Resolution carries
	// them ("path-attr", "default-module", "inline", or a path Shape's name).
	Shape Shape
	Label string
	// Into: Path is an EXISTING file the authored test is appended to —
	// composed onto what is there, never written over it. True for every
	// content landing (the file the source declares, or the source itself);
	// false for a path landing, which is a file of its own.
	Into bool
	// Depth is how many module levels Path sits below the source's own
	// module (shapeTraits.moduleDepth): 0 for the source itself, 1 for a
	// file the source declares. Meaningful only when Into.
	Depth int
	// DeclarationsRead is true when the source's own test-module
	// declarations were read (the caller supplied Sources.TestModules). A
	// content language whose declarations were NOT read lands on Primary —
	// for Rust the source itself — so an entry with no repository to read
	// from fails closed instead of guessing, and a refusal can say which of
	// the two reasons it is.
	DeclarationsRead bool
	// RefusedOutsideRoot is every path a declaration named outside
	// Sources.Root, never read and never landed in (see Resolution's).
	RefusedOutsideRoot []string
}

// Land answers where an authored test for codePath is written, from the SAME
// declared-module resolution Resolve runs first (resolveDeclared), so the
// file a source is paired to and the file its authored test lands in are one
// derivation:
//
//   - A content rule that resolves — the file the source's own `mod`
//     declaration loads, or the source itself for an inline module — is the
//     landing, Into, at that Shape's module depth.
//   - Otherwise Primary: the naming answer. For a path language that is a
//     new file beside or under the tests (Into false), exactly the name
//     corral has always written. For a content language it is the Inline
//     candidate — the source itself, Into at depth 0 — which is what a
//     source with no test module, or one whose declarations could not be
//     read, lands on.
//
// Only the declared stage runs: no convention probe and no search, which a
// landing never reads. A path language therefore costs nothing here, and
// its TestModules is never called. src.TestModules == nil is allowed and
// means "no repository to read the declarations from" (DeclarationsRead
// false); src.Exists must be set whenever TestModules is. The error is
// TestModules' own: a file that could not be read has not been shown to
// declare no test.
func Land(rules []Rule, codePath string, src Sources) (Landing, error) {
	var l Landing
	content := contentRules(rules)
	if len(content) > 0 && src.TestModules != nil {
		res, err := resolveDeclared(content, codePath, src)
		if err != nil {
			return Landing{}, err
		}
		l.DeclarationsRead = true
		l.RefusedOutsideRoot = res.RefusedOutsideRoot
		if res.Found {
			l.Path, l.Shape, l.Label = res.Path, res.Shape, res.Label
			l.Into, l.Depth = true, traitsOf(res.Shape).moduleDepth
			return l, nil
		}
	}
	c, ok := Primary(rules, codePath)
	if !ok {
		return l, nil
	}
	t := traitsOf(c.Shape)
	l.Path, l.Shape, l.Label = c.Path, c.Shape, string(c.Shape)
	l.Into, l.Depth = t.content, t.moduleDepth
	return l, nil
}
