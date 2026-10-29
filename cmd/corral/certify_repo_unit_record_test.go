// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestUnitEvidenceReportLine pins the two wordings of a unit-graded file's
// line, with the noun the runner row names and the plural of "unit" right
// for one unit and for several. The reason is the first unreported unit's.
func TestUnitEvidenceReportLine(t *testing.T) {
	line := func(f reposcan.WeakFile) string {
		f.Path = "src/a.ts"
		f.SelectionMethod = "unit-evidence"
		var b bytes.Buffer
		printWeakFile(&b, f)
		return b.String()
	}
	cases := []struct {
		name string
		f    reposcan.WeakFile
		want string
	}{
		{"none unreported", reposcan.WeakFile{SelectedTests: 4, SuiteTests: 1339, UnitNoun: "test files"},
			"graded by 4 of 1,339 test files (unit evidence)"},
		{"two unreported", reposcan.WeakFile{SelectedTests: 4, SuiteTests: 1339, UnitNoun: "test files", Unreported: 2, UnreportedReason: "timed out"},
			"graded by 4 of 1,339 test files (unit evidence; 2 units with no evidence added: timed out)"},
		{"one unreported", reposcan.WeakFile{SelectedTests: 3, SuiteTests: 12, UnitNoun: "packages", Unreported: 1, UnreportedReason: "could not run"},
			"graded by 3 of 12 packages (unit evidence; 1 unit with no evidence added: could not run)"},
		{"red units left out", reposcan.WeakFile{SelectedTests: 4, SuiteTests: 2186, UnitNoun: "test files", Excluded: 545, ExcludedReason: "run failed: exit 1"},
			"graded by 4 of 2,186 test files (unit evidence; 545 units that failed unmutated left out after the baseline failed with them: run failed: exit 1)"},
		{"one red unit left out", reposcan.WeakFile{SelectedTests: 3, SuiteTests: 12, UnitNoun: "packages", Excluded: 1, ExcludedReason: "run failed: exit 1"},
			"graded by 3 of 12 packages (unit evidence; 1 unit that failed unmutated left out after the baseline failed with it: run failed: exit 1)"},
		// A compiler's reason runs to many lines; the file's line keeps the
		// first (the verdict and the ledger keep it whole).
		{"multi-line reason", reposcan.WeakFile{SelectedTests: 1, SuiteTests: 99, UnitNoun: "packages", Excluded: 18,
			ExcludedReason: "run failed: exit 1: # github.com/smacker/go-tree-sitter/php\nparser.c:1:10: fatal error\nFAIL"},
			"graded by 1 of 99 packages (unit evidence; 18 units that failed unmutated left out after the baseline failed with them: run failed: exit 1: # github.com/smacker/go-tree-sitter/php …)\n"},
		{"rust", reposcan.WeakFile{SelectedTests: 1, SuiteTests: 7, UnitNoun: "test targets"},
			"graded by 1 of 7 test targets (unit evidence)"},
	}
	for _, c := range cases {
		if got := line(c.f); !strings.Contains(got, c.want) {
			t.Errorf("%s: line = %q, want it to contain %q", c.name, got, c.want)
		}
	}
	// No fallback noun: a unit verdict whose row named none says so.
	if got := line(reposcan.WeakFile{SelectedTests: 4, SuiteTests: 9}); !strings.Contains(got, "no unit noun") || strings.Contains(got, " tests") {
		t.Errorf("a unit line with no noun must say so, not invent one: %q", got)
	}
}

// Python's coverage-context line is byte for byte what it was before units
// had a noun: "tests", the method in parentheses, nothing else.
func TestCoverageContextReportLineIsUnchanged(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "a.py", KillRate: 0.5, Survivors: 1,
		SelectionMethod: "coverage-context", SelectedTests: 14, SuiteTests: 1431})
	want := "    0.50  a.py survivors=1   graded by 14 of 1431 tests (coverage-context)\n"
	if !strings.Contains(b.String(), "   graded by 14 of 1431 tests (coverage-context)") {
		t.Errorf("coverage-context line moved:\n got %q\nwant …%q", b.String(), want)
	}
}

// An authored refusal prints on its own line, beginning with the words the
// reader greps for, and only when there is one.
func TestAuthoredFallbackPrintsOnItsOwnLine(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "a.ts", SelectionMethod: "unit-evidence", SelectedTests: 1, SuiteTests: 2, UnitNoun: "test files",
		AuthoredFallback: "its filter would also run tests/two.test.ts"})
	if !strings.Contains(b.String(), "\n   authored test graded by the whole suite: its filter would also run tests/two.test.ts") {
		t.Errorf("authored fallback line missing:\n%s", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "a.ts", SelectionMethod: "unit-evidence", SelectedTests: 1, SuiteTests: 2, UnitNoun: "test files"})
	if strings.Contains(b.String(), "authored test graded") {
		t.Errorf("no refusal, yet a fallback line printed:\n%s", b.String())
	}
}

// The value survives the boundary (AGENTS.md, "the bug shape that keeps
// recurring"): a verdict whose selection added two unreported units and whose
// authored pass fell back reaches the report, the scan's file row, the
// ledger row's JSON and the signed attestation. The verdicts of BOTH
// construction sites carry these fields (advpool's
// TestUnitNounAndFirstReasonReachTheVerdict and
// TestAnAuthoredRefusalIsDisclosedOnTheVerdict); this test starts from such
// a verdict and follows it out.
func TestUnreportedCountSurvivesToTheLedgerAndAttestation(t *testing.T) {
	root := t.TempDir()
	const why = "its filter would also run tests/two.test.ts"
	mustWrite(t, filepath.Join(root, "src", "a.ts"), "export const a = 1;\n")
	v := advpool.Verdict{
		DevKillRate: 0.5, MutantsTotal: 4, Survivors: 2,
		TestSelection: advpool.TestSelection{Method: "unit-evidence", Selected: 4, Of: 10, Unreported: 2,
			UnreportedReason: "timed out", UnitNoun: "test files", AuthoredFallback: why,
			Excluded: 3, ExcludedReason: "run failed: exit 1"},
	}
	res := reposcan.FileResult{Job: reposcan.Job{Path: "src/a.ts", Lang: "typescript"}, Verdict: v, Gradable: true}

	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{res}, nil)
	if len(rep.Weakest) != 1 {
		t.Fatalf("report has %d files", len(rep.Weakest))
	}
	w := rep.Weakest[0]
	if w.Unreported != 2 || w.UnreportedReason != "timed out" || w.UnitNoun != "test files" || w.AuthoredFallback != why ||
		w.Excluded != 3 || w.ExcludedReason != "run failed: exit 1" {
		t.Fatalf("report dropped a field: %+v", w)
	}

	files := buildScanFileRows([]reposcan.FileResult{res}, nil, reposcan.CoverageMap{}, "", root, io.Discard)
	if len(files) != 1 || files[0].SelectionUnreported != 2 || files[0].AuthoredFallback != why || files[0].SelectionExcluded != 3 {
		t.Fatalf("scan row dropped a field: %+v", files)
	}

	b := buildBundle(scanstore.Scan{Owner: "o", Repo: "r", Commit: "c"}, 1, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
	if len(b.Files) != 1 || b.Files[0].SelectionUnreported != 2 || b.Files[0].AuthoredFallback != why || b.Files[0].SelectionExcluded != 3 {
		t.Fatalf("ledger row dropped a field: %+v", b.Files)
	}
	raw, err := json.Marshal(b.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"selection_unreported":2`, `"selection_excluded":3`, `"authored_fallback":"` + why + `"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ledger row JSON lacks %s: %s", want, raw)
		}
	}
	// And back out of the ledger into the scan's file, the way `corral scans`
	// reads it.
	if back := fileFromRow(b.Files[0]); back.SelectionUnreported != 2 || back.AuthoredFallback != why || back.SelectionExcluded != 3 {
		t.Errorf("fileFromRow dropped a field: %+v", back)
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "statement.json")
	if _, _, err := writeAuditStatement(out, dir, rep, map[string]string{"test-writer": "w"}, nil, nil, true, 0, auditpush.Bundle{}); err != nil {
		t.Fatalf("writeAuditStatement: %v", err)
	}
	stmt, err := os.ReadFile(out) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Predicate struct {
			Files []struct {
				SelectionUnreported int    `json:"selectionUnreported"`
				SelectionExcluded   int    `json:"selectionExcluded"`
				AuthoredFallback    string `json:"authoredFallback"`
			} `json:"files"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(stmt, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Predicate.Files) != 1 || doc.Predicate.Files[0].SelectionUnreported != 2 || doc.Predicate.Files[0].AuthoredFallback != why ||
		doc.Predicate.Files[0].SelectionExcluded != 3 {
		t.Errorf("attestation dropped a field: %s", stmt)
	}
}
