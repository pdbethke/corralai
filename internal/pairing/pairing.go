// SPDX-License-Identifier: Elastic-2.0

// Package pairing is the one place corral decides which file tests which.
//
// It used to be six hand-written TestPaths (one per language plugin), three
// TestRoots restating their directories, and two is-test inverses
// (reposcan.isTestFile, cmd/corral looksLikeATestPath) that had already drifted
// apart. A language now DECLARES its conventions as []Rule — data, not code —
// and everything about pairing is derived from that one declaration
// (DRY audit 2026-10-08, Round B1). A new language adds rules, or a new Shape
// here; it never adds a pairing function.
package pairing

import (
	"path/filepath"
	"strings"
)

// Shape is where a rule's test file sits relative to its source.
type Shape string

const (
	Sibling      Shape = "sibling"       // <dir>/<Name>
	BesideDir    Shape = "beside-dir"    // <dir>/<Dir>/<Name>; Dir is also a search root
	ParallelTree Shape = "parallel-tree" // <Dir>/<sub>/<Name> (or <Dir>/<dir>/<Name> with KeepLeading)
	FlatRoot     Shape = "flat-root"     // <Dir>/<Name>, only within MaxDepth
	Recognize    Shape = "recognize"     // never a candidate or a root; read by IsTest only
)

// DefaultRoot is searched for every language, before any root a rule names.
const DefaultRoot = "tests"

// Rule is one convention. Name may contain {base} (the source's file name
// without its extension), replaced literally, once.
type Rule struct {
	Shape       Shape
	Name        string
	Dir         string
	KeepLeading bool
	MaxDepth    int
	Rank        int
}

// Candidate is one place a test may live, ranked most specific first.
type Candidate struct {
	Path  string
	Rank  int
	Shape Shape
}

// Candidates derives codePath's test candidates from rules, in rule order,
// deduplicated by Dedupe's rule (see dedupe.go for why rank merges the way it
// does).
func Candidates(rules []Rule, codePath string) []Candidate {
	dir, base, _ := splitPath(codePath)
	var out []Candidate
	for _, r := range rules {
		name := strings.Replace(r.Name, "{base}", base, 1)
		var p string
		switch r.Shape {
		case Sibling:
			p = joinDir(dir, name)
		case BesideDir:
			p = filepath.Join(dir, r.Dir, name)
		case ParallelTree:
			sub := stripFirstSegment(dir)
			if r.KeepLeading {
				sub = dir
			}
			p = filepath.Join(r.Dir, sub, name)
		case FlatRoot:
			if dirDepth(dir) > r.MaxDepth {
				continue
			}
			p = filepath.Join(r.Dir, name)
		default:
			continue
		}
		out = append(out, Candidate{Path: p, Rank: r.Rank, Shape: r.Shape})
	}
	return Dedupe(out)
}

// Roots is the top-level directories a search walks for these rules:
// DefaultRoot, then every Dir a BesideDir/ParallelTree/FlatRoot rule names,
// trimmed of slashes, deduplicated, in order. (The six languages' TestRoots
// restated exactly these directories; deriving them removes the restatement.)
func Roots(rules []Rule) []string {
	out := []string{DefaultRoot}
	seen := map[string]bool{DefaultRoot: true}
	for _, r := range rules {
		switch r.Shape {
		case BesideDir, ParallelTree, FlatRoot:
		default:
			continue
		}
		d := strings.Trim(filepath.ToSlash(r.Dir), "/")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// splitPath decomposes codePath into its directory (empty string for a
// top-level file — never "."), its base name WITHOUT extension, and its
// extension (including the leading dot). Shared by every non-Go plugin's
// TestPaths, since the parallel-tree and sibling forms all start here.
func splitPath(codePath string) (dir, base, ext string) {
	ext = filepath.Ext(codePath)
	b := filepath.Base(codePath)
	base = strings.TrimSuffix(b, ext)
	dir = filepath.Dir(codePath)
	if dir == "." {
		dir = ""
	}
	return dir, base, ext
}

// joinDir joins dir and name, treating an empty dir as "no directory"
// (plain name) rather than filepath.Join's "./name".
func joinDir(dir, name string) string {
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

// stripFirstSegment removes the leading path component of a dir, e.g.
// "aisuite/agents" -> "agents", "agents" -> "", "" -> "". This is how a
// source file under one top-level directory (a package name, or a `src/`
// layout) maps onto a parallel test tree: real-world convention REPLACES the
// leading directory rather than nesting the whole original path under
// `tests/` (`aisuite/agents/artifact_store.py` pairs with
// `tests/agents/test_artifact_store.py`, not
// `tests/aisuite/agents/test_artifact_store.py`).
func stripFirstSegment(dir string) string {
	if dir == "" {
		return ""
	}
	parts := strings.SplitN(filepath.ToSlash(dir), "/", 2)
	if len(parts) == 2 {
		return filepath.FromSlash(parts[1])
	}
	return ""
}

// dirDepth returns the number of path segments in dir (0 for a top-level
// file). Used to bound how far a "no directory context at all" flat-tree
// test candidate is allowed to reach: a flat candidate is a plausible
// convention for a shallow source but a collision magnet for a deep one
// (e.g. `examples/javascript/js_example/views.py`, 3 segments deep, would
// otherwise generate the same flat candidate as a genuine top-level
// `src/flask/views.py`).
func dirDepth(dir string) int {
	if dir == "" {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(dir), "/"))
}
