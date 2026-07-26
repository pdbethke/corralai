// SPDX-License-Identifier: Elastic-2.0

// Package reposcan fans corral's single-file adequacy audit out over a whole
// repository. It is the shared core behind both the local CLI scan and the
// hosted scan service: the two differ only in who hands jobs to workers.
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
)

// skipDirs are never walked: dependency and VCS trees are not the subject of
// an audit of THIS repo's tests.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, ".bundle": true, "testdata": true,
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
				return filepath.SkipDir
			}
			return nil
		}
		present[filepath.ToSlash(rel)] = true
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

// isTestFile reports whether rel is itself a test file under the plugin's
// naming convention: applying TestPath to a test file is a no-op or the file
// already carries the convention's marker.
func isTestFile(p lang.Plugin, rel string) bool {
	// Check if the plugin's TestPath returns the same path (no-op for test files).
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
