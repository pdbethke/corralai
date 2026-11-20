// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path"
	"path/filepath"
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
// `.branch("...")` requirement, and its `url:` and `path:` when each is a
// plain string literal.
type swiftDependency struct {
	labels map[string]bool
	branch bool
	url    string
	path   string
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
					switch lit := swiftPlainLiteral(src, skipSpaces(src, after+1)); { // the literal is blanked in code
					case word == "url" && d.url == "":
						d.url = lit
					case word == "path" && d.path == "":
						d.path = lit
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

// swiftManifestPins reports whether the package whose Package.swift is in
// pkgDir declares a dependency a Package.resolved would pin: any
// `.package(...)` that is not a local `path:` one (a url, a registry id, or a
// form this reading does not know), here or in a local package it names,
// followed through path: dependencies, because SwiftPM records a local
// package's remote dependencies in the root package's Package.resolved.
//
// Every answer this reading cannot establish counts as pinning, so the
// lockfile rule errs toward one tree, never toward trees that could
// disagree: a manifest that cannot be read or blanked, a path that is not a
// plain string literal, one that is absolute or leaves pkgDir (the reads go
// through an os.Root on pkgDir, which refuses such a path, symlinks
// included), and a cycle. A local package named twice (two packages sharing
// a third) is read once.
func swiftManifestPins(pkgDir string) bool {
	root, err := os.OpenRoot(pkgDir)
	if err != nil {
		return true
	}
	defer func() { _ = root.Close() }()
	return swiftPinsIn(root, ".", map[string]bool{})
}

// swiftPinsIn is swiftManifestPins for the package at dir, a slash path
// inside root. state holds each package read: false while it is being read
// (meeting it again is a cycle), true once it was found to pin nothing.
func swiftPinsIn(root *os.Root, dir string, state map[string]bool) bool {
	if done, seen := state[dir]; seen {
		return !done
	}
	state[dir] = false
	src, err := root.ReadFile(path.Join(dir, "Package.swift"))
	if err != nil {
		return true
	}
	deps, ok := swiftDependencies(string(src))
	if !ok {
		return true
	}
	for _, d := range deps {
		if !d.labels["path"] || d.path == "" || filepath.IsAbs(d.path) {
			return true
		}
		child := path.Clean(path.Join(dir, filepath.ToSlash(d.path)))
		if child == ".." || strings.HasPrefix(child, "../") || swiftPinsIn(root, child, state) {
			return true
		}
	}
	state[dir] = true
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
