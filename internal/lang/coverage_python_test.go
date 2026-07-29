// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"testing"
)

// The fixtures below are trimmed EXCERPTS of a real `coverage json -o -`
// report, captured by cloning pallets/flask (shallow) into a scratch venv,
// running:
//
//	python3 -m coverage run -m pytest -q
//	python3 -m coverage json -o -
//
// and slimming the genuine "files" entries down to the "summary.covered_lines"
// / "summary.num_statements" fields ParseCoverage actually reads (dropping the
// per-line executed_lines/missing_lines/functions/classes breakdown, which is
// real but irrelevant to this contract and would make the fixture enormous).
// The two leading lines of pytest's own "-q" dot-progress output are kept
// verbatim in fixtureMixed to exercise preamble-skipping: coverage.py writes
// its JSON as the LAST line of a `sh -c 'coverage run ...; coverage json -o -'`
// script's combined stdout, with the wrapped test command's own output ahead
// of it — the same "everything before the payload is preamble" shape Task 1
// found for Go's "mode:" header, except here the payload is identified
// structurally (JSON on the last non-blank line), not by a header token.
// See .superpowers/sdd/2026-07-29-coverage-preflight/task-2-report.md for the
// full transcript this was captured from.

// fixtureMixed: real numbers for src/flask/app.py (395/435 lines covered) and
// src/flask/helpers.py (124/132), and a genuinely ZERO-coverage real file,
// src/flask/__main__.py (0/2) — flask's tests never execute the CLI
// entrypoint module, which is exactly the "some files, not all, have
// coverage" case this fixture is for.
const fixtureMixed = `........................................................................ [ 14%]
........................................................................ [ 29%]
{"meta": {"format": 3, "version": "7.15.2", "timestamp": "2026-07-29T12:01:40.588790", "branch_coverage": true, "show_contexts": false}, "files": {"src/flask/app.py": {"summary": {"covered_lines": 395, "num_statements": 435}}, "src/flask/__main__.py": {"summary": {"covered_lines": 0, "num_statements": 2}}, "src/flask/helpers.py": {"summary": {"covered_lines": 124, "num_statements": 132}}}, "totals": {"covered_lines": 7525, "num_statements": 7913}}`

// fixtureAllZero: every file entry in this real report has covered_lines: 0
// (three files flask's suite never touches at all). This is the ONE
// legitimate case ParseCoverage must return an EMPTY map with NO error —
// a well-formed report saying "nothing here was executed" is data, not a
// parse failure.
const fixtureAllZero = `{"meta": {"format": 3, "version": "7.15.2", "timestamp": "2026-07-29T12:01:40.588790", "branch_coverage": true, "show_contexts": false}, "files": {"src/flask/__main__.py": {"summary": {"covered_lines": 0, "num_statements": 2}}, "tests/test_apps/blueprintapp/apps/__init__.py": {"summary": {"covered_lines": 0, "num_statements": 0}}, "tests/test_apps/cliapp/__init__.py": {"summary": {"covered_lines": 0, "num_statements": 0}}}, "totals": {"covered_lines": 7525, "num_statements": 7913}}`

func TestPythonParseCoverageExecutedFiles(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureMixed, "")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	// __main__.py has covered_lines: 0 and must NOT appear.
	want := map[string]bool{"src/flask/app.py": true, "src/flask/helpers.py": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestPythonParseCoverageAllZeroIsEmptyNotError pins the one legitimate empty
// result: a well-formed report in which every file has zero coverage returns
// an empty map and no error — distinct from an unparseable report, which must
// error (see TestPythonParseCoverageUnparseableIsError below).
func TestPythonParseCoverageAllZeroIsEmptyNotError(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAllZero, "")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("executed set = %v, want empty", got)
	}
	if got == nil {
		t.Fatalf("executed set is nil, want a non-nil empty map")
	}
}

// TestPythonParseCoverageUnparseableIsError mirrors the Go plugin's test of
// the same name: a report ParseCoverage cannot make sense of must come back
// as an error, never a silently-empty map, because a later caller turns "no
// coverage data" into a repo-wide finding.
func TestPythonParseCoverageUnparseableIsError(t *testing.T) {
	p := pyPlugin{}

	cases := map[string]string{
		"empty input":                                "",
		"blank/whitespace only":                      "   \n\n\t\n",
		"no JSON anywhere":                           "just some random text\nthat is not a coverage report\n",
		"last line is a JSON array, not an object":   "some preamble\n[1, 2, 3]",
		"missing meta key":                           `{"files": {"a.py": {"summary": {"covered_lines": 1}}}}`,
		"missing files key":                          `{"meta": {"format": 3}}`,
		"files entry with non-numeric covered_lines": `{"meta": {"format": 3}, "files": {"a.py": {"summary": {"covered_lines": "lots"}}}}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := p.ParseCoverage(in, "")
			if err == nil {
				t.Fatalf("ParseCoverage(%q) = %v, nil error; want an error", in, got)
			}
			if got != nil {
				t.Fatalf("ParseCoverage(%q) returned non-nil map %v alongside an error", in, got)
			}
		})
	}
}
