// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// TestCertifyRepoDryRunNeverWidensCandidacyByEvidence pins the design's Dry
// run honesty decision: --dry-run never runs the suite, so it must never
// widen candidacy by a measurement it does not have — mypkg/core.py (covered
// only by tests/test_smoke.py, no filename pairing) stays UNPAIRED, and the
// summary says outright that uncovered/evidence-paired is unknown without a
// run rather than silently printing zeros a reader could mistake for "the
// evidence measured this and found nothing".
func TestCertifyRepoDryRunNeverWidensCandidacyByEvidence(t *testing.T) {
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{}`), "--dry-run",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "0 candidate(s)") {
		t.Fatalf("a dry run must not widen candidacy by evidence it never collected — mypkg/core.py has no filename pairing:\n%s", s)
	}
	if strings.Contains(s, "paired by evidence") {
		t.Errorf("a dry run must print no evidence-paired candidate at all:\n%s", s)
	}
	if !strings.Contains(s, "uncovered/evidence-paired unknown without a run — pairing shown") {
		t.Errorf("missing the dry-run honesty line, verbatim:\n%s", s)
	}
}

// TestCertifyRepoEvidenceWidensCandidacyAndNamesUncoveredTruthfully is the
// non-dry-run counterpart: the SAME repo, run for real (models faked,
// critic off, so nothing is actually graded — mypkg/core.py has no --goals
// entry and lands ungoaled). mypkg/core.py has no filename pairing but IS
// executed by tests/test_smoke.py, so it becomes a candidate "paired by
// evidence"; mypkg/orphan.py is measured and executes under NO test, so it
// is excluded "uncovered — no test executes this file" rather than the
// name-shaped "no-paired-test" — and the summary counts all three lanes.
func TestCertifyRepoEvidenceWidensCandidacyAndNamesUncoveredTruthfully(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace,
	}, &out, &errb)
	if code != 0 && code != 1 {
		// COULD-NOT-GRADE (nothing audited: mypkg/core.py is ungoaled) exits
		// non-zero on some builds; either way stdout must carry the report.
		t.Logf("exit=%d stderr=%s", code, errb.String())
	}
	s := out.String()

	if !strings.Contains(s, "1 candidate(s)") {
		t.Fatalf("mypkg/core.py must become a candidate by evidence alone:\n%s", s)
	}
	if !strings.Contains(s, "mypkg/core.py paired by evidence: 1 covering test(s), authored test lands beside tests/test_smoke.py") {
		t.Errorf("missing the evidence-paired disclosure line, verbatim:\n%s", s)
	}
	if !strings.Contains(s, "excluded mypkg/orphan.py (uncovered — no test executes this file)") {
		t.Errorf("mypkg/orphan.py must be excluded under the truthful uncovered reason, not no-paired-test:\n%s", s)
	}
	if !strings.Contains(s, "evidence-paired 1 · name-paired 0 · uncovered 2") {
		t.Errorf("missing the candidacy summary line with the right tally:\n%s", s)
	}
}

// TestCertifyRepoWholeSuiteFallsBackToPairingOnlyCandidacy pins the design's
// Failure posture / evidence-absent decision for a run that deliberately
// collects no evidence: --whole-suite. mypkg/core.py must stay a
// non-candidate (pairing alone cannot find it), and the summary must say
// candidacy fell back to pairing only, not silently print zeros.
func TestCertifyRepoWholeSuiteFallsBackToPairingOnlyCandidacy(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)

	var out, errb bytes.Buffer
	runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace, "--whole-suite",
	}, &out, &errb)
	s := out.String()

	if !strings.Contains(s, "0 candidate(s)") {
		t.Fatalf("--whole-suite collects no evidence, so candidacy must stay pairing-only (0 candidates):\n%s", s)
	}
	if strings.Contains(s, "paired by evidence") {
		t.Errorf("--whole-suite must produce no evidence-paired candidate:\n%s", s)
	}
	// The parenthetical now carries the SAME reason the "selection:" line
	// above it already gave — "(no selection evidence)" alone is the
	// generic case; --whole-suite's own note is appended after the colon.
	if !strings.Contains(s, "pairing-only candidacy (no selection evidence: --whole-suite)") {
		t.Errorf("missing the evidence-absent fallback wording on the summary line:\n%s", s)
	}
}

// TestCertifyRepoMirroredFixtureCandidacyLineIsUnaffectedByEvidence is the
// mirrored-fixture pin at the integration level: a file that pairing ALREADY
// finds (mypkg/core.py, a sibling test_core.py) must report and count
// IDENTICALLY whether or not the scan also collects evidence that happens to
// cover it — evidence only ever WIDENS candidacy for files pairing missed,
// never changes how an already-paired one is reported.
func TestCertifyRepoMirroredFixtureCandidacyLineIsUnaffectedByEvidence(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.coverage.run]\nsource = [\"mypkg\"]\n")
	// NO mypkg/__init__.py: a second, UNRELATED python file in the fixture
	// would legitimately reclassify between the two runs on its OWN merits
	// (no-paired-test under --whole-suite vs the truthfully narrower
	// uncovered once evidence measures it at zero — the exact upgrade this
	// design exists to make), which would pollute a diff aimed at whether
	// mypkg/core.py's ALREADY-paired candidacy moved. Python 3 does not
	// require __init__.py for `from mypkg.core import used` to resolve.
	mustWrite(t, filepath.Join(root, "mypkg", "core.py"), "def used():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "mypkg", "test_core.py"),
		"from mypkg.core import used\n\n\ndef test_it():\n    assert used() == 1\n")

	args := func(extra ...string) []string {
		base := []string{
			"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
			"--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace,
		}
		return append(base, extra...)
	}

	var withoutEvidence, withEvidence bytes.Buffer
	var errb1, errb2 bytes.Buffer
	runCertifyRepo(args("--whole-suite"), &withoutEvidence, &errb1)
	runCertifyRepo(args(), &withEvidence, &errb2)

	// F5: DIFF the two runs' candidate output, rather than asserting
	// substrings independently in each -- a substring check on each run
	// separately cannot catch a case where BOTH runs drifted the same way
	// (a shared regression that still contains the magic words); only a
	// direct comparison of the two runs against each other can.
	before := candidacyLines(withoutEvidence.String())
	after := candidacyLines(withEvidence.String())
	if diff := diffLines(before, after); diff != "" {
		t.Fatalf("mypkg/core.py's candidacy output differs between --whole-suite (no evidence) and a normal run (evidence collected, but this file is ALREADY name-paired) -- it must be byte-identical:\n%s\nwithout evidence:\n%s\nwith evidence:\n%s",
			diff, withoutEvidence.String(), withEvidence.String())
	}
	if !strings.Contains(before, "1 candidate(s)") || !strings.Contains(before, "name-paired 1") {
		t.Fatalf("fixture is wrong: mypkg/core.py must be the one name-paired candidate:\n%s", withoutEvidence.String())
	}
	if strings.Contains(before, "paired by evidence") {
		t.Errorf("a name-paired candidate must never be disclosed as evidence-paired:\n%s", withoutEvidence.String())
	}
}

// candidacyLines extracts the report lines that describe CANDIDACY -- the
// count line, the per-language profile line, and any line naming
// mypkg/core.py -- joined back into one string so two runs' candidacy
// output can be diffed directly instead of each being substring-matched in
// isolation.
func candidacyLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.Contains(line, "candidate(s)"), strings.Contains(line, "mypkg/core.py"):
			out = append(out, line)
		case strings.Contains(line, "evidence-paired"):
			// The counts (evidence-paired N / name-paired M / uncovered K)
			// are what pins mypkg/core.py's classification; the trailing
			// " — <basis>" disclosure legitimately differs between the two
			// runs by design (one collected evidence, one did not — see the
			// design's evidence-absent fallback decision) and is not part
			// of what this test is pinning.
			if i := strings.Index(line, " — "); i >= 0 {
				line = line[:i]
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// diffLines returns a human-readable diff of two multi-line strings ("" when
// they are identical) -- a minimal line-by-line comparison, not a full LCS
// diff, but enough to point at exactly which candidacy line moved rather
// than dumping two whole reports for a human to eyeball.
func diffLines(a, b string) string {
	if a == b {
		return ""
	}
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	maxLen := len(al)
	if len(bl) > maxLen {
		maxLen = len(bl)
	}
	for i := 0; i < maxLen; i++ {
		var la, lb string
		if i < len(al) {
			la = al[i]
		}
		if i < len(bl) {
			lb = bl[i]
		}
		if la != lb {
			out = append(out, fmt.Sprintf("line %d:\n  - %s\n  + %s", i, la, lb))
		}
	}
	return strings.Join(out, "\n")
}

// TestCertifyRepoEvidenceOnlyCandidateActuallyGrades is F1's regression: the
// two existing evidence-candidacy e2e tests leave mypkg/core.py UNGOALED, so
// EmitJobs excludes it before a job is ever built — nothing exercises
// auditInputFor/prepareAuditJail for an evidence-only candidate at all. This
// test GOALS it and replays a recorded mutant set the covering test kills
// outright (Survivors=0), so grading completes with NO model call (mutant
// generation is replayed; the writer pool never engages a survivor-less
// file) — proving prepareAuditJail actually finds and runs
// tests/test_smoke.py as the dev test (j.CoveringTestPath, not j.TestPath,
// which stays empty by construction) instead of refusing with "no test
// found", the exact "measurement computed then discarded" bug this fixes.
func TestCertifyRepoEvidenceOnlyCandidateActuallyGrades(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	const core = "def used():\n    return 1\n"
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.coverage.run]\nsource = [\"mypkg\"]\n")
	mustWrite(t, filepath.Join(root, "mypkg", "__init__.py"), "")
	mustWrite(t, filepath.Join(root, "mypkg", "core.py"), core)
	mustWrite(t, filepath.Join(root, "tests", "test_smoke.py"),
		"from mypkg.core import used\n\n\ndef test_it():\n    assert used() == 1\n")

	setPath := filepath.Join(root, "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			// A whole-file mutant (Search empty — see RecordedMutant's doc):
			// used() now returns 2, which tests/test_smoke.py's own
			// assertion (used() == 1) fails on — killed by the DEV suite
			// alone, so Survivors=0 and the writer pool never runs, which is
			// what makes this whole audit reachable with no model call.
			"mypkg/core.py": {
				ParentSHA256: shaOf(core),
				Mutants:      []adequacy.RecordedMutant{{ID: "m1", Replace: "def used():\n    return 2\n"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{"mypkg/core.py": "used() must return exactly 1"}`),
		"--substrate", substrateWorkspace, "--mutants", setPath,
	}, &out, &errb)
	s := out.String()
	if code != 0 {
		t.Fatalf("exit=%d, want 0 for a fully-replayed, fully-killed audit: stdout=%s stderr=%s", code, s, errb.String())
	}

	if !strings.Contains(s, "mypkg/core.py paired by evidence: 1 covering test(s), authored test lands beside tests/test_smoke.py") {
		t.Fatalf("missing the evidence-paired disclosure line:\n%s", s)
	}
	if strings.Contains(s, "COULD-NOT-GRADE") {
		t.Fatalf("nothing was graded — the exact bug F1 fixes (a discarded CoveringTestPath makes prepareAuditJail refuse the job):\n%s\nstderr:\n%s", s, errb.String())
	}
	if strings.Contains(errb.String(), "no test found") || strings.Contains(errb.String(), "could not audit") {
		t.Fatalf("prepareAuditJail refused the job for lack of a test, despite the evidence naming one:\nstdout:\n%s\nstderr:\n%s", s, errb.String())
	}
	if !strings.Contains(s, "kill rate 1.00 over 1 audited file(s) (100% of 1 candidates)") {
		t.Errorf("want a clean 1.00 kill rate over the one evidence-only candidate, graded against its covering test:\n%s", s)
	}
}
