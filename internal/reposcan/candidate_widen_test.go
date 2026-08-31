// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func widenFixtureIndex(t *testing.T, coverage map[string]lang.FileCoverage) (EvidenceIndex, bool) {
	t.Helper()
	sel := fakeSelector{index: coverage}
	return ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, sel)
}

// The design's headline fixture: utils.py has no filename pairing at all,
// but test_api.py's evidence proves it covers it — utils.py must become a
// candidate, "paired by evidence".
func TestWidenCandidacyPromotesAnUnpairedFileTheEvidenceCovers(t *testing.T) {
	cands := []Candidate{{Path: "pkg/api.py", TestPath: "tests/test_api.py", Lang: "python"}}
	excl := []Exclusion{{Path: "pkg/utils.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/utils.py": {Tests: map[string]int{"tests/test_api.py::test_a": 4}},
	})

	gotCands, gotExcl, nPromoted := WidenCandidacyByEvidence(cands, excl, idx, ok)

	if len(gotExcl) != 0 {
		t.Fatalf("excl = %+v, want utils.py promoted out of the exclusion list", gotExcl)
	}
	if len(gotCands) != 2 {
		t.Fatalf("cands = %+v, want 2 (the original pairing plus the evidence-only promotion)", gotCands)
	}
	if nPromoted != 1 {
		t.Errorf("promoted = %d, want 1", nPromoted)
	}
	var promoted *Candidate
	for i := range gotCands {
		if gotCands[i].Path == "pkg/utils.py" {
			promoted = &gotCands[i]
		}
	}
	if promoted == nil {
		t.Fatal("pkg/utils.py did not become a candidate")
	}
	if promoted.TestPath != "" {
		t.Errorf("TestPath = %q, want empty for an evidence-only candidate (EmitJobs keeps the suite-digest key on this)", promoted.TestPath)
	}
	if promoted.CoveringTestPath != "tests/test_api.py" {
		t.Errorf("CoveringTestPath = %q, want tests/test_api.py (the authored-landing hint)", promoted.CoveringTestPath)
	}
	if promoted.CoveringTests == nil || *promoted.CoveringTests != 1 {
		t.Errorf("CoveringTests = %v, want 1", promoted.CoveringTests)
	}
	if promoted.Lang != "python" {
		t.Errorf("Lang = %q, want python (re-detected the same way Enumerate would have)", promoted.Lang)
	}
}

// A file the evidence measured and found ZERO covering tests is the design's
// other headline case: excluded, but truthfully — "uncovered", not
// "no-paired-test".
func TestWidenCandidacyRelabelsAMeasuredZeroCoverageFileAsUncovered(t *testing.T) {
	excl := []Exclusion{{Path: "pkg/dead.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/dead.py": {Tests: map[string]int{}},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none — zero coverage never promotes", gotCands)
	}
	if len(gotExcl) != 1 || gotExcl[0].Reason != ReasonUncovered {
		t.Fatalf("excl = %+v, want exactly one entry reasoned %q", gotExcl, ReasonUncovered)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0 — a relabel is not a promotion", promoted)
	}
}

// A file the evidence never measured AT ALL keeps its original reason:
// absence of evidence must never be read as evidence of absence.
func TestWidenCandidacyLeavesAnUnmeasuredFileAsNoPairedTest(t *testing.T) {
	excl := []Exclusion{{Path: "pkg/never_imported.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/other.py": {Tests: map[string]int{"tests/test_x.py::test_x": 1}},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none", gotCands)
	}
	if len(gotExcl) != 1 || gotExcl[0].Reason != ReasonNoPairedTest || gotExcl[0].Path != "pkg/never_imported.py" {
		t.Fatalf("excl = %+v, want the original no-paired-test entry untouched", gotExcl)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}
}

// A non-no-paired-test exclusion (no-language, is-test, ambiguous-test,
// gitignored, ...) is never touched by widening — it is not the reason this
// mechanism exists to reconsider.
func TestWidenCandidacyLeavesOtherExclusionReasonsAlone(t *testing.T) {
	excl := []Exclusion{
		{Path: "pkg/thing.txt", Reason: ReasonNoLanguage},
		{Path: "tests/test_x.py", Reason: ReasonIsTest},
		{Path: "pkg/ambiguous.py", Reason: ReasonAmbiguousTest},
	}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/thing.txt":    {Tests: map[string]int{"tests/test_x.py::test_x": 1}},
		"pkg/ambiguous.py": {Tests: map[string]int{"tests/test_x.py::test_x": 1}},
	})

	_, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotExcl) != 3 {
		t.Fatalf("excl = %+v, want all 3 untouched", gotExcl)
	}
	for i, e := range excl {
		if gotExcl[i] != e {
			t.Errorf("excl[%d] = %+v, want unchanged %+v", i, gotExcl[i], e)
		}
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}
}

// THE MIRRORED-FIXTURE PIN: a file that is ALREADY a candidate by pairing —
// with evidence ALSO covering it — must come back with every field the
// pairing walk produced untouched (TestPath, ViaSearch), so its report line,
// grading command, cache key and verdict stay byte-identical. Only
// CoveringTests is allowed to change, as ledger metadata.
func TestWidenCandidacyLeavesAnAlreadyPairedCandidateByteIdentical(t *testing.T) {
	before := Candidate{Path: "pkg/api.py", TestPath: "tests/test_api.py", Lang: "python", ViaSearch: true}
	cands := []Candidate{before}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/api.py": {Tests: map[string]int{"tests/test_api.py::test_a": 2}},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(cands, nil, idx, ok)

	if len(gotExcl) != 0 {
		t.Fatalf("excl = %+v, want none", gotExcl)
	}
	if len(gotCands) != 1 {
		t.Fatalf("cands = %+v, want exactly the one pairing-based candidate", gotCands)
	}
	got := gotCands[0]
	if got.Path != before.Path || got.TestPath != before.TestPath || got.Lang != before.Lang || got.ViaSearch != before.ViaSearch || got.CoveringTestPath != "" {
		t.Errorf("got %+v, want every pairing field unchanged from %+v (CoveringTestPath still empty)", got, before)
	}
	if got.CoveringTests == nil || *got.CoveringTests != 1 {
		t.Errorf("CoveringTests = %v, want 1 (ledger metadata only)", got.CoveringTests)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0 — an already-paired candidate is never a promotion", promoted)
	}
}

// The fallback: no evidence to widen with (ok=false) leaves cands/excl
// exactly as pairing produced them.
func TestWidenCandidacyIsANoOpWithoutEvidence(t *testing.T) {
	cands := []Candidate{{Path: "pkg/api.py", TestPath: "tests/test_api.py", Lang: "python"}}
	excl := []Exclusion{{Path: "pkg/utils.py", Reason: ReasonNoPairedTest}}

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(cands, excl, EvidenceIndex{}, false)

	if len(gotCands) != 1 || gotCands[0] != cands[0] {
		t.Errorf("cands = %+v, want unchanged %+v", gotCands, cands)
	}
	if len(gotExcl) != 1 || gotExcl[0] != excl[0] {
		t.Errorf("excl = %+v, want unchanged %+v", gotExcl, excl)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}
}
