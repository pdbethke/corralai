// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

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
