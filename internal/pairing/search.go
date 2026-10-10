// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"path/filepath"
	"strings"
)

// SearchRank is the Rank a via-search pairing carries into
// demoteAmbiguousPairings: strictly less specific (a higher number) than any
// rank a shipped plugin's own TestRules ever hand out (today's max is 3,
// Python's flat form) — a fuzzy recursive match is weaker evidence than ANY
// convention-list hit, by construction, so it must never out-rank one in a
// cross-source collision.
const SearchRank = 100

// testDirAffixes: the prefixes and suffixes a test DIRECTORY may add to the
// source directory it mirrors (itsdangerous → test_itsdangerous). A directory
// convention, deliberately not derived from Generic's file-name markers — see
// the B1b plan's decision record. Unexported on purpose: nothing outside
// pairing reads it, and an exported mutable var would let any importer change
// what every search normalizes.
var testDirAffixes = struct{ Prefixes, Suffixes []string }{
	Prefixes: []string{"test_", "spec_"},
	Suffixes: []string{"_test", "_spec"},
}

// Sources is what a caller knows about the repository; Resolve knows nothing
// else. Exists answers stage 1 for a root-relative slash path. Universe lists
// the stage-2 search space (root-relative, slash-separated, already filtered
// to what the caller considers visible) and is called at most once, and only
// when stage 1 misses — a repository where listing fails still pairs by
// convention (Review Focus 2).
type Sources struct {
	Exists   func(rel string) bool
	Universe func() ([]string, error)
}

// Resolution is Resolve's whole answer: what it tried, where it looked, and
// what (if anything) it found. reposcan exposes it as SearchResult (a type
// alias, not a copy — a field-by-field converter once sat between the two and
// had already dropped Rank, the bug shape AGENTS.md names).
type Resolution struct {
	// Path is the resolved test path, root-relative and slash-separated.
	// "" when nothing was found.
	Path string
	// Rank is the winning candidate's own Rank for a convention hit, and
	// SearchRank for a search hit — see SearchRank for why the latter must
	// never compete with a real convention rank.
	Rank int
	// Shape is the Shape of the rule whose candidate paired the file on a
	// convention hit; "" on a search hit or a miss, because no rule paired
	// it. The Rust plan needs this label on the scan line ("paired by
	// parallel-tree"), so it is carried here rather than re-derived later.
	Shape Shape
	// ViaSearch is true when Path came from the stage-2 search rather than
	// the ordered candidates — the fact the "paired by search" disclosure is
	// built on.
	ViaSearch bool
	// Tried is every candidate checked for existence, in order — named in a
	// not-found error so the operator sees exactly what was already ruled
	// out, not just that something failed. Populated even on a miss.
	Tried []string
	// Roots is the set of conventional test directories the search walked
	// (populated only when stage 2 ran, i.e. every Tried candidate was
	// absent) — so a caller can say everywhere it looked.
	Roots []string
	// Found reports whether Path is usable. Path == "" and Found == false
	// coincide today, but Found makes the zero value unambiguous rather than
	// relying on a caller to remember that Path == "" means "not found".
	Found bool
}

// Resolve finds the test file for codePath in two strictly ordered stages,
// written once for every caller (reposcan's FindTest over a fresh git
// listing, and its findInUniverse over Enumerate's already-walked universe —
// each of which used to carry its own copy of this logic):
//
//  1. Candidates(rules, codePath), IN ORDER — the first one src.Exists
//     accepts wins, with that candidate's Rank and Shape. A conventionally-mirrored
//     file is paired here and NEVER reaches stage 2, so src.Universe is never
//     called for it.
//  2. Only when NONE of those candidates exists: a search of src.Universe()
//     under Roots(rules) for one of the SAME basenames the candidates already
//     try (searchBasenames), picking the most specific match
//     (bestSearchMatch). A hit carries SearchRank and ViaSearch.
//
// The error is src.Universe's, and only ever surfaces after a stage-1 miss.
func Resolve(rules []Rule, codePath string, src Sources) (Resolution, error) {
	cands := Candidates(rules, codePath)

	var res Resolution
	for _, c := range cands {
		cp := filepath.ToSlash(c.Path)
		if cp == "" {
			continue
		}
		res.Tried = append(res.Tried, cp)
		if src.Exists(cp) {
			res.Path = cp
			res.Rank = c.Rank
			res.Shape = c.Shape
			res.Found = true
			return res, nil
		}
	}

	res.Roots = Roots(rules)
	basenames := searchBasenames(cands)
	if len(basenames) == 0 {
		return res, nil
	}
	universe, err := src.Universe()
	if err != nil {
		return res, err
	}
	if found, ok := bestSearchMatch(codePath, res.Roots, basenames, universe); ok {
		res.Path = found
		res.Rank = SearchRank
		res.ViaSearch = true
		res.Found = true
	}
	return res, nil
}

// searchBasenames collects the distinct basenames p's own pairing
// candidates use — "test the conventional NAME, search a WIDER set of
// directories for it" rather than inventing a second, independently
// maintained naming rule that could drift from the declared rules. For
// itsdangerous this is exactly {"test_signer.py", "signer_test.py"}: the same
// two names pyPlugin's sibling rules already produce, just no longer confined
// to the directories the rules themselves derive.
func searchBasenames(cands []Candidate) map[string]bool {
	out := map[string]bool{}
	for _, c := range cands {
		if c.Path == "" {
			continue
		}
		out[filepath.Base(filepath.ToSlash(c.Path))] = true
	}
	return out
}

// UnderRoots reports whether rel (a root-relative, slash-separated path) sits
// under one of roots — checked as a TOP-LEVEL first path component, matching
// "tests/**" style conventions rather than a substring match that could fire
// on an unrelated directory that merely contains the word "test".
func UnderRoots(rel string, roots []string) bool {
	first, _, ok := strings.Cut(rel, "/")
	if !ok {
		return false
	}
	for _, r := range roots {
		if first == r {
			return true
		}
	}
	return false
}

// pathStemScore counts how many trailing NORMALIZED path segments a and b
// share, comparing from the end of each — the evidence bestSearchMatch
// requires before it will trust a basename-only hit at all (see there for
// why zero is not enough), and, among matches that clear that bar, the
// "most-specific match wins" tie breaker: a match whose own directory
// echoes more of codePath's directory is better evidence than one that
// shares less of it.
func pathStemScore(a, b []string) int {
	n := 0
	for i, j := len(a)-1, len(b)-1; i >= 0 && j >= 0; i, j = i-1, j-1 {
		if a[i] != b[j] {
			break
		}
		n++
	}
	return n
}

// segments splits a slash-separated directory path into its components; "."
// (filepath.Dir's answer for a top-level file) and "" both mean "no
// directory" and yield an empty slice, never a single "." element that would
// never legitimately match anything.
func segments(dir string) []string {
	dir = filepath.ToSlash(dir)
	if dir == "" || dir == "." {
		return nil
	}
	return strings.Split(dir, "/")
}

// normalizeSeg strips ONE conventional test-directory affix from a single
// path segment — a leading test_/spec_ or a trailing _test/_spec — so a
// directory named to mirror its source but ALSO carrying the language's own
// test-marker prefix (pallets/itsdangerous: the source package is
// `itsdangerous`, its test directory is `test_itsdangerous`, not
// `itsdangerous`) still compares equal to the source's own directory name.
// It is deliberately a single strip, not a loop: "stripping until nothing
// changes" would also erase a segment that is GENUINELY named e.g. "spec"
// on its own, which normalizeSeg must leave alone.
func normalizeSeg(s string) string {
	for _, pre := range testDirAffixes.Prefixes {
		if strings.HasPrefix(s, pre) && s != pre {
			return s[len(pre):]
		}
	}
	for _, suf := range testDirAffixes.Suffixes {
		if strings.HasSuffix(s, suf) && s != suf {
			return s[:len(s)-len(suf)]
		}
	}
	return s
}

// normalizeSegs applies normalizeSeg across a whole path-segment slice.
func normalizeSegs(segs []string) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = normalizeSeg(s)
	}
	return out
}

// bestSearchMatch scans universe (root-relative, slash-separated candidate
// paths — already filtered to whatever this caller considers "visible",
// e.g. NEVER a gitignored file) for the file that best pairs with codePath:
// it must sit under one of roots, carry one of basenames, AND share at
// least one NORMALIZED trailing directory segment with codePath's own
// directory (pathStemScore > 0) — a basename match with ZERO directory
// correlation is deliberately refused outright, never just ranked low. Two
// pinned regression fixtures are why: aisuite/agents_extra/artifact_store.py
// must NOT steal aisuite/agents/'s own tests/agents/test_artifact_store.py
// just because the basenames coincide, and flask's deep example apps
// (examples/celery/src/task_app/views.py, 4 segments) must NOT collide with
// src/flask's own top-level tests/test_views.py — both are basename
// matches with a directory that shares nothing real with the source's own,
// and the pre-existing depth-bounded convention list already refuses
// exactly this shape of match for the identical reason (see pyPlugin.
// TestRules' flat-form doc comment). Among files that DO clear the bar, the
// one with the highest score wins; ties break on the shallower path, then
// lexicographically, so the result is deterministic.
func bestSearchMatch(codePath string, roots []string, basenames map[string]bool, universe []string) (string, bool) {
	srcSegs := normalizeSegs(segments(filepath.Dir(filepath.ToSlash(codePath))))

	var best string
	bestScore := 0
	var bestSegs []string
	for _, f := range universe {
		f = filepath.ToSlash(f)
		if !UnderRoots(f, roots) || !basenames[filepath.Base(f)] {
			continue
		}
		segs := normalizeSegs(segments(filepath.Dir(f)))
		score := pathStemScore(segs, srcSegs)
		if score == 0 {
			continue
		}
		switch {
		case best == "":
			best, bestScore, bestSegs = f, score, segs
		case score > bestScore:
			best, bestScore, bestSegs = f, score, segs
		case score == bestScore:
			if len(segs) < len(bestSegs) || (len(segs) == len(bestSegs) && f < best) {
				best, bestSegs = f, segs
			}
		}
	}
	return best, best != ""
}
