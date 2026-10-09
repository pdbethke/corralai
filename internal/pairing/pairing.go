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
	"path"
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
	Recognize    Shape = "recognize"     // never a candidate or a root; read by IsTest and MightBeTest only
)

// DefaultRoot is searched for every language, before any root a rule names.
const DefaultRoot = "tests"

// Rule is one convention. Name may contain {base} (the source's file name
// without its extension), replaced literally, once.
//
// Read in the other direction — by IsTest and MightBeTest, asking whether a
// path IS a test rather than where one would be — Name is a pattern over a
// file's base name: {base} matches any run of characters (including none),
// and {ext} matches a dot followed by anything. {ext} is only meaningful
// there; Candidates never substitutes it.
type Rule struct {
	Shape       Shape
	Name        string
	Dir         string
	KeepLeading bool
	MaxDepth    int
	Rank        int
}

// Candidate is one place a test may live, ranked most specific first.
//
// Rank is its EVIDENTIARY rank: how much real directory context the
// candidate's own construction encodes, NOT its position in the returned
// slice. Rank is what a cross-source ambiguity check (reposcan's
// demoteAmbiguousPairings) compares — a plain slice index would conflate "how
// specific is this match" with "how many earlier candidates happened to
// collapse onto the same string for THIS source", which are different things:
// a zero-directory-evidence match (e.g. Python's flat tests/test_foo.py) can
// arise from the mirror, stripped, OR flat form depending on how shallow the
// source is, and MUST rank identically (as the least specific of whichever
// forms produced it) no matter which one it was, or two equally-uninformative
// matches from different-depth sources would never tie and the safer "demote
// both" outcome would never fire. Dedupe enforces exactly that attribution
// when candidates collapse.
//
// Rank is comparable ACROSS languages (all start at 0 = sibling, the most
// specific a rule set can offer) but is otherwise language-defined; reposcan
// only ever compares ranks between candidates for the SAME resolved test
// path, never across different paths.
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
//
// They are the fallback for a repo whose test layout matches none of the
// Candidates derived from the source path alone (e.g. pallets/itsdangerous:
// `src/itsdangerous/signer.py` pairs with
// `tests/test_itsdangerous/test_signer.py`, one directory level deeper than
// any convention-derived mirror). The search over them (reposcan.FindTest)
// scores MATCHES by shared path context with the source, not by which root
// produced them, and never walks a directory the repository's own .gitignore
// excludes — so a root named here is never itself a way to widen the search
// into `node_modules` or `.venv`.
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
// extension (including the leading dot). Candidates starts every Shape here:
// the sibling, beside-dir, parallel-tree and flat-root forms all derive from
// this one decomposition.
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

// Generic is the language-independent test markers corral has always applied
// (reposcan.isTestFile and cmd/corral looksLikeATestPath, merged in Round B1).
// Name templates may use {ext} for "any extension". Dir-only entries are the
// directory names that make a path a POSSIBLE test (MightBeTest only).
//
// It is package data, the one place cross-language markers live, so they are
// not copied into six languages' rules. Each entry is the old substring or
// prefix check it replaces, restated as a pattern: "{base}_test{ext}" is
// "the base name contains _test." (it matches foo_test.bar.go, as the
// substring check did). The prefix marker is "test_{base}" with no {ext},
// because the check it replaces was a bare prefix: {base} already spans the
// extension of test_foo.py, and leaving {ext} off keeps the old yes for an
// extension-less test_runner script in a diff. {ext} cannot simply be made
// optional everywhere instead — "{base}.spec" would then call a PyInstaller
// coworker-server.spec a possible test, which the golden pins as no.
//
// A marker one language owns is NOT here; it is declared in that language's
// rules, where IsTest reads it for that language only: PHPUnit's
// "{base}Test.php" (php.go) and RSpec's prefix "spec_{base}.rb" (ruby.go). The
// prefix is the one marker the two old functions disagreed on — isTestFile
// applied it to every language, looksLikeATestPath to none — and the
// characterization golden fixes both answers (spec/spec_helper.rb IS a Ruby
// test; tasks/spec_runner.rake is NOT a possible test), which a Generic entry
// cannot satisfy and a Ruby rule does.
var Generic = []Rule{
	{Shape: Recognize, Name: "{base}_test{ext}"}, // Go foo_test.go, minitest foo_test.rb
	{Shape: Recognize, Name: "test_{base}"},      // Python test_foo.py, Ruby test_foo.rb
	{Shape: Recognize, Name: "{base}_spec{ext}"}, // RSpec foo_spec.rb, foo_spec.js, foo_spec.ts
	{Shape: Recognize, Name: "{base}.test{ext}"}, // foo.test.js, foo.test.ts
	{Shape: Recognize, Name: "{base}.spec{ext}"}, // foo.spec.js, foo.spec.ts
	{Shape: Recognize, Dir: "test"}, {Shape: Recognize, Dir: "tests"},
	{Shape: Recognize, Dir: "spec"}, {Shape: Recognize, Dir: "specs"},
	{Shape: Recognize, Dir: "__tests__"}, {Shape: Recognize, Dir: "testing"},
}

// IsTest reports whether rel is itself a test file under one language's
// rules: the STRICT question. Its answer removes a file from the audit, so a
// false yes silently skips auditing real code — which is why a directory
// alone never makes a file a test here (a conftest.py or a fixture under
// tests/ is test SUPPORT, accounted separately by reposcan, never a test).
//
// The name patterns are the real check, and they do NOT depend on the shape
// of the candidate list at all: a parallel-tree test like
// tests/agents/test_artifact_store.py is caught by "test_{base}" exactly
// like a sibling test_artifact_store.py would be, so widening the candidates
// from one path to an ordered list changes nothing here. The patterns are
// every Name in rules (so PHPUnit's separator-less suffix, tests/CalcTest.php,
// is recognized from the PHP plugin's own "{base}Test.php" — the plugin's
// candidates cannot see it, since for a test file they propose
// tests/CalcTestTest.php, and before the marker existed every PHP test was
// counted as an unpaired source) plus every Name in Generic. Matching is
// case-sensitive: src/Latest.php is not a PHPUnit test.
//
// The fixed-point check (does rel appear in ITS OWN candidate list) is a
// cheap belt-and-braces for a rule set that is someday idempotent on an
// already-test path — no current one is (`foo_test.go`'s own conventions
// produce `foo_test_test.go`, `test_test_foo.py`, etc, never `foo_test.go`
// itself), so it never fires today either.
func IsTest(rules []Rule, rel string) bool {
	for _, c := range Candidates(rules, rel) {
		if filepath.ToSlash(c.Path) == rel {
			return true
		}
	}
	base := filepath.Base(rel)
	return nameMatches(rules, base, false) || nameMatches(Generic, base, false)
}

// MightBeTest is the cheap, language-independent question the diff bound asks
// before any evidence exists: could this changed file be a test? It is the
// GENEROUS question over the same data IsTest reads — a false "yes" only costs
// one instrumented run, while a false "no" is the false-green this exists to
// prevent.
//
// So it says yes when IsTest says yes for any language in all, when any
// directory segment of rel is a Dir some rule (or Generic) names, or when rel's
// LOWERCASED base name matches any lowercased Name pattern — CalcTest.php,
// calctest.php and Test_Foo.py all might be tests. It takes every language's
// rules rather than one because a changed file is asked about before anyone
// knows which language claims it (README.md and a fixture under specs/ are
// asked too).
func MightBeTest(all [][]Rule, rel string) bool {
	rel = filepath.ToSlash(rel)
	sets := append([][]Rule{Generic}, all...)
	for _, rules := range all {
		if IsTest(rules, rel) {
			return true
		}
	}
	dirs := map[string]bool{}
	for _, rules := range sets {
		for _, r := range rules {
			if d := strings.Trim(filepath.ToSlash(r.Dir), "/"); d != "" {
				dirs[d] = true
			}
		}
	}
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		if dirs[seg] {
			return true
		}
	}
	base := strings.ToLower(path.Base(rel))
	for _, rules := range sets {
		if nameMatches(rules, base, true) {
			return true
		}
	}
	return false
}

// nameMatches reports whether base matches any rule's Name pattern ({base} =
// any characters, {ext} = a dot then any characters). With fold, the pattern
// is lowercased first; the caller lowercases base.
func nameMatches(rules []Rule, base string, fold bool) bool {
	for _, r := range rules {
		if r.Name == "" {
			continue
		}
		name := r.Name
		if fold {
			name = strings.ToLower(name)
		}
		if ok, err := path.Match(namePattern(name), base); err == nil && ok {
			return true
		}
	}
	return false
}

// namePattern turns a Name template into a path.Match pattern: every literal
// character that path.Match treats specially is escaped, {base} becomes "*"
// and {ext} becomes ".*". A base name never contains "/", so "*" spanning
// any run of characters is exactly the substring/prefix checks Generic
// replaced.
func namePattern(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); {
		switch {
		case strings.HasPrefix(name[i:], "{base}"):
			b.WriteString("*")
			i += len("{base}")
		case strings.HasPrefix(name[i:], "{ext}"):
			b.WriteString(".*")
			i += len("{ext}")
		default:
			if strings.ContainsRune(`*?[]\`, rune(name[i])) {
				b.WriteByte('\\')
			}
			b.WriteByte(name[i])
			i++
		}
	}
	return b.String()
}
