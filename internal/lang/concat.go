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
		return "", append([]AuthoredPart(nil), parts...)
	}
	var kept []AuthoredPart
	for _, part := range parts {
		candidate := append(append([]AuthoredPart(nil), kept...), part)
		out, err := c.ConcatTests(candidate)
		if err != nil {
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
	// declRes are the regexps that name a top-level declaration; each must
	// have exactly one capture group holding the name.
	declRes []*regexp.Regexp
	// isTestName reports whether a declared name is a TEST (renameable on
	// collision) rather than a helper (which is refused).
	isTestName func(name string) bool
}

// mergeParts is the shared body of every ConcatTests implementation: hoist and
// de-duplicate headers, rename colliding test declarations, refuse colliding
// helpers, and join what is left.
//
// headerPrefix is emitted verbatim before the hoisted headers (Go's package
// clause; empty for Python) and headerWrap renders the de-duplicated header
// lines (Go folds them into one import block; Python emits them as they were).
func mergeParts(parts []AuthoredPart, spec concatSpec, headerPrefix string, headerWrap func([]string) string) (string, error) {
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
			if isTopLevel {
				for _, re := range spec.declRes {
					if m := re.FindStringSubmatch(trimmed); m != nil {
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
		if !spec.isTestName(name) {
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
		isTestName: func(name string) bool {
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
// de-duplicate. The package clause is not a de-duplicated header but a
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
		declRes:  []*regexp.Regexp{goFuncRe, goTypeRe, goVarRe},
		isTestName: func(name string) bool {
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
