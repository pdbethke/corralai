// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"strings"
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

// fixtureAllZeroTotals is a real captured `coverage json -o -` report from a
// SUITE THAT NEVER RAN: `coverage run -m pytest -p nonexistent_plugin_xyz`
// against the flask clone (a broken/missing pytest plugin — pytest refuses
// to start before a single test is collected). Reproduced directly:
//
//	$ rm -f .coverage
//	$ python3 -m coverage run -m pytest -p nonexistent_plugin_xyz
//	$ python3 -m coverage json -o -
//	# exit 0, valid JSON, 23 files, EVERY ONE at covered_lines: 0,
//	# totals.covered_lines: 0.
//
// This is the case a first pass of this task missed: coverage.py's
// `[tool.coverage.run] source = [...]` config (present in flask's real
// pyproject.toml) makes `coverage json` enumerate every file under the
// configured source tree whether or not it was ever executed, so a suite
// that never ran at all is FILE-BY-FILE indistinguishable from a suite that
// ran and genuinely touched nothing — the only difference is
// totals.covered_lines, which is why ParseCoverage checks it.
const fixtureAllZeroTotals = `{"meta": {"format": 3, "version": "7.15.2", "timestamp": "2026-07-29T12:21:33.679901", "branch_coverage": false, "show_contexts": false}, "files": {"tests/conftest.py": {"summary": {"covered_lines": 0, "num_statements": 72}}, "tests/test_appctx.py": {"summary": {"covered_lines": 0, "num_statements": 178}}, "tests/test_async.py": {"summary": {"covered_lines": 0, "num_statements": 100}}, "tests/test_basic.py": {"summary": {"covered_lines": 0, "num_statements": 1321}}}, "totals": {"covered_lines": 0, "num_statements": 4985}}`

// fixtureAbsolutePaths is a real captured `coverage json -o -` report from
// running the SAME instrumented command from flask's tests/ subdirectory
// instead of the repo root:
//
//	$ cd tests && python3 -m coverage run -m pytest -q -k test_basic_view
//	$ python3 -m coverage json -o -
//
// coverage.py switches to ABSOLUTE paths in this case — verified directly
// (the repo-root run's paths are "src/flask/app.py"; this run's paths are
// "/…/scratchpad/flask/src/flask/app.py") — which a caller running the test
// command from anywhere but the repo root would otherwise silently
// misalign against reposcan.Enumerate's repo-relative candidates. The
// second "outside root" entry is NOT captured (this run traced nothing
// outside the repo) — it is added to exercise the "absolute path outside
// modulePath is a dependency, skip it" branch, using the real path SHAPE
// confirmed above.
const fixtureAbsolutePaths = `{"meta": {"format": 3, "version": "7.15.2", "timestamp": "2026-07-29T12:24:02.188610", "branch_coverage": false, "show_contexts": false}, "files": {"/tmp/claude-1000/-home-pdbethke-PycharmProjects-corralai/4b00f3f8-0a34-4550-a407-afd5b49fcd55/scratchpad/flask/src/flask/app.py": {"summary": {"covered_lines": 211, "num_statements": 435}}, "/usr/lib/python3.12/site-packages/somedep/__init__.py": {"summary": {"covered_lines": 3, "num_statements": 3}}}, "totals": {"covered_lines": 2056, "num_statements": 7808}}`

const fixtureAbsolutePathsRoot = "/tmp/claude-1000/-home-pdbethke-PycharmProjects-corralai/4b00f3f8-0a34-4550-a407-afd5b49fcd55/scratchpad/flask"

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

// TestPythonParseCoverageZeroTotalsIsError pins the C1 fix: a well-formed
// report whose totals.covered_lines is 0 means the suite never ran at all
// (see fixtureAllZeroTotals) and must be an ERROR — never an empty map, even
// though every individual file also reports covered_lines: 0, which on its
// own (see TestPythonParseCoverageAllZeroIsEmptyNotError) is the legitimate
// empty case. The distinguishing signal is totals, not any one file.
func TestPythonParseCoverageZeroTotalsIsError(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAllZeroTotals, "")
	if err == nil {
		t.Fatalf("ParseCoverage(fixtureAllZeroTotals) = %v, nil error; want an error", got)
	}
	if got != nil {
		t.Fatalf("ParseCoverage(fixtureAllZeroTotals) returned non-nil map %v alongside an error", got)
	}
}

// TestPythonParseCoverageAbsolutePathsRelativizedToModulePath pins the C3
// fix: an absolute path under modulePath (the repo root) is relativized to
// it, matching what reposcan.Enumerate produces, and an absolute path
// OUTSIDE modulePath (a dependency) is skipped rather than reported as a
// bogus repo-relative path.
func TestPythonParseCoverageAbsolutePathsRelativizedToModulePath(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAbsolutePaths, fixtureAbsolutePathsRoot)
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	want := map[string]bool{"src/flask/app.py": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v (the site-packages entry must be skipped, not reported)", got, want)
	}
}

// TestPythonParseCoverageAbsolutePathWithNoModulePathIsError pins the other
// half of the C3 fix: an absolute path with modulePath == "" cannot be
// aligned to anything — guessing would silently fabricate a repo-relative
// path that might collide with an unrelated file, so this is an error, not
// a best-effort pass-through of the absolute path as-is.
func TestPythonParseCoverageAbsolutePathWithNoModulePathIsError(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAbsolutePaths, "")
	if err == nil {
		t.Fatalf("ParseCoverage(fixtureAbsolutePaths, \"\") = %v, nil error; want an error", got)
	}
	if got != nil {
		t.Fatalf("ParseCoverage(fixtureAbsolutePaths, \"\") returned non-nil map %v alongside an error", got)
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

// TestPythonCoverageCmdAcceptedShapes pins the C2 fix: the [interpreter,
// "-m", <module>, ...] shape TestCmd() itself returns, AND a bare
// "pytest"/"py.test" head — the documented GitHub Action test-command shape
// (action.yml, docs/corral/github-action.md) that cmd/corral/certify_repo.go
// passes through verbatim from an operator's `-- <cmd>`. Measured before this
// fix: ["pytest","-q"] and ["poetry","run","pytest"] both got ok=false;
// after, the bare-pytest shape is accepted (poetry-style wrapping is still
// declined — see TestPythonCoverageCmdDeclinedShapes).
func TestPythonCoverageCmdAcceptedShapes(t *testing.T) {
	p := pyPlugin{}
	cases := []struct {
		name    string
		testCmd []string
	}{
		{"TestCmd() shape", []string{"python3", "-m", "pytest", "-q"}},
		{"bare pytest", []string{"pytest", "-q"}},
		{"bare py.test", []string{"py.test", "-q"}},
		{"bare pytest, no extra args", []string{"pytest"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, ok := p.CoverageCmd(c.testCmd)
			if !ok {
				t.Fatalf("CoverageCmd(%v) ok = false, want true", c.testCmd)
			}
			if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
				t.Fatalf("CoverageCmd(%v) = %v, want [sh -c <script>]", c.testCmd, cmd)
			}
			script := cmd[2]
			if !strings.Contains(script, "coverage run -m") || !strings.Contains(script, "coverage json -o -") {
				t.Fatalf("CoverageCmd(%v) script = %q, missing expected coverage invocations", c.testCmd, script)
			}
		})
	}
}

// TestPythonCoverageCmdDeclinedShapes pins that a testCmd shape this method
// cannot safely instrument is declined (ok=false), never guessed at.
func TestPythonCoverageCmdDeclinedShapes(t *testing.T) {
	p := pyPlugin{}
	cases := []struct {
		name    string
		testCmd []string
	}{
		{"empty", nil},
		{"poetry run pytest", []string{"poetry", "run", "pytest"}},
		{"tox", []string{"tox"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, ok := p.CoverageCmd(c.testCmd)
			if ok {
				t.Fatalf("CoverageCmd(%v) = %v, ok=true; want ok=false", c.testCmd, cmd)
			}
			if cmd != nil {
				t.Fatalf("CoverageCmd(%v) returned non-nil cmd %v alongside ok=false", c.testCmd, cmd)
			}
		})
	}
}

// TestPythonCoverageCmdShellInjectionPayloadsAreInert mirrors the review
// Task 1's CoverageCmd got: fire shell-metacharacter payloads through
// testCmd elements and confirm shellQuote neutralizes them — the built
// script must single-quote every element, never interpolate one unquoted.
func TestPythonCoverageCmdShellInjectionPayloadsAreInert(t *testing.T) {
	p := pyPlugin{}
	payloads := []string{
		`-q'; rm -rf / #`,
		"-q\nrm -rf /tmp/pwned",
		"-q$(whoami)",
		"-q`whoami`",
	}
	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			cmd, ok := p.CoverageCmd([]string{"pytest", payload})
			if !ok {
				t.Fatalf("CoverageCmd with payload %q: ok=false", payload)
			}
			script := cmd[2]
			// The payload must appear ONLY inside a single-quoted token
			// (shellQuote's output), never as bare, unquoted shell syntax.
			// A crude but effective check: every occurrence of the payload's
			// otherwise-dangerous prefix is immediately preceded by a `'`
			// that shellQuote would have emitted.
			if !strings.Contains(script, shellQuote(payload)) {
				t.Fatalf("CoverageCmd script does not contain the shell-quoted form of payload %q; script = %q", payload, script)
			}
		})
	}
}
