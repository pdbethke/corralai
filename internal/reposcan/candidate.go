// SPDX-License-Identifier: Elastic-2.0

// Package reposcan fans corral's single-file adequacy audit out over a whole
// repository: enumerate candidates, emit owner-keyed jobs, run them through an
// Executor, aggregate the verdicts into one report with complete accounting.
package reposcan

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
)

// Candidate is one source file that can be audited, paired with the test
// file that is supposed to be exercising it.
type Candidate struct {
	Path     string
	TestPath string
	Lang     string
}

// Exclusion is a file deliberately NOT audited, with a machine-stable reason.
// Exclusions are reported, never dropped: the repo report must account for
// every file so a reader can see what the score does and does not cover.
type Exclusion struct {
	Path   string
	Reason string
}

// Exclusion reasons.
const (
	ReasonNoLanguage   = "no-language"
	ReasonIsTest       = "is-test"
	ReasonNoPairedTest = "no-paired-test"
	// ReasonAmbiguousTest marks a source file whose resolved test path is
	// ALSO claimed by at least one other source file at the same or better
	// specificity rank. Ordered TestPaths candidates broke the injectivity
	// the old single-path design had for free (one source, one test, no two
	// sources could ever name the same test) — this reason is the repair:
	// a wrong pairing plants mutants in one file and grades them against a
	// DIFFERENT file's tests, producing a confident, signed, wrong adequacy
	// verdict. An accounted non-pairing is always the safer failure. See
	// demoteAmbiguousPairings.
	ReasonAmbiguousTest = "ambiguous-test"
	// ReasonNotRegularFile covers symlinks, FIFOs, sockets and devices. A
	// symlink is the dangerous one: `secrets.py -> ~/.aws/credentials` in a
	// cloned repo would otherwise be auto-discovered, digested, shipped to a
	// model provider and copied into the jail workspace. The rest simply
	// cannot be audited (a FIFO read blocks forever). Fail closed: they are
	// accounted for, never followed.
	ReasonNotRegularFile = "not-a-regular-file"
	// ReasonSkippedDir marks a file inside a directory the walk does not
	// descend (build output: dist, build, target, .tox, site-packages). It is
	// still ACCOUNTED: a reader must be able to see the scan chose not to look
	// there, rather than see a repo that appears smaller than it is. VCS and
	// dependency trees are the exception — see invisibleDirs.
	ReasonSkippedDir = "skipped-dir"
)

// skipDirs are never walked: dependency, build-output and VCS trees are not
// the subject of an audit of THIS repo's tests, and letting them into the walk
// puts vendored third-party code into the report's denominator.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, ".bundle": true, "testdata": true,
	"dist": true, "build": true, "target": true, ".tox": true,
	"site-packages": true,
}

// invisibleDirs are skipped WITHOUT accounting. Two kinds of tree qualify,
// for the same reason:
//
//   - .git is not source at all — listing its objects would swamp the report.
//   - DEPENDENCY trees are not THIS repo's source either; they are
//     third-party code the audit has no business grading, and they are
//     enormous. node_modules alone is routinely tens of thousands of files;
//     a Python virtualenv's site-packages is comparable. Enumerating them
//     buries the handful of entries a reader actually needs in a report that
//     gets signed and anchored.
//
// BUILD OUTPUT (dist, build, target) stays ACCOUNTED: those trees are small
// and derived from this repo, so a reader benefits from seeing the scan chose
// not to look there.
//
// .tox is classified as a dependency tree rather than build output because a
// tox environment contains full virtualenvs — it is site-packages wearing a
// different name, not a modest build artifact.
var invisibleDirs = map[string]bool{
	".git": true,
	// Dependency trees, by ecosystem: node, go, python (×3), ruby.
	"node_modules": true, "vendor": true,
	".venv": true, "venv": true, "site-packages": true, ".tox": true,
	".bundle": true,
}

// skippedDirFiles enumerates the regular files under dir as skipped-dir
// exclusions, keyed repo-relative to root. Non-regular entries (symlinks,
// devices) are not followed and not listed: the point is an honest count of
// source files not looked at, not an inventory of the filesystem.
//
// Degradation: a subtree that cannot be read (permissions, a directory that
// vanished mid-scan) is NOT scan-fatal. These are trees the audit deliberately
// does not look at — before this accounting existed the walk never descended
// into them at all, so an unreadable build/ could not affect a scan. Failing
// the whole run over one would be a regression. The unreadable subtree is
// recorded as a single skipped-dir entry and the walk continues.
func skippedDirFiles(dir, root string) ([]Exclusion, error) {
	var out []Exclusion
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Account the unreadable path as one entry and move on. d may be
			// nil here (an error stat-ing the root of the subtree).
			if rel, rerr := filepath.Rel(root, p); rerr == nil {
				out = append(out, Exclusion{Path: filepath.ToSlash(rel), Reason: ReasonSkippedDir})
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// A dependency tree NESTED inside an accounted one is still
			// invisible: build/node_modules is no more worth enumerating
			// than a top-level node_modules, and it is just as large.
			// Without this, accounting build/ drags its whole dependency
			// tree into a signed report.
			if p != dir && invisibleDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, Exclusion{Path: filepath.ToSlash(rel), Reason: ReasonSkippedDir})
		return nil
	})
	return out, err
}

// Enumerate walks root and classifies every file into an audit candidate or
// an exclusion with a reason. Results are sorted by path so a scan of the
// same tree always produces the same job order.
func Enumerate(root string) ([]Candidate, []Exclusion, error) {
	var cands []Candidate
	var excl []Exclusion

	// First pass: which repo-relative paths exist, so pairing can check.
	present := map[string]bool{}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				// Account the files we are choosing not to look at. Without
				// this they vanish from the walked total, making the repo
				// look smaller than it is in a report that gets signed.
				if !invisibleDirs[d.Name()] {
					sub, serr := skippedDirFiles(path, root)
					if serr != nil {
						return serr
					}
					excl = append(excl, sub...)
				}
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(rel)
		// ONLY regular files are auditable. Everything else — above all a
		// symlink, which can point anywhere on the host — is excluded with a
		// reason instead of being enumerated. The scan AUTO-DISCOVERS its
		// subjects (unlike `certify --local`, where the operator names the
		// file), so following a link here would be a repository choosing what
		// the audit reads off the operator's disk.
		if !d.Type().IsRegular() {
			excl = append(excl, Exclusion{Path: slash, Reason: ReasonNotRegularFile})
			return nil
		}
		present[slash] = true
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, nil, err
	}

	// rank tracks, per candidate (parallel to cands, before the ambiguity
	// pass below), the SPECIFICITY of the match: the index in the plugin's
	// ordered TestPaths list where the paired test was found. Lower is more
	// specific. It only exists to resolve cross-source collisions below and
	// is discarded once that pass is done — Candidate itself carries no
	// notion of rank.
	var rank []int

	for rel := range present {
		p, ok := lang.Detect(rel)
		if !ok {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonNoLanguage})
			continue
		}
		// A file that IS the sibling test of some source file is not itself
		// a subject. Detected structurally: its own conventional test path
		// differs from itself only for non-test files.
		if isTestFile(p, rel) {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonIsTest})
			continue
		}
		// Walk the plugin's ordered candidates and pair with the first one
		// that actually exists in this repo. The list is ordered most
		// specific first (see each plugin's TestPaths), so a sibling or
		// full-directory-mirror test wins over a same-named test that could
		// plausibly belong to a different source file.
		tp := ""
		tpRank := -1
		for i, cand := range p.TestPaths(rel) {
			cand = filepath.ToSlash(cand)
			if cand != "" && present[cand] {
				tp = cand
				tpRank = i
				break
			}
		}
		if tp == "" {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonNoPairedTest})
			continue
		}
		cands = append(cands, Candidate{Path: rel, TestPath: tp, Lang: p.Name()})
		rank = append(rank, tpRank)
	}

	cands, excl = demoteAmbiguousPairings(cands, rank, excl)

	sort.Slice(cands, func(i, j int) bool { return cands[i].Path < cands[j].Path })
	sort.Slice(excl, func(i, j int) bool { return excl[i].Path < excl[j].Path })
	return cands, excl, nil
}

// demoteAmbiguousPairings enforces, as a global property (not a per-plugin
// heuristic), that one test file grades exactly one source file. The
// per-plugin TestPaths ordering minimizes collisions but cannot rule them
// out — two sources can each legitimately resolve to the SAME test path
// (observed on real repos: flask's tests/test_views.py matched THREE
// distinct source files; tests/test_blueprints.py matched two). Signing a
// mutation-adequacy verdict for one file using a test suite that actually
// belongs to another is worse than not auditing the file at all, so this
// pass runs AFTER every source has independently resolved its pairing and
// removes every pairing that isn't safely unambiguous:
//
//   - Group the just-resolved candidates by TestPath.
//   - A group of size 1 is untouched — no collision.
//   - In a group of size >1, if exactly one member has a STRICTLY better
//     (lower) specificity rank than every other member, it is kept — a
//     sibling or full-directory-mirror match outranks a same-named test that
//     merely happens to also resolve via a less specific form for some other
//     file — and every other member is demoted.
//   - If the best rank is TIED across two or more members, ALL of them are
//     demoted. This does lose a correct pairing sometimes (a genuine
//     sibling match demoted because an unrelated file's own sibling match
//     collides implausibly) — that is the intended trade: an accounted
//     ReasonAmbiguousTest exclusion is honest; a coin-flip pairing that
//     happens to land on the right file some fraction of the time is not.
//
// rank[i] is the specificity index (lower = more specific) at which
// cands[i].TestPath was resolved; it is parallel to cands and produced by
// the same loop in Enumerate, never persisted on Candidate itself.
func demoteAmbiguousPairings(cands []Candidate, rank []int, excl []Exclusion) ([]Candidate, []Exclusion) {
	groups := map[string][]int{} // TestPath -> indices into cands
	for i, c := range cands {
		groups[c.TestPath] = append(groups[c.TestPath], i)
	}

	demoted := map[int]bool{}
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		best := rank[idxs[0]]
		for _, i := range idxs[1:] {
			if rank[i] < best {
				best = rank[i]
			}
		}
		var atBest []int
		for _, i := range idxs {
			if rank[i] == best {
				atBest = append(atBest, i)
			}
		}
		if len(atBest) == 1 {
			// Exactly one strictly-best claimant: keep it, demote the rest.
			for _, i := range idxs {
				if i != atBest[0] {
					demoted[i] = true
				}
			}
			continue
		}
		// Tied for best (or, degenerately, every member shares one rank):
		// no safe winner — demote the whole group.
		for _, i := range idxs {
			demoted[i] = true
		}
	}
	if len(demoted) == 0 {
		return cands, excl
	}

	kept := cands[:0:0]
	for i, c := range cands {
		if demoted[i] {
			excl = append(excl, Exclusion{Path: c.Path, Reason: ReasonAmbiguousTest})
			continue
		}
		kept = append(kept, c)
	}
	return kept, excl
}

// isTestFile reports whether rel is itself a test file, detected by the
// naming markers the five language plugins use. The markers are the real
// check and do NOT depend on the shape of TestPaths at all — a parallel-tree
// test like tests/agents/test_artifact_store.py is caught by the "test_"
// prefix marker exactly like a sibling test_artifact_store.py would be, so
// widening TestPaths from one path to an ordered list changes nothing here.
//
// The fixed-point check below (does rel appear in ITS OWN TestPaths list) is
// a cheap belt-and-braces for a plugin that is someday idempotent on an
// already-test path — no current plugin is (`foo_test.go`'s own conventions
// produce `foo_test_test.go`, `test_test_foo.py`, etc, never `foo_test.go`
// itself), so this never fires today either.
func isTestFile(p lang.Plugin, rel string) bool {
	for _, tp := range p.TestPaths(rel) {
		if filepath.ToSlash(tp) == rel {
			return true
		}
	}

	// Check against the basename only to avoid directory-component matches.
	base := filepath.Base(rel)

	// _test. suffix (Go: foo_test.go, Ruby minitest: foo_test.rb)
	if strings.Contains(base, "_test.") {
		return true
	}

	// test_ prefix (Python: test_foo.py, Ruby: test_foo.rb)
	if strings.HasPrefix(base, "test_") {
		return true
	}

	// _spec. suffix (Ruby RSpec: foo_spec.rb, JavaScript: foo_spec.js, TypeScript: foo_spec.ts)
	if strings.Contains(base, "_spec.") {
		return true
	}

	// .test. suffix (JavaScript: foo.test.js, TypeScript: foo.test.ts)
	if strings.Contains(base, ".test.") {
		return true
	}

	// .spec. suffix (JavaScript: foo.spec.js, TypeScript: foo.spec.ts)
	if strings.Contains(base, ".spec.") {
		return true
	}

	// spec_ prefix (Ruby: spec_foo.rb)
	if strings.HasPrefix(base, "spec_") {
		return true
	}

	return false
}
