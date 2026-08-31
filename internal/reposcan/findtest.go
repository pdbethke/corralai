// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
)

// SearchRank is the Rank a via-search pairing carries into
// demoteAmbiguousPairings: strictly less specific (a higher number) than any
// rank a shipped plugin's own TestPaths ever hands out (today's max is 3,
// Python's flat form) — a fuzzy recursive match is weaker evidence than ANY
// convention-list hit, by construction, so it must never out-rank one in a
// cross-source collision.
const SearchRank = 100

// searchTestRoot is reposcan's own default fallback for a plugin that
// implements no lang.TestRooter of its own — see that interface's doc
// comment. Merged additively with whatever the plugin names, never a
// replacement for it.
const searchTestRoot = "tests"

// testRootsFor returns the top-level directories FindTest searches
// recursively for p: the generic "tests" default, plus whatever p's own
// lang.TestRooter (if implemented) adds — deduplicated, order-stable.
func testRootsFor(p lang.Plugin) []string {
	roots := []string{searchTestRoot}
	seen := map[string]bool{searchTestRoot: true}
	if r, ok := p.(lang.TestRooter); ok {
		for _, extra := range r.TestRoots() {
			extra = strings.Trim(filepath.ToSlash(extra), "/")
			if extra == "" || seen[extra] {
				continue
			}
			seen[extra] = true
			roots = append(roots, extra)
		}
	}
	return roots
}

// searchBasenames collects the distinct basenames p's own TestPaths
// candidates use — "test the conventional NAME, search a WIDER set of
// directories for it" rather than inventing a second, independently
// maintained naming rule that could drift from TestPaths'. For itsdangerous
// this is exactly {"test_signer.py", "signer_test.py"}: the same two names
// pyPlugin.TestPaths already tries as siblings, just no longer confined to
// the directories TestPaths itself derives.
func searchBasenames(cands []lang.TestCandidate) map[string]bool {
	out := map[string]bool{}
	for _, c := range cands {
		if c.Path == "" {
			continue
		}
		out[filepath.Base(filepath.ToSlash(c.Path))] = true
	}
	return out
}

// underRoots reports whether f (a root-relative, slash-separated path) sits
// under one of roots — checked as a TOP-LEVEL first path component, matching
// "tests/**" style conventions rather than a substring match that could fire
// on an unrelated directory that merely contains the word "test".
func underRoots(f string, roots []string) bool {
	first, _, ok := strings.Cut(f, "/")
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
	for _, pre := range []string{"test_", "spec_"} {
		if strings.HasPrefix(s, pre) && s != pre {
			return s[len(pre):]
		}
	}
	for _, suf := range []string{"_test", "_spec"} {
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
// TestPaths' flat-form doc comment). Among files that DO clear the bar, the
// one with the highest score wins; ties break on the shallower path, then
// lexicographically, so the result is deterministic.
func bestSearchMatch(codePath string, roots []string, basenames map[string]bool, universe []string) (string, bool) {
	srcSegs := normalizeSegs(segments(filepath.Dir(filepath.ToSlash(codePath))))

	var best string
	bestScore := 0
	var bestSegs []string
	for _, f := range universe {
		f = filepath.ToSlash(f)
		if !underRoots(f, roots) || !basenames[filepath.Base(f)] {
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

// SearchResult is FindTest's outcome: what it tried, where it looked, and
// what (if anything) it found.
type SearchResult struct {
	// Path is the resolved test path, root-relative and slash-separated.
	// "" when nothing was found.
	Path string
	// ViaSearch is true when Path came from the recursive fallback rather
	// than p's own ordered TestPaths candidates — the fact the "paired by
	// search" disclosure is built on.
	ViaSearch bool
	// Tried is every TestPaths candidate FindTest checked for existence, in
	// order — named in a not-found error so the operator sees exactly what
	// was already ruled out, not just that something failed.
	Tried []string
	// Roots is the set of conventional test directories the recursive
	// fallback searched (only populated when the fallback actually ran —
	// i.e. every Tried candidate was absent).
	Roots []string
	// Found reports whether Path is usable. Path == "" and Found == false
	// coincide today, but Found makes the zero value unambiguous rather
	// than relying on a caller to remember Path == "" means "not found".
	Found bool
}

// gitVisibleFiles enumerates every file the repository at root considers its
// own — git's tracked files plus untracked-but-not-ignored ones — as
// root-relative, slash-separated paths, with the SAME skipDirs (vendor,
// node_modules, .venv, ...) filter applied unconditionally, on top of
// whatever `git ls-files` returns. Outside a git work tree, or with no git on
// PATH, falls back to a plain recursive walk that applies the identical
// filter directly during the walk (walkSkippingBuildDirs) instead of after —
// either way, a dependency tree is never part of the search universe.
//
// The extra filter on the git branch matters because `git ls-files` answers
// "what does git track or see as untracked-but-not-ignored", NOT "what is a
// dependency tree" — a node_modules/ or .venv/ committed by mistake (or an
// untracked one missing from .gitignore) is fully git-visible, and without
// this pass a candidate basename living inside it would be handed back as a
// paired test.
func gitVisibleFiles(root string) ([]string, error) {
	if root == "" {
		// "" means "the working directory" to every OTHER caller in this
		// file (FindTest only ever joins root onto a RELATIVE candidate, so
		// "" already meant cwd there) but is not a valid argument to
		// filepath.WalkDir or a portable one to hand git — normalize once,
		// here, rather than at every call site.
		root = "."
	}
	git, lerr := exec.LookPath("git")
	if lerr == nil {
		probe := exec.Command(git, "-C", root, "rev-parse", "--is-inside-work-tree") // #nosec G204 -- git resolved via LookPath; root is the operator's own path; every other arg literal
		if out, perr := probe.Output(); perr == nil && strings.TrimSpace(string(out)) == "true" {
			ls := exec.Command(git, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard") // #nosec G204 -- same: LookPath binary, operator's root, literal args
			out, lserr := ls.Output()
			if lserr != nil {
				return nil, fmt.Errorf("git ls-files in %s: %w", root, lserr)
			}
			var files []string
			for _, ent := range bytes.Split(out, []byte{0}) {
				if len(ent) == 0 {
					continue
				}
				rel := filepath.ToSlash(string(ent))
				if pathUnderSkippedDir(rel) {
					continue
				}
				files = append(files, rel)
			}
			return files, nil
		} else if perr != nil {
			var exit *exec.ExitError
			if !errors.As(perr, &exit) {
				return nil, fmt.Errorf("git rev-parse in %s: %w", root, perr)
			}
			// Not a work tree: fall through to the plain walk.
		}
	}
	return walkSkippingBuildDirs(root)
}

// walkSkippingBuildDirs is gitVisibleFiles' fallback for a root that is not
// a git work tree: a plain recursive walk that still refuses to descend
// into the same dependency/build/VCS directories Enumerate's own walk
// skips (skipDirs, defined in candidate.go), so a search root can never
// reach into vendor/node_modules/.venv just because git could not tell it
// not to.
func walkSkippingBuildDirs(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files, err
}

// pathUnderSkippedDir reports whether a slash-separated, root-relative path
// has any path SEGMENT in skipDirs — the same unconditional filter
// walkSkippingBuildDirs applies during its own walk, applied here as a
// post-hoc check for git's flat file list, which has no directory-descent
// step to short-circuit.
func pathUnderSkippedDir(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if skipDirs[seg] {
			return true
		}
	}
	return false
}

// FindTest resolves the test file for codePath under root, in two strictly
// ordered stages:
//
//  1. p's own TestPaths(codePath) candidates, IN ORDER — the first one that
//     exists on disk under root wins. This is unchanged from every caller's
//     prior behavior (they used to take TestPaths[0] unconditionally, or
//     Enumerate's own present-map probe); FindTest just does the same probe
//     as a named, reusable step. A conventionally-mirrored file is paired
//     here and NEVER reaches stage 2.
//  2. Only when NONE of those candidates exists: a recursive search of p's
//     conventional test roots (testRootsFor) for one of the SAME basenames
//     TestPaths already tries (searchBasenames), picking the most specific
//     match (bestSearchMatch) when more than one qualifies.
//
// The search universe for stage 2 is gitVisibleFiles(root): the repository's
// own tracked-plus-untracked-but-not-ignored files, so a gitignored
// directory (.venv, node_modules, a stray worktree) is never walked, exactly
// the rule Enumerate's own gitIgnored applies to the whole-repo scan.
//
// Tried and Roots are populated even on a miss, specifically so a caller can
// build a "here is everywhere I looked" error instead of leaving an operator
// to guess a second and third time — see certify_local.go's use.
func FindTest(p lang.Plugin, root, codePath string) (SearchResult, error) {
	cands := p.TestPaths(codePath)

	var res SearchResult
	for _, c := range cands {
		cp := filepath.ToSlash(c.Path)
		if cp == "" {
			continue
		}
		res.Tried = append(res.Tried, cp)
		full := cp
		// Guard, not a fix for a live bug: every caller today either passes
		// root == "" (doctor.go, checkPairing) or a repo-relative cp that is
		// never absolute, so this never actually joins in practice. It exists
		// so a FUTURE non-empty-root caller with an absolute candidate path
		// can't silently get a mis-joined path out of Stat.
		if root != "" && !filepath.IsAbs(cp) {
			full = filepath.Join(root, cp)
		}
		if fi, err := os.Stat(full); err == nil && fi.Mode().IsRegular() {
			res.Path = cp
			res.Found = true
			return res, nil
		}
	}

	res.Roots = testRootsFor(p)
	basenames := searchBasenames(cands)
	if len(basenames) == 0 {
		return res, nil
	}
	universe, err := gitVisibleFiles(root)
	if err != nil {
		return res, err
	}
	if found, ok := bestSearchMatch(codePath, res.Roots, basenames, universe); ok {
		res.Path = found
		res.ViaSearch = true
		res.Found = true
	}
	return res, nil
}

// findInUniverse is Enumerate's own use of the same two-stage rule, over a
// universe it has ALREADY computed (present, the whole-repo walk's
// gitignore-and-skipDir-clean file set) rather than re-deriving one with a
// fresh git ls-files call per candidate source file — Enumerate already
// paid for that walk once, for the entire repo, and re-running it per file
// would turn an O(files) scan into an O(files²) one. presentList is
// present's keys as a slice (built once by the caller and reused across
// every file), since bestSearchMatch needs to range over it in an order the
// map itself does not offer for free.
//
// Returns the resolved path, its Rank (0 for a convention hit, SearchRank
// for a search hit — see SearchRank's doc comment for why that value must
// never compete with a real convention rank), and whether the recursive
// fallback is what found it. tp == "" means neither stage found anything.
func findInUniverse(p lang.Plugin, codePath string, present map[string]bool, presentList []string) (tp string, rank int, viaSearch bool) {
	cands := p.TestPaths(codePath)
	for _, c := range cands {
		cp := filepath.ToSlash(c.Path)
		if cp != "" && present[cp] {
			return cp, c.Rank, false
		}
	}
	roots := testRootsFor(p)
	basenames := searchBasenames(cands)
	if len(basenames) == 0 {
		return "", 0, false
	}
	if found, ok := bestSearchMatch(codePath, roots, basenames, presentList); ok {
		return found, SearchRank, true
	}
	return "", 0, false
}
