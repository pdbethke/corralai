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
	// ReasonNotRegularFile covers symlinks, FIFOs, sockets and devices. A
	// symlink is the dangerous one: `secrets.py -> ~/.aws/credentials` in a
	// cloned repo would otherwise be auto-discovered, digested, shipped to a
	// model provider and copied into the jail workspace. The rest simply
	// cannot be audited (a FIFO read blocks forever). Fail closed: they are
	// accounted for, never followed.
	ReasonNotRegularFile = "not-a-regular-file"
	// ReasonSkippedDir marks a file inside a directory the walk does not
	// descend (build output, dependency trees). It is still ACCOUNTED: a
	// reader must be able to see the scan chose not to look there, rather
	// than see a repo that appears smaller than it is.
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

// invisibleDirs are skipped WITHOUT accounting. .git is not source, and
// listing its objects would swamp the report with thousands of entries that
// tell a reader nothing.
var invisibleDirs = map[string]bool{".git": true}

// skippedDirFiles enumerates the regular files under dir as skipped-dir
// exclusions, keyed repo-relative to root. Non-regular entries (symlinks,
// devices) are not followed and not listed: the point is an honest count of
// source files not looked at, not an inventory of the filesystem.
func skippedDirFiles(dir, root string) ([]Exclusion, error) {
	var out []Exclusion
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
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
		tp := filepath.ToSlash(p.TestPath(rel))
		if tp == "" || !present[tp] {
			excl = append(excl, Exclusion{Path: rel, Reason: ReasonNoPairedTest})
			continue
		}
		cands = append(cands, Candidate{Path: rel, TestPath: tp, Lang: p.Name()})
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].Path < cands[j].Path })
	sort.Slice(excl, func(i, j int) bool { return excl[i].Path < excl[j].Path })
	return cands, excl, nil
}

// isTestFile reports whether rel is itself a test file, detected by the
// naming markers the five language plugins use. The markers are the real
// check: no current plugin's TestPath is idempotent on an already-test path
// (`foo_test.go` becomes `foo_test_test.go`), so the fixed-point check below
// is a cheap belt-and-braces for a plugin that someday IS idempotent — it
// never fires today.
func isTestFile(p lang.Plugin, rel string) bool {
	if filepath.ToSlash(p.TestPath(rel)) == rel {
		return true
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
