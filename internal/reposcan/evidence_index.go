// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
)

// EvidenceIndex is what ONE instrumented run's evidence says about EVERY
// source file it measured, reduced to exactly what candidacy needs: how many
// tests cover a file, and which test FILE covers it the most (by executed
// lines) — the authored-landing hint for an evidence-only candidate. Built
// once per scan by ParseEvidenceIndex and consulted for every candidacy
// decision, so the evidence is parsed exactly once regardless of repo size.
type EvidenceIndex struct {
	files map[string]evidenceFileEntry
}

type evidenceFileEntry struct {
	coveringTests int
	mostCovering  string // the covering test FILE with the most executed lines; "" when coveringTests == 0
	// hasStatic is true when the evidence also recorded coverage for this
	// file OUTSIDE any test context — import/module-load time execution a
	// selector cannot attribute to one test (see lang.FileCoverage.HasStatic).
	// A file with coveringTests == 0 and hasStatic true was genuinely
	// executed, just never by a test directly — WidenCandidacyByEvidence's
	// ReasonImportOnly, distinct from ReasonUncovered (coveringTests == 0
	// AND hasStatic false: nothing executed it at all).
	hasStatic bool
}

// CoverageFor answers one file: how many tests cover it (0 is a genuine
// finding — the evidence measured this file and no test executed it
// DIRECTLY), the most-covering test FILE (empty when coveringTests is 0),
// whether the evidence ALSO recorded coverage for it outside any test
// context (import/module-load time — see lang.FileCoverage.HasStatic; a
// file with coveringTests == 0 and hasStatic true was genuinely executed,
// just never by a test), and whether the evidence measured this file AT
// ALL. measured=false means the file never appeared in the evidence's own
// file list — absence of evidence, which a caller must NEVER treat as
// evidence of absence (see the design's Failure posture decision): the
// zero values of every other return carry no meaning in that case.
func (idx EvidenceIndex) CoverageFor(path string) (coveringTests int, mostCoveringTestPath string, hasStatic bool, measured bool) {
	f, ok := idx.files[path]
	if !ok {
		return 0, "", false, false
	}
	return f.coveringTests, f.mostCovering, f.hasStatic, true
}

// ParseEvidenceIndex builds an EvidenceIndex from one scan's selection
// evidence, for a single language plugin. ok is false exactly when there is
// no usable index to build — the evidence never ran, the plugin has no
// selector (a selector-less language, e.g. under --whole-suite), or the
// evidence could not be parsed — every one of which is the "fall back to
// pairing-only candidacy" case the design calls for.
//
// It reuses lang.TestSelector.Index — the SAME parsing Select uses for the
// per-file path — rather than re-reading the evidence's own document format
// a second time from this package.
func ParseEvidenceIndex(ev SelectionEvidence, plug lang.Plugin) (EvidenceIndex, bool) {
	if !ev.Ran {
		return EvidenceIndex{}, false
	}
	sel, ok := plug.(lang.TestSelector)
	if !ok {
		return EvidenceIndex{}, false
	}
	raw, err := sel.Index(ev.Raw)
	if err != nil || raw == nil {
		return EvidenceIndex{}, false
	}

	files := make(map[string]evidenceFileEntry, len(raw))
	for path, fc := range raw {
		best, bestLines := "", -1
		for testID, lines := range fc.Tests {
			testFile := testFileFromNodeID(testID)
			if lines > bestLines || (lines == bestLines && moreSpecificTestPath(testFile, best)) {
				best, bestLines = testFile, lines
			}
		}
		n := len(fc.Tests)
		if n == 0 {
			best = ""
		}
		files[path] = evidenceFileEntry{coveringTests: n, mostCovering: best, hasStatic: fc.HasStatic}
	}
	return EvidenceIndex{files: files}, true
}

// testFileFromNodeID strips a test selector's node id ("tests/test_api.py::test_x")
// down to its containing file. A selector with no "::" (a bare test-file
// path) passes through unchanged.
func testFileFromNodeID(id string) string {
	if i := strings.Index(id, "::"); i >= 0 {
		return id[:i]
	}
	return id
}

// moreSpecificTestPath breaks a tie between two covering tests that executed
// the SAME number of lines of the candidate: more path segments wins (a
// deeper, narrower test file beats a shallow catch-all one), and a further
// tie goes to the lexicographically smaller path — deterministic regardless
// of the evidence map's iteration order.
func moreSpecificTestPath(a, b string) bool {
	da, db := strings.Count(a, "/"), strings.Count(b, "/")
	if da != db {
		return da > db
	}
	return a < b
}
