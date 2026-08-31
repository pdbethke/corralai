// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// AuthoredPart is ONE proven authored test: the source the writer returned for
// one survivor, and the id of the survivor it was proven against.
//
// The mutant id is carried, not just the source, because it is the only
// disambiguator available when two independently-authored tests collide on a
// name — see TestConcatenator.
type AuthoredPart struct {
	MutantID string
	Source   string
	// Reason is why this part could NOT be merged into the shared file, set
	// by ConcatAuthored from the concatenator's own error. Empty on a part
	// that merged (or that nobody tried to merge yet).
	//
	// It is carried because "here is a second file" without "because these
	// two both declare helper()" is an unexplained inconvenience, and the
	// operator is the one who has to decide what to do about it.
	Reason string
}

// TestConcatenator is the OPTIONAL plugin extension that folds several
// separately-authored, separately-PROVEN test files into the one file an
// operator can actually paste into their suite.
//
// It exists because the writer seat now runs one call PER SURVIVOR: each
// returned test is proven alone, against its own mutant, in its own tree. That
// is the measurement corral wants — a proof that names exactly which survivor
// it killed — but it is not what a developer wants handed to them. Twenty-four
// files each declaring `import pytest` and a `def test_x` is not a deliverable.
//
// The merge is deliberately CONSERVATIVE. It does exactly two things:
//
//  1. hoists and de-duplicates the header lines a language allows only once
//     per file (Go's package clause and imports, Python's imports); and
//  2. suffixes a TEST name that two or more parts both declare with the
//     declaring part's mutant id, so neither proven test is shadowed by the
//     other.
//
// Anything else — most importantly a non-test helper two parts both declare —
// is REFUSED with an error rather than guessed at. Renaming a helper means
// rewriting its call sites, which are the model's own code; a merge that
// produces a file which does not compile would take a set of individually
// proven tests and hand back something worse than any one of them. The caller
// (see ConcatAuthored) carries a refused part out separately instead, so the
// proof is still delivered — just not in the same file.
//
// A language whose file structure corral cannot rewrite precisely simply does
// not implement this, exactly as FailureDeselector is left unimplemented where
// a runner's output cannot be parsed precisely. Guessing is the failure mode
// this interface is shaped to avoid.
type TestConcatenator interface {
	ConcatTests(parts []AuthoredPart) (string, error)
}

// ConcatAuthored folds parts into one file using p's concatenator, returning
// the merged source and the parts that could NOT be merged into it.
//
// It is the fold every caller wants, and it is here rather than at the call
// site because the incremental shape is load-bearing: ConcatTests is
// all-or-nothing over the slice it is handed, so the only way to learn WHICH
// part is unmergeable is to add them one at a time and keep the last
// successful result. A caller that simply called ConcatTests once and dropped
// everything on error would discard proven tests wholesale because of one
// duplicated helper.
//
// A plugin with no concatenator returns ("", every part) — never a silently
// concatenated blob, and never nothing at all. The proofs are real either way;
// the caller reports them separately.
func ConcatAuthored(p Plugin, parts []AuthoredPart) (merged string, extra []AuthoredPart) {
	c, ok := p.(TestConcatenator)
	if !ok {
		out := make([]AuthoredPart, 0, len(parts))
		for _, part := range parts {
			part.Reason = "lang: " + p.Name() + " has no test concatenator, so each proven test stays its own file"
			out = append(out, part)
		}
		return "", out
	}
	var kept []AuthoredPart
	for _, part := range parts {
		candidate := append(append([]AuthoredPart(nil), kept...), part)
		out, err := c.ConcatTests(candidate)
		if err != nil {
			part.Reason = err.Error()
			extra = append(extra, part)
			continue
		}
		kept, merged = candidate, out
	}
	return merged, extra
}

// idSuffix renders a mutant id as an identifier fragment: "s0/m1" becomes
// "s0m1". Every character a language would refuse in an identifier is dropped
// rather than replaced, so two ids that differ only in punctuation ("s0/m1"
// and "s0-m1") cannot collapse onto the same suffix by way of a shared
// substitute character.
func idSuffix(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// concatSpec is the language-specific half of the shared merge below: which
// top-level lines are hoisted headers, and how a top-level declaration is
// recognised and classified as a test.
type concatSpec struct {
	// isHeader reports whether a top-level line is a de-duplicated header
	// (an import). It never sees an indented line.
	isHeader func(line string) bool
	// declRes are the regexps that name a declaration; each must have
	// exactly one capture group holding the name.
	declRes []*regexp.Regexp
	// renameOnCollision reports whether a name two parts both declare may be
	// SUFFIXED with its mutant id (a test, or — in JS — any local helper)
	// rather than refused. False means the collision is returned as an error
	// and the part is carried out separately.
	renameOnCollision func(name string) bool
	// declAnyIndent widens declaration matching past column zero. Ruby needs
	// it: a Minitest `def test_x` lives INSIDE `class FooTest`, indented, and
	// a top-level-only scan would see no declarations at all and merge two
	// silent overrides.
	declAnyIndent bool
	// precheck runs before anything is merged, for a refusal a line-by-line
	// scan cannot express (JS's same-module-different-specifiers, Ruby's
	// mixed frameworks). nil means nothing to check.
	precheck func(parts []AuthoredPart) error
}

// mergeParts is the shared body of every ConcatTests implementation: hoist and
// de-duplicate headers, rename colliding test declarations, refuse colliding
// helpers, and join what is left.
//
// headerPrefix is emitted verbatim before the hoisted headers (Go's package
// clause; empty for Python) and headerWrap renders the de-duplicated header
// lines (Go folds them into one import block; Python emits them as they were).
func mergeParts(parts []AuthoredPart, spec concatSpec, headerPrefix string, headerWrap func([]string) string) (string, error) {
	if spec.precheck != nil {
		if err := spec.precheck(parts); err != nil {
			return "", err
		}
	}
	var headers []string
	seenHeader := map[string]bool{}
	bodies := make([]string, 0, len(parts))

	// Which parts declare which top-level name. A name declared by one part
	// is left exactly as the writer wrote it — a suffix is a repair, not a
	// policy, and renaming a name nothing collides with would make the
	// delivered file differ from the file that was actually proven.
	declaredBy := map[string][]int{}
	partDecls := make([]map[string]bool, len(parts))

	for i, part := range parts {
		partDecls[i] = map[string]bool{}
		var body []string
		for _, line := range strings.Split(part.Source, "\n") {
			trimmed := strings.TrimRight(line, " \t")
			if strings.TrimSpace(trimmed) == "" {
				body = append(body, "")
				continue
			}
			isTopLevel := trimmed[0] != ' ' && trimmed[0] != '\t'
			if isTopLevel && spec.isHeader(trimmed) {
				if !seenHeader[trimmed] {
					seenHeader[trimmed] = true
					headers = append(headers, trimmed)
				}
				continue
			}
			if isTopLevel || spec.declAnyIndent {
				probe := trimmed
				if !isTopLevel {
					probe = strings.TrimLeft(trimmed, " \t")
				}
				for _, re := range spec.declRes {
					if m := re.FindStringSubmatch(probe); m != nil {
						if !partDecls[i][m[1]] {
							partDecls[i][m[1]] = true
							declaredBy[m[1]] = append(declaredBy[m[1]], i)
						}
						break
					}
				}
			}
			body = append(body, trimmed)
		}
		bodies = append(bodies, strings.Trim(strings.Join(body, "\n"), "\n"))
	}

	// Deterministic order: map iteration is random, and this function's output
	// is handed to an operator and stored in a ledger.
	names := make([]string, 0, len(declaredBy))
	for name := range declaredBy {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		owners := declaredBy[name]
		if len(owners) < 2 {
			continue
		}
		if !spec.renameOnCollision(name) {
			// REFUSED, not guessed at. See TestConcatenator's doc: renaming a
			// helper means rewriting call sites that are the model's own code,
			// and a merged file that does not compile is worse than any one of
			// the proven parts on its own.
			return "", fmt.Errorf("lang: %d authored parts each declare %q, which is not a test and cannot be renamed safely", len(owners), name)
		}
		word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		for _, i := range owners {
			bodies[i] = word.ReplaceAllString(bodies[i], name+"_"+idSuffix(parts[i].MutantID))
		}
	}

	var out strings.Builder
	if headerPrefix != "" {
		out.WriteString(headerPrefix)
		out.WriteString("\n")
	}
	if h := headerWrap(headers); h != "" {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(h)
		out.WriteString("\n")
	}
	for _, body := range bodies {
		if strings.TrimSpace(body) == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(body)
		out.WriteString("\n")
	}
	return out.String(), nil
}

var (
	pyImportRe = regexp.MustCompile(`^(import\s+\S|from\s+\S+\s+import\s)`)
	pyDefRe    = regexp.MustCompile(`^def\s+([A-Za-z_]\w*)\s*\(`)
	pyClassRe  = regexp.MustCompile(`^class\s+([A-Za-z_]\w*)\b`)
)

// ConcatTests folds several proven pytest/unittest files into one: imports
// hoisted and de-duplicated, colliding test names suffixed with their mutant
// id, a colliding helper refused. See TestConcatenator.
func (pyPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	return mergeParts(parts, concatSpec{
		isHeader: func(line string) bool { return pyImportRe.MatchString(line) },
		declRes:  []*regexp.Regexp{pyDefRe, pyClassRe},
		// pytest and unittest both collect on this prefix, so it is the
		// project's own definition of "a test", not corral's.
		renameOnCollision: func(name string) bool {
			return strings.HasPrefix(name, "test") || strings.HasPrefix(name, "Test")
		},
	}, "", func(headers []string) string { return strings.Join(headers, "\n") })
}

var (
	goPackageRe = regexp.MustCompile(`^package\s+(\w+)`)
	goImportRe  = regexp.MustCompile(`^import\s+(?:[\w.]+\s+)?["` + "`" + `]`)
	goFuncRe    = regexp.MustCompile(`^func\s+([A-Za-z_]\w*)\s*\(`)
	goTypeRe    = regexp.MustCompile(`^type\s+([A-Za-z_]\w*)\b`)
	goVarRe     = regexp.MustCompile(`^(?:var|const)\s+([A-Za-z_]\w*)\b`)
	// goImportSpecRe matches a line INSIDE an `import ( … )` block.
	goImportSpecRe = regexp.MustCompile(`^\s*(?:[\w.]+\s+)?["` + "`" + `]`)
)

// ConcatTests folds several proven Go test files into one: a single package
// clause, one import block, colliding Test/Benchmark/Fuzz/Example functions
// suffixed with their mutant id, a colliding helper refused.
//
// Go's grouped `import ( … )` form is flattened to its specs first, so two
// parts that spell the same import differently (one grouped, one not) still
// de-duplicate.
//
// A `//go:build` line in a part is NOT hoisted, and would end up below the
// package clause where the toolchain ignores it. Accepted: an authored test
// file corral wrote has no reason to carry a build constraint (it is written
// for the project's own default build, and every plugin's writer prompt asks
// for a plain test file), and a constraint that DID appear would fail loudly
// as an unused-import or unbuilt-file error rather than silently changing
// what ran. Hoisting one correctly means also deciding what two DIFFERENT
// constraints mean, which is a merge this function refuses on principle. The package clause is not a de-duplicated header but a
// SINGLETON: two parts that disagree about the package name are not two views
// of one file, and merging them would produce a file that belongs to neither.
func (goPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	pkg := ""
	flat := make([]AuthoredPart, 0, len(parts))
	for _, part := range parts {
		var kept []string
		inImports := false
		for _, line := range strings.Split(part.Source, "\n") {
			trimmed := strings.TrimRight(line, " \t")
			switch {
			case inImports:
				if strings.TrimSpace(trimmed) == ")" {
					inImports = false
					continue
				}
				if goImportSpecRe.MatchString(trimmed) {
					kept = append(kept, "import "+strings.TrimSpace(trimmed))
					continue
				}
				// A blank line or a comment inside the block: drop it. The
				// grouping it separated does not survive the flatten anyway.
				continue
			case strings.HasPrefix(trimmed, "import ("):
				inImports = true
				continue
			case goPackageRe.MatchString(trimmed):
				name := goPackageRe.FindStringSubmatch(trimmed)[1]
				if pkg != "" && pkg != name {
					return "", fmt.Errorf("lang: authored parts disagree about the package clause (%q vs %q)", pkg, name)
				}
				pkg = name
				continue
			default:
				kept = append(kept, trimmed)
			}
		}
		flat = append(flat, AuthoredPart{MutantID: part.MutantID, Source: strings.Join(kept, "\n")})
	}
	prefix := ""
	if pkg != "" {
		prefix = "package " + pkg
	}
	return mergeParts(flat, concatSpec{
		isHeader: func(line string) bool { return goImportRe.MatchString(line) },
		// METHODS ARE NOT MATCHED, deliberately: goFuncRe requires the name
		// immediately after `func`, so `func (s *suite) helper()` declares
		// nothing this merge can see. Two parts that both hang a method off
		// the same receiver type would therefore merge into a file that does
		// not build — but a receiver type is itself a top-level `type`
		// declaration, which goTypeRe DOES catch and refuse, so the only way
		// past this is two parts declaring methods on a type neither of them
		// declares (a method on the package's own type, from a white-box
		// test). Rare enough to accept, and the failure is a compile error in
		// the delivered file rather than a silent wrong result.
		declRes: []*regexp.Regexp{goFuncRe, goTypeRe, goVarRe},
		renameOnCollision: func(name string) bool {
			return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
				strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
		},
	}, prefix, func(headers []string) string {
		if len(headers) == 0 {
			return ""
		}
		if len(headers) == 1 {
			return headers[0]
		}
		var b strings.Builder
		b.WriteString("import (")
		for _, h := range headers {
			b.WriteString("\n\t" + strings.TrimSpace(strings.TrimPrefix(h, "import ")))
		}
		b.WriteString("\n)")
		return b.String()
	})
}

var (
	// A leading import/require line, in either of the two forms a JS/TS test
	// file actually uses. Both are hoisted and de-duplicated by EXACT text.
	jsImportRe = regexp.MustCompile(`^(import\s|(?:const|let|var)\s+[^=]+=\s*require\()`)
	// The module an import/require line names, so two lines that pull from
	// the same module with different specifiers can be caught before they are
	// silently merged into a file with a missing binding.
	jsModuleRe = regexp.MustCompile(`(?:from\s*|require\(\s*)['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]`)
	jsFuncRe   = regexp.MustCompile(`^(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`)
	jsBindRe   = regexp.MustCompile(`^(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=`)
	jsClassRe  = regexp.MustCompile(`^class\s+([A-Za-z_$][\w$]*)`)
)

// jsSpecifiersDiffer refuses two parts whose imports pull DIFFERENT specifier
// lists from the SAME module.
//
// Exact-duplicate lines are the ordinary case and de-duplicate cleanly. Two
// different lists are the trap: dropping one loses a binding the part that
// wrote it uses, and merging the braces means rewriting the model's own import
// — a rewrite whose failure mode (a missing binding) is a runtime
// ReferenceError inside a test that was PROVEN to work. Refused instead, with
// the module named, so the part is delivered separately and still runs.
func jsSpecifiersDiffer(parts []AuthoredPart) error {
	seen := map[string]string{} // module -> the first import line that named it
	for _, part := range parts {
		for _, line := range strings.Split(part.Source, "\n") {
			trimmed := strings.TrimSpace(line)
			if !jsImportRe.MatchString(trimmed) {
				continue
			}
			m := jsModuleRe.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			if first, ok := seen[m[1]]; ok && first != trimmed {
				return fmt.Errorf("lang: two authored parts import different specifiers from %q (%q vs %q); merging them would rewrite an import the model wrote", m[1], first, trimmed)
			}
			seen[m[1]] = trimmed
		}
	}
	return nil
}

// ConcatTests folds several proven node:test/vitest/jest files into one:
// imports hoisted and de-duplicated by exact text, colliding top-level
// bindings suffixed with their mutant id, two different specifier lists from
// one module refused.
//
// EVERY top-level binding is renameable here, unlike Go and Python where only
// a test is. A JS test is a `test('title', …)` CALL, not a declaration — its
// title is a string, two parts may legitimately share one, and nothing in the
// file collides on it. What does collide is the helpers, and a redeclared
// top-level `const`/`function` in ESM is a SyntaxError, not a shadow — so
// suffixing them is the merge, not a compromise.
func (jsPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	return mergeParts(parts, jsConcatSpec(), "", func(headers []string) string {
		return strings.Join(headers, "\n")
	})
}

// ConcatTests is jsPlugin's, unchanged: TypeScript's test files differ from
// JavaScript's in their type annotations, and none of them appear in an
// import line or a top-level binding NAME — the only two things this merge
// reads. A separate implementation would be a second copy of one rule.
func (tsPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	return jsPlugin{}.ConcatTests(parts)
}

func jsConcatSpec() concatSpec {
	return concatSpec{
		isHeader:          func(line string) bool { return jsImportRe.MatchString(line) },
		declRes:           []*regexp.Regexp{jsFuncRe, jsBindRe, jsClassRe},
		renameOnCollision: func(string) bool { return true },
		precheck:          jsSpecifiersDiffer,
	}
}

var (
	rubyRequireRe = regexp.MustCompile(`^require(?:_relative)?\s`)
	rubyDefRe     = regexp.MustCompile(`^def\s+([A-Za-z_][\w?!]*)`)
	// RSpec and Minitest markers, for the mixed-framework refusal below.
	rubyRSpecRe    = regexp.MustCompile(`(?m)^\s*(?:RSpec\.)?describe\s|^\s*it\s+['"]`)
	rubyMinitestRe = regexp.MustCompile(`(?m)^\s*def\s+test_|Minitest::Test`)
)

// rubyFrameworksMix refuses a set of parts that is not all one framework.
//
// A Ruby project runs ONE runner. `rspec` never collects a
// `Minitest::Test` subclass and `rake test` never runs an RSpec block, so a
// file holding both delivers a proven test that cannot execute — the exact
// failure the harness-exemplar work exists to prevent, reintroduced by the
// merge instead of by the prompt. Refused per SET rather than per part, so
// the FIRST framework seen wins the merged file and the odd part out is
// delivered on its own, where its own runner can still find it.
func rubyFrameworksMix(parts []AuthoredPart) error {
	kind := ""
	for _, part := range parts {
		k := ""
		switch {
		case rubyMinitestRe.MatchString(part.Source) && rubyRSpecRe.MatchString(part.Source):
			return fmt.Errorf("lang: authored part %s mixes test frameworks (both Minitest and RSpec constructs); one runner would skip half of it", part.MutantID)
		case rubyMinitestRe.MatchString(part.Source):
			k = "Minitest"
		case rubyRSpecRe.MatchString(part.Source):
			k = "RSpec"
		default:
			continue
		}
		if kind != "" && kind != k {
			return fmt.Errorf("lang: authored parts use different test frameworks (%s and %s); one file runs under one runner, so half the proofs would never execute", kind, k)
		}
		kind = k
	}
	return nil
}

// ConcatTests folds several proven Minitest or RSpec files into one:
// require/require_relative lines hoisted and de-duplicated, colliding
// `def test_…` names suffixed with their mutant id, a colliding non-test
// `def` refused, a mixed-framework set refused.
//
// The test-def suffixing is not tidiness — it is the whole reason Ruby needs
// this. Redefining a method in Ruby is a SILENT override: two parts that both
// write `def test_x` produce a file where the second definition wins, the
// first proof disappears, and NOTHING reports it — no error, no warning, and a
// proven_missed count that no longer matches the delivered file.
//
// A `class` clause is deliberately NOT a collision: reopening a class is
// idiomatic Ruby, and once the test names inside it are unique, two
// `class FooTest < Minitest::Test … end` blocks are one class with both tests.
// RSpec `it '…'` titles are strings, not bindings, and are left exactly as
// proven.
func (rubyPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	return mergeParts(parts, concatSpec{
		isHeader: func(line string) bool { return rubyRequireRe.MatchString(line) },
		declRes:  []*regexp.Regexp{rubyDefRe},
		// Minitest collects on this prefix, so it is the project's own
		// definition of "a test". A colliding HELPER def is refused for the
		// same reason Go's is: renaming it means rewriting call sites that
		// are the model's own code.
		renameOnCollision: func(name string) bool { return strings.HasPrefix(name, "test_") },
		// A Minitest `def test_x` lives indented inside `class FooTest`, so a
		// top-level-only scan would find no declarations at all and merge two
		// silent overrides.
		declAnyIndent: true,
		precheck:      rubyFrameworksMix,
	}, "", func(headers []string) string { return strings.Join(headers, "\n") })
}
