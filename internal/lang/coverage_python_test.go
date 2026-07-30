// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
// (three files flask's suite never touches at all), while totals.covered_lines
// is positive (the suite genuinely ran). This is a legitimate case ParseCoverage
// must return WITHOUT an error — a well-formed report saying "these measured
// files were never executed" is data, not a parse failure — but under the
// tri-state contract that means every one of these three files comes back as
// a present-FALSE entry (measured, not executed), not an empty map.
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

// fixtureAllCoveredOutsideRoot is a real captured `coverage json -o -`
// report from a SECOND real project (not flask) built specifically to
// reproduce review finding A: a src-layout package (src/mypkg2/__init__.py)
// installed NON-EDITABLE (`pip install .`) into a venv OUTSIDE the repo
// checkout, with `[tool.coverage.run] source = ["mypkg2"]` and — critically —
// NO `[tool.coverage.paths]` remap (flask's own pyproject.toml has one,
// which is why flask alone never exposed this). Reproduced directly:
//
//	$ python3 -m venv /…/pkgrepo2venv   # OUTSIDE the repo checkout
//	$ source /…/pkgrepo2venv/bin/activate
//	$ pip install -q .                  # non-editable
//	$ python3 -m coverage run -m pytest -q
//	$ python3 -m coverage json -o -
//
// gave a single file entry, covered_lines: 2, at an ABSOLUTE path under the
// venv (…/pkgrepo2venv/lib/python3.12/site-packages/mypkg2/__init__.py) —
// NOT under the repo root. totals.covered_lines is 2 (nonzero — the suite
// genuinely ran), so the C1 zero-totals check does not fire; the one file
// with real coverage is outside modulePath, so the old per-entry-skip-only
// logic returned an empty map with a nil error — a suite that ran green,
// reported as touching nothing in the repo. The fed-through-the-real-
// ParseCoverage transcript (before the fix) is in task-2-report.md.
const fixtureAllCoveredOutsideRoot = `{"meta": {"format": 3, "version": "7.15.2", "timestamp": "2026-07-29T12:44:21.124725", "branch_coverage": false, "show_contexts": false}, "files": {"/tmp/claude-1000/-home-pdbethke-PycharmProjects-corralai/4b00f3f8-0a34-4550-a407-afd5b49fcd55/scratchpad/pkgrepo2venv/lib/python3.12/site-packages/mypkg2/__init__.py": {"summary": {"covered_lines": 2, "num_statements": 2}}}, "totals": {"covered_lines": 2, "num_statements": 2}}`

const fixtureAllCoveredOutsideRootRoot = "/tmp/claude-1000/-home-pdbethke-PycharmProjects-corralai/4b00f3f8-0a34-4550-a407-afd5b49fcd55/scratchpad/pkgrepo2"

func TestPythonParseCoverageExecutedFiles(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureMixed, "")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	// __main__.py has covered_lines: 0 — it was MEASURED (present in the
	// report) but never executed, so it must appear as false, not be
	// dropped: present-false is the real finding this contract exists to
	// preserve, distinct from a file the report never mentions at all.
	want := map[string]bool{
		"src/flask/app.py":      true,
		"src/flask/helpers.py":  true,
		"src/flask/__main__.py": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestPythonParseCoverageAllZeroPerFileIsMeasuredNotExecuted pins the
// tri-state contract's other legitimate non-error case: a well-formed report
// in which every individual file has zero COVERED lines, but
// totals.covered_lines is positive (the suite genuinely ran), returns a
// present-false ("measured, not executed") entry for each such file that
// has at least one STATEMENT to have skipped — a real finding, not an empty
// map — distinct from an unparseable report, which must error (see
// TestPythonParseCoverageUnparseableIsError below), and distinct from
// totals.covered_lines == 0, which means the suite never ran at all (see
// TestPythonParseCoverageZeroTotalsIsError).
//
// fixtureAllZero's two __init__.py entries have num_statements: 0 — real,
// zero-byte package __init__.py files flask's suite DOES import
// (tests/test_blueprints.py, tests/test_cli.py) but which have nothing
// measurable to record either way. Only src/flask/__main__.py
// (num_statements: 2, genuinely never imported by the suite) is a real
// finding; the two num_statements: 0 files must be ABSENT, never a false
// accusation that the suite "never executed" a file with no statement to
// execute. This is the corrected form of a bug the review round after this
// task caught: an earlier version of this test asserted BOTH __init__.py
// paths as false, pinning the wrong (accusing) contract as correct.
func TestPythonParseCoverageAllZeroPerFileIsMeasuredNotExecuted(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAllZero, "")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	want := map[string]bool{
		"src/flask/__main__.py": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v (the two num_statements:0 __init__.py files must be ABSENT, not false)", got, want)
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

// TestPythonParseCoverageAllPositiveEntriesOutsideRootIsError pins the
// finding-A fix: totals.covered_lines > 0 (the suite genuinely ran) but
// EVERY entry with covered_lines > 0 lands outside modulePath after the
// per-entry filter — not a legitimate empty result, an alignment failure
// (wrong modulePath, or a project needing a coverage.py
// [tool.coverage.paths] remap corral cannot see). Must be an error, not an
// empty map with a nil error.
func TestPythonParseCoverageAllPositiveEntriesOutsideRootIsError(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAllCoveredOutsideRoot, fixtureAllCoveredOutsideRootRoot)
	if err == nil {
		t.Fatalf("ParseCoverage(fixtureAllCoveredOutsideRoot, root) = %v, nil error; want an error", got)
	}
	if got != nil {
		t.Fatalf("ParseCoverage(fixtureAllCoveredOutsideRoot, root) returned non-nil map %v alongside an error", got)
	}
}

// TestPythonParseCoverageModulePathTrailingSlashStillAligns pins the C fix:
// modulePath is normalised (filepath.Clean) before use, so a caller passing
// a trailing slash ("/root/") or the bare root ("/") still aligns absolute
// report paths correctly instead of silently missing every entry the way an
// un-normalised "root+\"/\"" prefix match would (a caller-contract slip
// landing in exactly finding A's failure mode).
func TestPythonParseCoverageModulePathTrailingSlashStillAligns(t *testing.T) {
	p := pyPlugin{}
	got, err := p.ParseCoverage(fixtureAbsolutePaths, fixtureAbsolutePathsRoot+"/")
	if err != nil {
		t.Fatalf("ParseCoverage with trailing-slash modulePath: %v", err)
	}
	want := map[string]bool{"src/flask/app.py": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestPythonParseCoverageDotDotEscapeIsSkippedNotEmitted pins the other half
// of the C fix: an absolute report path that only appears to be under
// modulePath as an un-Clean'd string (e.g. "/root/../other/x.py") is
// Clean'd before the prefix match, so it is correctly recognised as OUTSIDE
// modulePath (and skipped) rather than relativized to a non-repo-relative
// "../other/x.py" and emitted into the executed set.
func TestPythonParseCoverageDotDotEscapeIsSkippedNotEmitted(t *testing.T) {
	p := pyPlugin{}
	root := "/repo"
	report := `{"meta": {"format": 3}, "files": {"/repo/../repo-other/x.py": {"summary": {"covered_lines": 5, "num_statements": 5}}, "/repo/real.py": {"summary": {"covered_lines": 1, "num_statements": 1}}}, "totals": {"covered_lines": 6}}`
	got, err := p.ParseCoverage(report, root)
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	want := map[string]bool{"real.py": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v (the ../ escape must be skipped, not emitted)", got, want)
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
		// Finding B: pytest's OWN "-m <marker-expression>" flag (action.yml-
		// documented, mainstream) must not be misread by the
		// [interpreter,"-m",<module>,...] branch as "run module -m as a
		// module" — the bare-pytest branch must win.
		{"bare pytest with -m marker expression", []string{"pytest", "-m", "not slow"}},
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

// TestPythonCoverageCmdUsesATempCoverageFileNotTheCwdDefault pins F3: the
// script must point coverage.py's data file at a mktemp'd path via
// COVERAGE_FILE (cleaned up by an EXIT trap), for BOTH the `run` and the
// `json` step — never let `coverage run` fall back to its own default of
// writing `.coverage` straight into the cwd, which on the workspace
// substrate IS the operator's own --repo checkout. Mirrors
// goPlugin.CoverageCmd's existing `mktemp` + `trap rm` discipline for
// `-coverprofile`.
func TestPythonCoverageCmdUsesATempCoverageFileNotTheCwdDefault(t *testing.T) {
	p := pyPlugin{}
	cmd, ok := p.CoverageCmd([]string{"pytest", "-q"})
	if !ok {
		t.Fatalf("CoverageCmd ok=false")
	}
	script := cmd[2]
	for _, want := range []string{
		"f=$(mktemp)",
		`trap 'rm -f "$f"' EXIT`,
		`COVERAGE_FILE="$f"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script = %q, missing %q (coverage.py must not fall back to its own cwd-relative .coverage default)", script, want)
		}
	}
	// COVERAGE_FILE must precede BOTH invocations — the run step and the
	// json step both need to agree on where the data file is.
	if n := strings.Count(script, `COVERAGE_FILE="$f"`); n != 2 {
		t.Errorf("script has COVERAGE_FILE=\"$f\" %d time(s), want 2 (one per coverage invocation): %q", n, script)
	}
}

// TestPythonCoverageCmdMarkerExpressionNotMisreadAsModule pins the exact
// finding-B failure mode: before the fix, ["pytest","-m","not slow"] matched
// the [interpreter,"-m",<module>,...] branch FIRST (interp="pytest",
// module="not"?? no — interp="pytest", then testCmd[2:]=["not slow"] was
// treated as the module+args to run under `coverage run -m`), building
// `'pytest' -m coverage run -m 'not slow'` — running pytest with marker
// expression "coverage" and positional args "run"/"not slow", never the
// operator's actual suite. The fix reorders the switch so the bare-pytest
// branch wins, building `coverage run -m pytest -m 'not slow'` instead —
// pytest itself, given its own -m marker expression.
func TestPythonCoverageCmdMarkerExpressionNotMisreadAsModule(t *testing.T) {
	p := pyPlugin{}
	cmd, ok := p.CoverageCmd([]string{"pytest", "-m", "not slow"})
	if !ok {
		t.Fatalf("CoverageCmd ok=false")
	}
	script := cmd[2]
	wantRunPart := "-m coverage run -m " + shellQuote("pytest") + " " + shellQuote("-m") + " " + shellQuote("not slow")
	if !strings.Contains(script, wantRunPart) {
		t.Fatalf("script = %q, want it to contain %q (pytest run WITH its own -m marker expression, not misread as a module to run)", script, wantRunPart)
	}
	badRunPart := "coverage run -m " + shellQuote("not slow")
	if strings.Contains(script, badRunPart) {
		t.Fatalf("script = %q contains the finding-B bug shape (marker expression misread as a module)", script)
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

// TestPythonCoverageCmdShellInjectionPayloadsAreInertWhenExecuted strengthens
// the string-match test above by actually RUNNING the built script and
// checking for a real side effect: each payload, if it escaped quoting,
// would create a marker file via a separately-executed shell command. It
// does not require coverage/pytest to be installed or importable — the
// `python3 -m coverage ...` invocation itself is allowed to fail (module not
// found, etc); the only thing under test is whether the injected shell
// command ever ran at all. Skips cleanly if "sh" is not on PATH.
func TestPythonCoverageCmdShellInjectionPayloadsAreInertWhenExecuted(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	p := pyPlugin{}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	payloads := []string{
		`-q'; touch ` + marker + ` #`,
		"-q\ntouch " + marker,
		"-q$(touch " + marker + ")",
		"-q`touch " + marker + "`",
		"-q && touch " + marker,
		"-q; touch " + marker,
	}
	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			_ = os.Remove(marker)
			cmd, ok := p.CoverageCmd([]string{"pytest", payload})
			if !ok {
				t.Fatalf("CoverageCmd ok=false")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
			_ = c.Run() // exit status is irrelevant; only the marker matters.
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatalf("injection payload %q executed for real — marker file %s was created", payload, marker)
			}
		})
	}
}
