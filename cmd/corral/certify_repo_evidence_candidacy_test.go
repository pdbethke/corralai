// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(s, "pairing-only candidacy (no selection evidence)") {
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
	mustWrite(t, filepath.Join(root, "mypkg", "__init__.py"), "")
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

	for _, s := range []string{withoutEvidence.String(), withEvidence.String()} {
		if !strings.Contains(s, "1 candidate(s)") {
			t.Fatalf("mypkg/core.py is name-paired and must be the ONE candidate regardless of evidence:\n%s", s)
		}
		if strings.Contains(s, "paired by evidence") {
			t.Errorf("a name-paired candidate must never be disclosed as evidence-paired:\n%s", s)
		}
		if !strings.Contains(s, "name-paired 1") {
			t.Errorf("must count as name-paired in both runs:\n%s", s)
		}
	}
}
