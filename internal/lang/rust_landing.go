// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// rustLandedModule is the module name corral's authored tests are kept in,
// apart from the project's own (which, in Rust, is almost always `tests`):
// a reviewer can see which tests corral wrote, and nothing corral writes can
// collide with a test the project already has.
const rustLandedModule = "corral_tests"

var (
	// rustModDeclRe finds a module declaration naming corral's module or a
	// numbered sibling of it, anywhere in a file.
	rustModDeclRe = regexp.MustCompile(`(?m)^\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+(` + rustLandedModule + `(?:_\d+)?)\b`)
	// rustWrapperOpenRe is a writer's own wrapper module's opening line.
	rustWrapperOpenRe = regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?mod\s+[A-Za-z_]\w*\s*\{\s*$`)
	// rustAncestorGlobRe is a glob import of an ancestor module — the line
	// the landing itself supplies, at the depth the landing knows.
	rustAncestorGlobRe = regexp.MustCompile(`^\s*use\s+(?:super::)+\*\s*;\s*$`)
	// rustLiterals is Rust's string and comment syntax for the brace scan
	// and the merge's renames. The single quote is NOT a quote here: it
	// opens a lifetime ('a) as often as a char literal, and a char literal
	// holding a brace is the only thing that misses.
	rustLiterals = literalSyntax{quotes: `"`, lineComments: []string{"//"}, blockComment: [2]string{"/*", "*/"}}
)

// LandTest wraps body as `#[cfg(test)] mod corral_tests { use <ancestor>::*;
// … }`, the block appended to target. The use line is DATA from the landing:
// depth 1 (a file the source declares as its child module) reaches the code
// under test with super::super, depth 0 (the source itself) with super — the
// same scope the developer's own tests module sees, so private items are in
// reach either way.
//
// Two things the writer may have done are folded rather than nested, because
// this runs BEFORE the test is compiled and proven, so whatever it produces
// is what the proof is about:
//   - a body that is wholly one wrapper module (`#[cfg(test)] mod x { … }`,
//     what the plugin's prompt used to ask for) is unwrapped, and its lines
//     de-indented by their common indentation;
//   - a leading ancestor glob (`use super::*;`) is dropped — the landing's
//     own line is the one that is right for this file.
//
// The block is not indented: Rust does not care, and indenting would change
// any string literal that continues across lines. The module is named
// corral_tests, or corral_tests_N when target already declares that name, so
// appending never redeclares a module.
func (rustPlugin) LandTest(body string, depth int, target string) string {
	if depth < 0 {
		depth = 0
	}
	name := rustFreeModuleName(target)
	inner := strings.Trim(rustDropAncestorGlobs(rustUnwrapWholeModule(body)), "\n")
	var b strings.Builder
	b.WriteString("#[cfg(test)]\nmod " + name + " {\n")
	b.WriteString("use " + strings.Repeat("super::", depth+1) + "*;\n")
	if inner != "" {
		b.WriteString("\n" + inner + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// ReachCanaryTest is a libtest test that panics, so it fails whenever it is
// run, and only then: it compiles in any crate (no imports, no items of the
// code under test), so a failure means the command RAN the landed block, not
// that the block broke the build. See TestLander.ReachCanaryTest.
func (rustPlugin) ReachCanaryTest() string {
	return "#[test]\nfn corral_reach_canary() {\n    panic!(\"corral positive control: this test must fail when it runs\");\n}"
}

// InlineTestIDs is the libtest ids of landed's tests once the block is
// appended to codePath: the module path the library gives codePath, the
// block's own module and each test, joined with "::" (`inner::extract::
// corral_tests::kills`). They are exact, which is the point: libtest's name
// filter without --exact matches a substring, so the module path alone
// (`corral_tests::`) would also select a corral_tests module another file
// of the crate already holds, and the authored pass would not be alone.
//
// The module path is read from the file's place, as cargo's default module
// layout gives it (src/lib.rs is the crate root; src/a/b.rs and
// src/a/b/mod.rs are a::b), for a file the library compiles
// (rustUnitTarget's lib case, the same place the authored pass's unit is
// named from). A file mounted elsewhere by a `#[path]` attribute has a
// different path; its ids then select nothing, the reach canary is not run
// either, and the positive control reports the authored test unreached
// rather than crediting it. A file outside the library (a binary's), a
// name that is no Rust identifier, or a block whose tests cannot be listed
// (TestNamesInSource) has no ids.
func (rustPlugin) InlineTestIDs(repoRoot, codePath, landed string) ([]string, error) {
	mod, err := rustLibModulePath(repoRoot, codePath)
	if err != nil {
		return nil, err
	}
	names := rustPlugin{}.TestNamesInSource("", landed)
	if len(names) == 0 {
		return nil, fmt.Errorf("lang: no test can be named in the block landed in %s, so its tests cannot be run apart from the file's own", codePath)
	}
	ids := make([]string, len(names))
	for i, n := range names {
		ids[i] = rustJoin(mod, n)
	}
	return ids, nil
}

// rustLibModulePath is the module path the library gives codePath, from its
// place under the package's src/ (see InlineTestIDs).
func rustLibModulePath(repoRoot, codePath string) (string, error) {
	f, ok := repoRelative(codePath)
	_, kind, _, unit := rustUnitTarget(repoRoot, codePath)
	if !ok || !unit || kind != "lib" {
		return "", fmt.Errorf("lang: %s is not a file the library of its Cargo package compiles, so no module path names it", codePath)
	}
	_, dir, err := owningCrateDir(repoRoot, f)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(filepath.Join(dir, "src"), filepath.Join(root, filepath.FromSlash(f)))
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	last := len(parts) - 1
	switch {
	case len(parts) == 1 && parts[0] == "lib.rs":
		return "", nil
	case parts[last] == "mod.rs":
		parts = parts[:last]
	default:
		parts[last] = strings.TrimSuffix(parts[last], ".rs")
	}
	for _, p := range parts {
		if !rustIdentifier(p) {
			return "", fmt.Errorf("lang: %s is not a module name, so no module path names %s", p, codePath)
		}
	}
	return strings.Join(parts, "::"), nil
}

// rustIdentifier reports whether s is a Rust identifier as a module file's
// name can spell one: identifier bytes throughout, not starting with a digit.
func rustIdentifier(s string) bool {
	if !isIdent(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !identByte(s[i]) {
			return false
		}
	}
	return true
}

// rustFreeModuleName is corral_tests, or the first corral_tests_N (N ≥ 2)
// target does not already declare.
func rustFreeModuleName(target string) string {
	taken := map[string]bool{}
	for _, m := range rustModDeclRe.FindAllStringSubmatch(target, -1) {
		taken[m[1]] = true
	}
	if !taken[rustLandedModule] {
		return rustLandedModule
	}
	for n := 2; ; n++ {
		if c := rustLandedModule + "_" + strconv.Itoa(n); !taken[c] {
			return c
		}
	}
}

// rustUnwrapWholeModule returns the inside of body when body is exactly one
// module item (optionally preceded by attributes), de-indented by its common
// indentation; otherwise body unchanged. "Exactly one" is decided by the
// braces outside strings and comments: the module's own brace must close at
// the end of the body, so a helper module beside a test is never unwrapped.
func rustUnwrapWholeModule(body string) string {
	lines := strings.Split(strings.Trim(body, "\n"), "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#[") {
		i++
	}
	if i >= len(lines) || !rustWrapperOpenRe.MatchString(strings.TrimSpace(lines[i])) {
		return body
	}
	rest := strings.Join(lines[i:], "\n")
	open := strings.IndexByte(rest, '{')
	if end := rustMatchingBrace(rest, open); end < 0 || strings.TrimSpace(rest[end+1:]) != "" {
		return body
	}
	inner := lines[i+1 : len(lines)-1]
	if strings.TrimSpace(lines[len(lines)-1]) != "}" {
		return body
	}
	return dedent(inner)
}

// rustMatchingBrace is the index of the brace closing the one at open in s,
// skipping strings and comments; -1 when it never closes.
func rustMatchingBrace(s string, open int) int {
	masked := maskedSpans(s, rustLiterals)
	inMask := func(i int) bool {
		for _, sp := range masked {
			if i >= sp[0] && i < sp[1] {
				return true
			}
		}
		return false
	}
	depth := 0
	for i := open; i < len(s); i++ {
		if inMask(i) {
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// dedent removes the indentation every non-blank line shares — and nothing
// else: when they share none (a string literal continuing at column 0, say)
// every line is left exactly as written, so nesting and literal text are
// never flattened. A blank line keeps nothing but its newline.
func dedent(lines []string) string {
	common := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			out[i] = ""
			continue
		}
		out[i] = l[common:]
	}
	return strings.Join(out, "\n")
}

// rustDropAncestorGlobs drops the ancestor glob imports in body's LEADING
// block of use lines (blank lines and comments may sit between them). A
// nested module's own `use super::*` is past that block and is kept.
func rustDropAncestorGlobs(body string) string {
	lines := strings.Split(body, "\n")
	out := lines[:0:0]
	leading := true
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if leading {
			if rustAncestorGlobRe.MatchString(l) {
				continue
			}
			if t != "" && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "use ") {
				leading = false
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

var (
	rustUseRe   = regexp.MustCompile(`^use\s.*;$`)
	rustFnRe    = regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+"[^"]*"\s+)?fn\s+([A-Za-z_]\w*)`)
	rustItemRe  = regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|type|union|mod|const|static(?:\s+mut)?)\s+([A-Za-z_]\w*)`)
	rustMacroRe = regexp.MustCompile(`^macro_rules!\s*([A-Za-z_]\w*)`)
)

// rustLandedHeader splits a block LandTest produced into its three header
// lines and its body. ok=false for anything else: a part this merge cannot
// read as corral's own block is refused, never re-wrapped by guess.
func rustLandedHeader(part string) (header, body string, ok bool) {
	lines := strings.Split(strings.TrimRight(part, "\n"), "\n")
	if len(lines) < 4 || lines[0] != "#[cfg(test)]" || !rustModDeclRe.MatchString(lines[1]) ||
		!strings.HasSuffix(lines[1], " {") || !rustAncestorGlobRe.MatchString(lines[2]) || lines[len(lines)-1] != "}" {
		return "", "", false
	}
	return strings.Join(lines[:3], "\n"), strings.Join(lines[3:len(lines)-1], "\n"), true
}

// ConcatTests folds several proven landed blocks — each one LandTest wrapped
// for the same landing — into one: the header (cfg, module, use line) once,
// the bodies' `use` lines hoisted and de-duplicated, a colliding test whose
// name starts with `test` suffixed with its mutant id, any other colliding
// name refused (it is the model's own helper, and renaming call sites is a
// rewrite this merge does not make — see TestConcatenator). Parts landed under
// different headers belong to different landings and are refused, as is a
// part that is not a landed block.
//
// Nothing here changes a body's text beyond the renames: the parts were
// proven as written.
func (rustPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	header := ""
	bodies := make([]AuthoredPart, 0, len(parts))
	for _, part := range parts {
		h, body, ok := rustLandedHeader(part.Source)
		if !ok {
			return "", fmt.Errorf("lang: authored part %s is not a landed corral_tests module, so it cannot be merged into one", part.MutantID)
		}
		if header != "" && h != header {
			return "", fmt.Errorf("lang: authored part %s was landed under a different module header (%q vs %q) — a different landing, not the same file", part.MutantID, h, header)
		}
		header = h
		bodies = append(bodies, AuthoredPart{MutantID: part.MutantID, Source: body})
	}
	merged, err := mergeParts(bodies, concatSpec{
		isHeader: func(line string) bool { return rustUseRe.MatchString(line) },
		declRes:  []*regexp.Regexp{rustFnRe, rustItemRe, rustMacroRe},
		// libtest runs any #[test] fn whatever its name, so there is no
		// project-defined test prefix to read; `test` is the one a writer
		// reaches for, and anything else colliding is refused rather than
		// renamed.
		renameOnCollision: func(name string) bool { return strings.HasPrefix(name, "test") },
		literals:          rustLiterals,
	}, "", func(h []string) string { return strings.Join(h, "\n") })
	if err != nil {
		return "", err
	}
	return header + "\n\n" + strings.TrimRight(merged, "\n") + "\n}\n", nil
}
