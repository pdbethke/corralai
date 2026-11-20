// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"strings"
)

// The static reading of a SwiftPM manifest that the lockfile rule and the
// branch-pin disclosure share: the `.package(...)` dependency declarations in
// Package.swift, read from code blanked by swiftCode, so a comment or a
// string literal never yields a declaration.

// swiftPackageCall is the start of a dependency declaration in blanked
// manifest code: `.package(`, an implicit member of Package.Dependency, or
// the same member named in full (Package.Dependency.package(...)).
var swiftPackageCall = regexp.MustCompile(`\.package\s*\(`)

// swiftDependency is one `.package(...)` call: the labels of its own
// arguments (not those of a call nested in it), whether it carries the older
// `.branch("...")` requirement, and its `url:` when that is a plain string
// literal.
type swiftDependency struct {
	labels map[string]bool
	branch bool
	url    string
}

// swiftDependencies reads every `.package(...)` call in a manifest. ok is
// false when the manifest cannot be blanked (it ends inside a comment or a
// literal), which a caller must not read as "no dependencies".
func swiftDependencies(src string) (deps []swiftDependency, ok bool) {
	code, ok := swiftCode(src)
	if !ok {
		return nil, false
	}
	for _, loc := range swiftPackageCall.FindAllStringIndex(code, -1) {
		if !swiftDependencyMember(code, loc[0]) {
			continue
		}
		d := swiftDependency{labels: map[string]bool{}}
		depth := 0
		for i := loc[1] - 1; i < len(code); i++ {
			switch c := code[i]; {
			case c == '(' || c == '[' || c == '{':
				depth++
			case c == ')' || c == ']' || c == '}':
				depth--
			case depth == 1 && identByte(c) && !identByte(code[i-1]):
				j := i
				for j < len(code) && identByte(code[j]) {
					j++
				}
				word, after := code[i:j], skipSpaces(code, j)
				next := byte(0)
				if after < len(code) {
					next = code[after]
				}
				switch {
				case code[i-1] != '.' && next == ':':
					d.labels[word] = true
					if word == "url" && d.url == "" {
						d.url = swiftPlainLiteral(src, skipSpaces(src, after+1)) // the literal is blanked in code
					}
				case code[i-1] == '.' && word == "branch" && next == '(':
					d.branch = true
				}
				i = j - 1
			}
			if depth == 0 {
				break
			}
		}
		deps = append(deps, d)
	}
	return deps, true
}

// swiftDependencyMember reports whether the `.package(` at dot is a
// Package.Dependency member: written bare (`.package(`, the implicit member
// form) or after the type's own name (`Package.Dependency.package(`). After
// any other name it is a member of something else.
func swiftDependencyMember(code string, dot int) bool {
	if dot == 0 || !identByte(code[dot-1]) {
		return true
	}
	start := dot
	for start > 0 && identByte(code[start-1]) {
		start--
	}
	return code[start:dot] == "Dependency"
}

// swiftManifestPins reports whether a manifest declares a dependency a
// Package.resolved would pin: any `.package(...)` that is not a local
// `path:` one (a url, a registry id, or a form this reading does not know).
// A manifest that cannot be read counts as one that does, so the lockfile
// rule errs toward one tree, never toward trees that could disagree.
func swiftManifestPins(src string) bool {
	deps, ok := swiftDependencies(src)
	if !ok {
		return true
	}
	for _, d := range deps {
		if !d.labels["path"] {
			return true
		}
	}
	return false
}

// swiftBranchPins names the dependencies a manifest pins to a branch: the
// `branch:` label among a `.package(...)` call's own arguments, or the older
// `.branch("...")` requirement. Each is named by its `url:` literal, or "a
// dependency" when the url is not a plain string literal. nil when the
// manifest cannot be read.
func swiftBranchPins(src string) []string {
	deps, _ := swiftDependencies(src)
	var pins []string
	for _, d := range deps {
		if !d.labels["branch"] && !d.branch {
			continue
		}
		if d.url == "" {
			pins = append(pins, "a dependency")
		} else {
			pins = append(pins, d.url)
		}
	}
	return pins
}

// swiftPlainLiteral is the string literal that opens at src[i] when it is a
// plain one: a double quote, no escape or interpolation, closed on the same
// line. "" otherwise.
func swiftPlainLiteral(src string, i int) string {
	if i >= len(src) || src[i] != '"' {
		return ""
	}
	end := strings.IndexAny(src[i+1:], "\"\\\n")
	if end < 0 || src[i+1+end] != '"' {
		return ""
	}
	return src[i+1 : i+1+end]
}
