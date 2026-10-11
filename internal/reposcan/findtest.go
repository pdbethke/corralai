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
	"github.com/pdbethke/corralai/internal/pairing"
)

// SearchResult is FindTest's outcome: what it tried, where it looked, and
// what (if anything) it found. It IS pairing.Resolution (an alias, so the
// field docs live there): a struct copy with a field-by-field converter used
// to sit here, and it had already dropped Rank — the converter that fails
// open on whatever field nobody remembered to add.
type SearchResult = pairing.Resolution

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
//
// A subdirectory this process cannot read (permission denied, most often)
// is skipped rather than aborting the whole walk: FindTest's caller turns a
// walk error into a raw "no test found: <os error>" message that throws away
// the "Looked for:" listing a stranger actually needs — one unreadable
// directory anywhere under root must not cost the whole disclosure. Only an
// error reading the root itself (nothing left to walk at all) still
// propagates.
func walkSkippingBuildDirs(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
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
//  1. p's own pairing.Candidates(p.TestRules(), codePath), IN ORDER — the
//     first one that exists on disk under root wins. This is unchanged from
//     every caller's prior behavior (they used to take the rank-0 candidate
//     unconditionally, or
//     Enumerate's own present-map probe); FindTest just does the same probe
//     as a named, reusable step. A conventionally-mirrored file is paired
//     here and NEVER reaches stage 2.
//  2. Only when NONE of those candidates exists: a recursive search of p's
//     conventional test roots (pairing.Roots) for one of the SAME basenames
//     the candidates already try, picking the most specific match when more
//     than one qualifies. Both stages are pairing.Resolve; FindTest only
//     supplies the disk (os.Stat) and the listing (gitVisibleFiles).
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
	return pairing.Resolve(p.TestRules(), codePath, pairing.Sources{
		Exists: func(cp string) bool {
			full := cp
			// Guard, not a fix for a live bug: every caller today either passes
			// root == "" (doctor.go, checkPairing) or a repo-relative cp that is
			// never absolute, so this never actually joins in practice. It exists
			// so a FUTURE non-empty-root caller with an absolute candidate path
			// can't silently get a mis-joined path out of Stat.
			if root != "" && !filepath.IsAbs(cp) {
				full = filepath.Join(root, cp)
			}
			fi, err := os.Stat(full)
			return err == nil && fi.Mode().IsRegular()
		},
		Universe: func() ([]string, error) { return gitVisibleFiles(root) },
	})
}

// findInUniverse is Enumerate's own use of the same two-stage rule, over a
// universe it has ALREADY computed (present, the whole-repo walk's
// gitignore-and-skipDir-clean file set) rather than re-deriving one with a
// fresh git ls-files call per candidate source file — Enumerate already
// paid for that walk once, for the entire repo, and re-running it per file
// would turn an O(files) scan into an O(files²) one. presentList is
// present's keys as a slice (built once by the caller and reused across
// every file), since pairing.Resolve's search needs to range over it in an
// order the map itself does not offer for free.
//
// Returns the resolved path, its Rank (the matching candidate's own
// pairing.Candidate Rank for a convention hit, pairing.SearchRank for a
// search hit — see pairing.SearchRank's doc comment for why that value must
// never compete with a real convention rank), and whether the recursive
// fallback is what found it. tp == "" means neither stage found anything.
func findInUniverse(p lang.Plugin, codePath string, present map[string]bool, presentList []string) (tp string, rank int, viaSearch bool) {
	// Universe cannot fail here: presentList is already in hand, so the error
	// Resolve returns is always nil and there is nothing to surface.
	r, _ := pairing.Resolve(p.TestRules(), codePath, pairing.Sources{
		Exists:   func(cp string) bool { return present[cp] },
		Universe: func() ([]string, error) { return presentList, nil },
	})
	if !r.Found {
		return "", 0, false
	}
	return r.Path, r.Rank, r.ViaSearch
}
