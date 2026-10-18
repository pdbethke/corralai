// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestScansShowEvidence_NeverClaimsASoundGradeForAnUnsoundRun fixes a false
// claim this command shipped with: the --evidence block inferred "graded
// soundly" from an EMPTY proven-id list alone, without consulting the
// diagnosis flags. So a run marked [TEST UNSOUND] — a test that failed on
// unmutated code and scored nothing at all — was reported as having "graded
// soundly against N survivors and proved none of them", which is the opposite
// of what happened.
//
// Caught on the first real use: a gemini-3.6-flash run of pallets/flask whose
// authored test failed on clean code (CompliantPass=false, Total=0) printed
// exactly that sentence about 17 survivors it had never actually graded.
//
// The table's NOTE column already got this right (scanFileNote consults the
// flags); only this block did not — the same value rendered two ways, one of
// them wrong.
func TestScansShowEvidence_NeverClaimsASoundGradeForAnUnsoundRun(t *testing.T) {
	const src = "def test_authored():\n    assert True\n"
	for _, c := range []struct {
		name       string
		file       scanstore.File
		wantSubstr string
		wantAbsent string
	}{{
		name: "unsound — failed on clean code, graded nothing",
		file: scanstore.File{
			Path: "src/flask/cli.py", Disposition: "audited", Survivors: 17,
			ProvenMissed: 0, PoolTestUnsound: true, AuthoredTest: src,
		},
		wantSubstr: "never genuinely graded",
		wantAbsent: "graded soundly",
	}, {
		name: "writer failed — no compiling test at all",
		file: scanstore.File{
			Path: "a.py", Disposition: "audited", Survivors: 4,
			ProvenMissed: 0, TestWriterFailed: true, AuthoredTest: src,
		},
		wantSubstr: "did not compile",
		wantAbsent: "graded soundly",
	}, {
		name: "genuine tried-and-missed — this one DID grade soundly",
		file: scanstore.File{
			Path: "b.py", Disposition: "audited", Survivors: 10,
			ProvenMissed: 0, AuthoredTest: src,
		},
		wantSubstr: "graded soundly",
	}, {
		name: "proven — names the ids it killed",
		file: scanstore.File{
			Path: "c.py", Disposition: "audited", Survivors: 3,
			ProvenMissed: 2, ProvenMutantIDs: "m1,m3", AuthoredTest: src,
		},
		wantSubstr: "m1,m3",
		wantAbsent: "graded soundly against",
	}} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeScansReader{files: []scanstore.File{c.file}}
			var out, errOut bytes.Buffer
			if code := runScans([]string{"show", "1", "--evidence"}, openFake(r), &out, &errOut); code != 0 {
				t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
			}
			got := out.String()
			if !strings.Contains(got, c.wantSubstr) {
				t.Errorf("evidence block must contain %q, got:\n%s", c.wantSubstr, got)
			}
			if c.wantAbsent != "" && strings.Contains(got, c.wantAbsent) {
				t.Errorf("evidence block must NOT claim %q here — it asserts the opposite of what happened:\n%s", c.wantAbsent, got)
			}
			// The authored test itself is printed in every case: retaining it
			// for exactly the runs that proved nothing is why it is stored.
			if !strings.Contains(got, src) {
				t.Errorf("the authored test must always be printed:\n%s", got)
			}
		})
	}
}

// TestScansShowEvidence_CarriesTheTogetherOutcome closes a gap an outside
// review (Codex, 2026-10-18) reproduced: the live report warned that the
// proven tests FAIL when run together, the stored verdict kept that result,
// and `scans show --evidence` printed the combined test with no warning — so
// a reader retrieving the evidence later could adopt a suite already known
// to fail as one. The reader must say what the live report said, through the
// real conversion (buildScanFileRows) and the real reader (runScans), and
// must keep "not checked" distinct from "passed".
func TestScansShowEvidence_CarriesTheTogetherOutcome(t *testing.T) {
	const failure = "FAILED: shared state makes the combined file fail"
	const authored = "def test_one(): assert True\ndef test_two(): assert True\n"
	for _, c := range []struct {
		name     string
		together string
		output   string
		want     []string
		absent   []string
	}{
		{name: "fail", together: advpool.AuthoredTogetherFail, output: failure,
			want: []string{"FAIL when run together", failure}},
		{name: "pass", together: advpool.AuthoredTogetherPass,
			want: []string{"also pass when run together"}, absent: []string{"FAIL when run together"}},
		{name: "not checked",
			absent: []string{"run together"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rows := buildScanFileRows([]reposcan.FileResult{{
				Job: reposcan.Job{Path: "a.py", Lang: "python"}, Gradable: true,
				Verdict: advpool.Verdict{
					Status: "needs-review", Lang: "python", Survivors: 2,
					MutantsTotal: 8, DevKillRate: .75,
					ProvenMissed: 2, ProvenMutantIDs: []string{"m1", "m2"},
					AuthoredTest: authored, AuthoredTogether: c.together,
					AuthoredTogetherOutput: c.output,
				},
			}}, nil, reposcan.CoverageMap{}, "", t.TempDir(), &bytes.Buffer{})
			var out, errOut bytes.Buffer
			if code := runScans([]string{"show", "1", "--evidence"}, openFake(&fakeScansReader{files: rows}), &out, &errOut); code != 0 {
				t.Fatalf("show exit %d: %s", code, errOut.String())
			}
			s := out.String()
			if !strings.Contains(s, authored) {
				t.Fatalf("evidence lost the authored test:\n%s", s)
			}
			for _, w := range c.want {
				if !strings.Contains(s, w) {
					t.Errorf("evidence lacks %q:\n%s", w, s)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(s, a) {
					t.Errorf("evidence must not say %q:\n%s", a, s)
				}
			}
		})
	}
}
