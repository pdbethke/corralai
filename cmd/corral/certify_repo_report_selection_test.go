// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

func TestPrintWeakFileNamesTheMeasurement(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.65, SelectionMethod: "coverage-context", SelectedTests: 14, SuiteTests: 1431})
	if !strings.Contains(b.String(), "graded by 14 of 1431 tests (coverage-context)") {
		t.Errorf("got %q", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "lib/a.rb", KillRate: 0.9, SelectionFallback: "no selector for ruby"})
	if !strings.Contains(b.String(), "graded by the whole suite (no selector for ruby)") {
		t.Errorf("got %q", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/u.py", Uncovered: true, ProvenMissed: 3, SelectionMethod: "coverage-context"})
	if !strings.Contains(b.String(), "[UNCOVERED — no test executes this file]") || strings.Contains(b.String(), "0.00") {
		t.Errorf("uncovered must withhold the rate: %q", b.String())
	}
	// The "which measurement is this" clause is printed on EVERY line — the
	// point of it is that a reader never has to infer the mode from an
	// absence. The uncovered case had no clause at all, so the one line
	// where the mode is most load-bearing was the one line that said nothing.
	if !strings.Contains(b.String(), "graded by the tests for this file — none execute it (coverage-context)") {
		t.Errorf("uncovered must still say which measurement it is: %q", b.String())
	}

	// THE RESIDUAL: a paired, import-only file (evidence recorded static
	// coverage but zero test contexts — e.g. a package __init__.py) must
	// NEVER print the word UNCOVERED, though its rate is withheld exactly
	// the same way. Reuses reposcan.ReasonImportOnly verbatim.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/__init__.py", Uncovered: true, ImportOnly: true, SelectionMethod: "coverage-context"})
	if strings.Contains(b.String(), "UNCOVERED") {
		t.Errorf("an import-only file must never print UNCOVERED: %q", b.String())
	}
	if !strings.Contains(b.String(), "  ["+reposcan.ReasonImportOnly+"]") {
		t.Errorf("missing the import-only marker, verbatim: %q", b.String())
	}
	if strings.Contains(b.String(), "0.00") {
		t.Errorf("import-only must also withhold the rate, never print 0.00: %q", b.String())
	}
	if !strings.Contains(b.String(), "withheld") {
		t.Errorf("import-only must withhold the rate, same as uncovered: %q", b.String())
	}
	if !strings.Contains(b.String(), "graded by the tests for this file — "+reposcan.ReasonImportOnly+" (coverage-context)") {
		t.Errorf("import-only must say which measurement it is, in its own words: %q", b.String())
	}

	// A per-mutant run whose every mutant was rejected by the compile gate
	// carries PerMutant with a spread of {0,0,0} — a reachable state, and
	// "0 to 0 per mutant, median 0" would report a range nothing measured as
	// though it had been.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/none.py", KillRate: 0, SelectionMethod: "coverage-lines", PerMutant: true})
	if strings.Contains(b.String(), "per mutant") {
		t.Errorf("an unmeasured spread must not be printed as a range: %q", b.String())
	}
	if !strings.Contains(b.String(), "(coverage-lines; no mutant graded)") {
		t.Errorf("a per-mutant run that graded nothing must say so: %q", b.String())
	}

	// The rule breakdown is what says how much of the narrowing was real.
	// A run reported as "coverage-lines" whose mutants were mostly graded by
	// the whole file selection (static, unreached, file) narrowed almost
	// nothing, and the line that prints only the spread lets that pass as a
	// per-mutant measurement.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/a.py", KillRate: 0.5, SelectionMethod: "coverage-lines",
		SelectedTests: 234, SuiteTests: 620,
		PerMutant: true, TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
		Rules: map[string]int{"lines": 30, "static": 4, "unreached": 1, "file": 2},
	})
	if !strings.Contains(b.String(), "(coverage-lines; 4 static, 1 unreached, 2 file)") {
		t.Errorf("the non-lines rules must be broken out, in a stable order: %q", b.String())
	}
	// Every mutant narrowed by its own lines: nothing to qualify, and the
	// parenthetical is exactly what it always was.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/a.py", KillRate: 0.5, SelectionMethod: "coverage-lines",
		SelectedTests: 234, SuiteTests: 620,
		PerMutant: true, TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
		Rules: map[string]int{"lines": 30},
	})
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if !strings.HasSuffix(lines[0], "(coverage-lines)") {
		t.Errorf("an all-lines run prints no breakdown: %q", b.String())
	}
}

func TestMinKillRateFailsAnUncoveredFile(t *testing.T) {
	min := 0.5
	// Audited: 1 so the Audited == 0 early return cannot answer this, and a
	// rate ABOVE the threshold so ONLY the Uncovered branch can fail it.
	r := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{{Path: "pkg/u.py", Uncovered: true, KillRate: 0.9}}}
	if code := repoScanExitCode(r, false, 0, &min, nil); code != 1 {
		t.Errorf("exit %d, want 1: an uncovered file under a --min-kill-rate gate is a failure, not a pass on a withheld number", code)
	}
}

// The same gate must fail an import-only file identically — it has no rate
// to satisfy the threshold either. Only the REPORTED WORDING differs
// elsewhere (printWeakFile, printRepoReport); the gate itself is unchanged.
func TestMinKillRateFailsAnImportOnlyFile(t *testing.T) {
	min := 0.5
	r := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{{Path: "pkg/__init__.py", Uncovered: true, ImportOnly: true, KillRate: 0.9}}}
	if code := repoScanExitCode(r, false, 0, &min, nil); code != 1 {
		t.Errorf("exit %d, want 1: an import-only file under a --min-kill-rate gate is a failure, not a pass on a withheld number", code)
	}
}

// THE RESIDUAL, at the repo-report level: printRepoReport's NO-GRADED-FILE
// and kill-rate summary lines must not call an all-import-only (or
// mixed) audit UNCOVERED either.
func TestPrintRepoReportDistinguishesImportOnlyInSummaryLines(t *testing.T) {
	// Every audited file import-only: the ORIGINAL "all N audited file(s)
	// are UNCOVERED" wording must not appear at all.
	var b bytes.Buffer
	r := reposcan.RepoReport{Audited: 2, GradedFiles: 0, UncoveredFiles: 2, ImportOnlyFiles: 2}
	printRepoReport(&b, r, false, nil, nil, nil, time.Time{})
	if strings.Contains(b.String(), "UNCOVERED") {
		t.Errorf("all-import-only audit must never print UNCOVERED: %q", b.String())
	}
	if !strings.Contains(b.String(), "NO GRADED FILE: all 2 audited file(s) were "+reposcan.ReasonImportOnly) {
		t.Errorf("missing the all-import-only NO GRADED FILE line, verbatim: %q", b.String())
	}

	// A genuinely all-uncovered audit (ImportOnlyFiles == 0) is BYTE
	// IDENTICAL to before this distinction existed.
	b.Reset()
	r = reposcan.RepoReport{Audited: 2, GradedFiles: 0, UncoveredFiles: 2, ImportOnlyFiles: 0}
	printRepoReport(&b, r, false, nil, nil, nil, time.Time{})
	if !strings.Contains(b.String(), "NO GRADED FILE: all 2 audited file(s) are UNCOVERED — no test executes them, so no kill rate was measured") {
		t.Errorf("a genuinely all-uncovered audit must keep the exact original wording: %q", b.String())
	}

	// A mix: some graded, some genuinely uncovered, some import-only — the
	// %d UNCOVERED in the summary line must count ONLY the genuinely dead
	// ones.
	b.Reset()
	r = reposcan.RepoReport{Audited: 4, GradedFiles: 2, KillRate: 0.5, UncoveredFiles: 2, ImportOnlyFiles: 1, Candidates: 4}
	printRepoReport(&b, r, false, nil, nil, nil, time.Time{})
	if !strings.Contains(b.String(), "1 UNCOVERED") {
		t.Errorf("mixed audit: the UNCOVERED count must exclude the import-only file, got: %q", b.String())
	}
	if !strings.Contains(b.String(), "1 "+reposcan.ReasonImportOnly) {
		t.Errorf("mixed audit: missing the import-only count, verbatim: %q", b.String())
	}
}

// TestPrintWeakFileNamesThePerMutantMeasurement pins the disclosure at the
// grain the grading now happens: when each mutant was graded by the tests
// that reach ITS lines, "234 of 620 tests" is the file's UNION, not what any
// one mutant faced. A line that printed only the union would let a reader
// take 234 for the number every mutant survived.
func TestPrintWeakFileNamesThePerMutantMeasurement(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "src/flask/cli.py", KillRate: 0.65,
		SelectionMethod: "coverage-lines", SelectedTests: 234, SuiteTests: 620,
		PerMutant: true, TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
	})
	want := "graded by 234 of 620 tests — 3 to 41 per mutant, median 9 (coverage-lines)"
	if !strings.Contains(b.String(), want) {
		t.Errorf("got %q, want it to contain %q", b.String(), want)
	}
	// The method comes from the verdict, never from a hardcoded string here:
	// a per-mutant run's selection IS "coverage-lines", and a printer that
	// stamped the label itself would keep saying so after the measurement
	// changed underneath it.
	if strings.Contains(b.String(), "coverage-context") {
		t.Errorf("the method must be the verdict's, not invented: %q", b.String())
	}

	// Without PerMutant the line is byte-identical to what it has always
	// been — the per-mutant clause is an ADDITION, not a rewrite.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.65, SelectionMethod: "coverage-context", SelectedTests: 14, SuiteTests: 1431})
	if !strings.Contains(b.String(), "graded by 14 of 1431 tests (coverage-context)") {
		t.Errorf("got %q", b.String())
	}
	if strings.Contains(b.String(), "per mutant") {
		t.Errorf("a run that did NOT grade per mutant must not claim it did: %q", b.String())
	}
}

func TestPrintWeakFileSaysProvenByTheAuthoredTestAlone(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.55, Survivors: 18, ProvenMissed: 18,
		SelectionMethod: "coverage-lines", SelectedTests: 234, SuiteTests: 620, ProvenByAuthoredAlone: true})
	if !strings.Contains(b.String(), "proven by the authored test alone") {
		t.Errorf("got %q", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/b.py", KillRate: 0.55, Survivors: 18, ProvenMissed: 0,
		SelectionMethod: "coverage-lines", SelectedTests: 234, SuiteTests: 620, ProvenByAuthoredAlone: true})
	if strings.Contains(b.String(), "proven by the authored test alone") {
		t.Errorf("nothing was proven; the clause must not print: %q", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/c.py", KillRate: 0.55, Survivors: 18, ProvenMissed: 3, SelectionFallback: "--whole-suite"})
	if strings.Contains(b.String(), "authored test alone") {
		t.Errorf("a whole-suite run proves the old way: %q", b.String())
	}
}

// TestPrintWeakFileNamesTheConcurrency pins the report's half of "every
// reader says how many trees scored the file, or why one". The wording must
// be the SAME as noteConcurrency's live progress line — both go through
// concurrencyDisclosure — so an operator reading the report after the fact
// sees exactly what they saw scroll past during the run.
func TestPrintWeakFileNamesTheConcurrency(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.65, Trees: 6})
	if !strings.Contains(b.String(), "   concurrency: 6 trees (baseline passed under 6)") {
		t.Errorf("got %q", b.String())
	}

	// The dep dirs shared by every tree ride on the SAME line, through the
	// same helper, so the live progress, the report and `corral scans show`
	// cannot disagree about what was shared.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/shared.py", KillRate: 0.65, Trees: 6, SharedDirs: []string{".venv"}})
	if !strings.Contains(b.String(), "   concurrency: 6 trees (baseline passed under 6; shared: .venv)") {
		t.Errorf("got %q", b.String())
	}

	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/b.py", KillRate: 0.65, Trees: 1,
		ConcurrencyNote: "suite is not concurrency-safe: baseline failed under 3"})
	if !strings.Contains(b.String(), "   concurrency: 1 (suite is not concurrency-safe: baseline failed under 3)") {
		t.Errorf("got %q", b.String())
	}

	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/c.py", KillRate: 0.65, Trees: 1})
	if !strings.Contains(b.String(), "   concurrency: 1") {
		t.Errorf("got %q", b.String())
	}
	if strings.Contains(b.String(), "   concurrency: 1 (") {
		t.Errorf("no note must print bare '1', not an empty parenthetical: %q", b.String())
	}

	// Trees 0 is "not recorded", and the report says nothing rather than
	// inventing a "1". This is the shape a jail-substrate file has (it builds
	// no trees at all) and the shape a verdict served from a cache row
	// written before this branch has — the same silence noteConcurrency
	// already keeps live.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/d.py", KillRate: 0.65})
	if strings.Contains(b.String(), "concurrency:") {
		t.Errorf("an unrecorded concurrency must print NO line: %q", b.String())
	}
}
