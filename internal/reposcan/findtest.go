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
	"github.com/pdbethke/corralai/internal/repoindex"
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
//
// root is also the directory a source's own declared test path must stay
// inside (pairing.Sources.Root): the doors that take an operator's --code as
// typed pass ".", the directory that --code is relative to — and the one an
// absolute --code is held to. RefusedOutsideRoot names what that refused.
func FindTest(p lang.Plugin, root, codePath string) (SearchResult, error) {
	return pairing.Resolve(p.TestRules(), codePath, DiskSources(p, root))
}

// DiskSources is the pairing.Sources over the repository on disk at root:
// os.Stat for existence, gitVisibleFiles for the search universe, and the
// source's own declared test modules read by diskTestModules. It is FindTest's
// adapter, exported so a door asking where an authored test LANDS
// (lang.TestLanding) reads the repository exactly as the door that paired the
// file did — one adapter, not a second resolver.
func DiskSources(p lang.Plugin, root string) pairing.Sources {
	return pairing.Sources{
		Root: root,
		Exists: func(cp string) bool {
			fi, err := os.Stat(underRoot(root, cp))
			return err == nil && fi.Mode().IsRegular()
		},
		Universe:    func() ([]string, error) { return gitVisibleFiles(root) },
		TestModules: diskTestModules(p, root),
	}
}

// underRoot is the on-disk path of a root-relative slash path. Live guard:
// certify --local and certify --adversarial pass root "." with whatever
// --code the operator gave, and an absolute --code yields absolute candidate
// paths. Joining those onto the root would stat a mis-joined path, so they
// are used as given.
func underRoot(root, rel string) string {
	if root == "" || filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

// readsTestModules is repoindex.ReadsTestModules, a variable only so a test
// in a cgo build (this package cannot build without cgo) can stand in for a
// no-cgo build's answer and drive Enumerate and FindTest through it.
var readsTestModules = repoindex.ReadsTestModules

// diskTestModules is the Sources.TestModules both adapters (FindTest and
// findInUniverse) hand pairing.Resolve: the source file's bytes under root,
// parsed by repoindex for the test modules p's language declares in itself.
// pairing calls it only for a language with a content rule (Rust), so no
// other language's scan reads a single extra file.
//
// A read error is RETURNED, never mapped to "no modules": a file that could
// not be read has not been shown to declare no test, and treating it so
// would report a real source as unpaired for a reason that is not true.
func diskTestModules(p lang.Plugin, root string) func(rel string) ([]pairing.TestModule, error) {
	return func(rel string) ([]pairing.TestModule, error) {
		if !readsTestModules(p.Name()) {
			// A no-cgo build answers nil for every file; that is not
			// "declares no test", and must not be reported as one.
			return nil, pairing.ErrDeclarationsUnreadable
		}
		src, err := os.ReadFile(underRoot(root, rel)) // #nosec G304 -- a source file the scan itself enumerated (or the operator's own --code), under the operator's root
		if err != nil {
			return nil, err
		}
		return repoindex.TestModules(p.Name(), src), nil
	}
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
// modules is the Sources.TestModules to resolve with: diskTestModules over
// the scan's root in production, and a paths-only stand-in in the search
// golden, whose corpus has paths but no file contents. root is the scan's
// root on disk, which a source's declared test path must stay inside
// (pairing.Sources.Root); "" in the golden, which has no disk.
//
// Returns the resolved path, its Rank (the matching candidate's own
// pairing.Candidate Rank for a convention hit, pairing.SearchRank for a
// search hit — see pairing.SearchRank's doc comment for why that value must
// never compete with a real convention rank), and whether the recursive
// fallback is what found it. tp == "" means neither stage found anything.
// The error is modules' (a source that could not be read): Universe cannot
// fail here, because presentList is already in hand.
func findInUniverse(p lang.Plugin, root, codePath string, present map[string]bool, presentList []string, modules func(string) ([]pairing.TestModule, error)) (tp string, rank int, viaSearch bool, err error) {
	r, err := pairing.Resolve(p.TestRules(), codePath, universeSources(root, present, presentList, modules))
	if err != nil {
		return "", 0, false, err
	}
	if !r.Found {
		return "", 0, false, nil
	}
	return r.Path, r.Rank, r.ViaSearch, nil
}

// universeSources is findInUniverse's pairing.Sources: the scan's own walk
// (present, presentList) as the disk and the listing. Enumerate asks where a
// file's authored test lands over the same one, so the landing and the
// pairing read the same tree.
func universeSources(root string, present map[string]bool, presentList []string, modules func(string) ([]pairing.TestModule, error)) pairing.Sources {
	return pairing.Sources{
		Root:        root,
		Exists:      func(cp string) bool { return present[cp] },
		Universe:    func() ([]string, error) { return presentList, nil },
		TestModules: modules,
	}
}

// ResolutionPaths is every path pairing.Resolve checks for codePath, in
// order: the paths the source's own test-module declarations load (Rust),
// then the convention candidates. It is Resolve's own Tried with every check
// answered "absent" and no search — so the list cannot drift from what a
// resolution actually tries, which a second enumeration over
// pairing.Candidates already had (it never saw a content-derived path). The
// orphan check reads it to ask "did the diff delete the test this source
// would have paired to?".
func ResolutionPaths(p lang.Plugin, root, codePath string) ([]string, error) {
	res, err := pairing.Resolve(p.TestRules(), codePath, pairing.Sources{
		Root:        root,
		Exists:      func(string) bool { return false },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: diskTestModules(p, root),
	})
	if err != nil {
		return nil, err
	}
	return res.Tried, nil
}
