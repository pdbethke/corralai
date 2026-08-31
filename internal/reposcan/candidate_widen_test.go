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
	// pkg/__init__.py establishes pkg/ as an importable package — the
	// library-code gate (founder ruling, below) requires it for a bare
	// "pkg/..." path with no src/ prefix.
	excl := []Exclusion{
		{Path: "pkg/dead.py", Reason: ReasonNoPairedTest},
		{Path: "pkg/__init__.py", Reason: ReasonNoPairedTest},
	}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/dead.py": {Tests: map[string]int{}, HasStatements: true},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none — zero coverage never promotes", gotCands)
	}
	// pkg/__init__.py itself was never measured (idx has no entry for it):
	// absence of evidence, so it keeps its original reason untouched.
	var dead *Exclusion
	for i := range gotExcl {
		if gotExcl[i].Path == "pkg/dead.py" {
			dead = &gotExcl[i]
		}
	}
	if dead == nil || dead.Reason != ReasonUncovered {
		t.Fatalf("excl = %+v, want pkg/dead.py reasoned %q", gotExcl, ReasonUncovered)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0 — a relabel is not a promotion", promoted)
	}
}

// THE FOURTH DEFECT: a file present in the evidence with zero covering
// TESTS but NON-EMPTY static (import/module-load-time) coverage was
// executed — coverage.py recorded real lines for it — just never by a
// test directly. Calling that "uncovered — no test executes this file" is
// false in the sense a reader checks it against, and hits on essentially
// every Python repo's package __init__.py. Must relabel ReasonImportOnly,
// NEVER ReasonUncovered — same disposition (excluded, nothing to grade a
// kill rate against) but a different, honest claim.
func TestWidenCandidacyRelabelsAnImportOnlyFileDistinctlyFromUncovered(t *testing.T) {
	excl := []Exclusion{{Path: "src/pkg/__init__.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"src/pkg/__init__.py": {Tests: map[string]int{}, HasStatic: true, HasStatements: true},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none — import-only never promotes, same as uncovered", gotCands)
	}
	if len(gotExcl) != 1 || gotExcl[0].Reason != ReasonImportOnly {
		t.Fatalf("excl = %+v, want exactly one entry reasoned %q", gotExcl, ReasonImportOnly)
	}
	if gotExcl[0].Reason == ReasonUncovered {
		t.Error("an executed-but-import-only file must NEVER read as ReasonUncovered")
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0 — a relabel is not a promotion", promoted)
	}
}

// A genuinely dead file — zero covering tests AND zero static coverage,
// nothing executed it at all — is still ReasonUncovered. The distinction
// above must not weaken this, the ORIGINAL headline case.
func TestWidenCandidacyStillLabelsAGenuinelyDeadFileUncovered(t *testing.T) {
	excl := []Exclusion{{Path: "src/pkg/deadmod.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"src/pkg/deadmod.py": {Tests: map[string]int{}, HasStatic: false, HasStatements: true},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none", gotCands)
	}
	if len(gotExcl) != 1 || gotExcl[0].Reason != ReasonUncovered {
		t.Fatalf("excl = %+v, want exactly one entry reasoned %q", gotExcl, ReasonUncovered)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}
}

// THE EMPTY-FILE DEFECT: a file with zero covering tests AND zero static
// coverage is the SAME shape as a genuinely dead file — UNLESS coverage's
// own static parse found no executable statement in it at all (a 0-byte or
// comment-only file, the textbook case being tests/__init__.py). That is
// benign — nothing to execute, nothing a test could cover — and must never
// read as "uncovered", which every real Python repo's handful of empty
// __init__.py files would otherwise inflate the headline finding with.
func TestWidenCandidacyExcludesAnEmptyFileAsNoExecutableCode(t *testing.T) {
	excl := []Exclusion{
		{Path: "pkg/empty.py", Reason: ReasonNoPairedTest},
		{Path: "pkg/__init__.py", Reason: ReasonNoPairedTest},
	}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/empty.py": {Tests: map[string]int{}, HasStatements: false},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none", gotCands)
	}
	var empty *Exclusion
	for i := range gotExcl {
		if gotExcl[i].Path == "pkg/empty.py" {
			empty = &gotExcl[i]
		}
	}
	if empty == nil || empty.Reason != ReasonNoExecutableCode {
		t.Fatalf("excl = %+v, want pkg/empty.py reasoned %q", gotExcl, ReasonNoExecutableCode)
	}
	if empty.Reason == ReasonUncovered || empty.Reason == ReasonImportOnly {
		t.Error("an empty file must never read as uncovered or import-only")
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}
}

// THE FOUNDER RULING: uncovered/import-only/no-executable-code apply to
// LIBRARY CODE ONLY. A file with zero coverage that sits in NO importable
// package (no __init__.py anywhere in its directory chain, no src/<pkg>/
// layout) — docs/conf.py is the canonical example — keeps its ORDINARY
// no-paired-test reason. It is still enumerated, still excluded, just
// never counted among the loudest findings.
func TestWidenCandidacyLeavesANonLibraryFileAtItsOrdinaryReason(t *testing.T) {
	excl := []Exclusion{{Path: "docs/conf.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"docs/conf.py": {Tests: map[string]int{}, HasStatements: true},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none", gotCands)
	}
	if len(gotExcl) != 1 || gotExcl[0].Reason != ReasonNoPairedTest || gotExcl[0].Path != "docs/conf.py" {
		t.Fatalf("excl = %+v, want docs/conf.py untouched at its ordinary reason %q — the evidence measured it, but it is not library code", gotExcl, ReasonNoPairedTest)
	}
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}
}

// The library gate is scoped to a real package: src/pkg/dead.py (an
// src/<pkg>/ layout — no __init__.py needed, PEP 420 namespace packages
// ship without one) IS library code, and a genuine zero-coverage finding
// for it stays the loud ReasonUncovered.
func TestWidenCandidacyStillLabelsAnSrcLayoutLibraryFileUncovered(t *testing.T) {
	excl := []Exclusion{{Path: "src/pkg/dead.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"src/pkg/dead.py": {Tests: map[string]int{}, HasStatements: true},
	})

	_, gotExcl, _ := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotExcl) != 1 || gotExcl[0].Reason != ReasonUncovered {
		t.Fatalf("excl = %+v, want src/pkg/dead.py reasoned %q", gotExcl, ReasonUncovered)
	}
}

// A FLAT-layout library file (mypkg/dead.py, WITH mypkg/__init__.py
// present as its own enumerated entry) is also library code, and a genuine
// zero-coverage finding for it stays ReasonUncovered — the founder's own
// worked example.
func TestWidenCandidacyStillLabelsAFlatLayoutLibraryFileUncovered(t *testing.T) {
	excl := []Exclusion{
		{Path: "mypkg/dead.py", Reason: ReasonNoPairedTest},
		{Path: "mypkg/__init__.py", Reason: ReasonNoPairedTest},
	}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"mypkg/dead.py": {Tests: map[string]int{}, HasStatements: true},
	})

	_, gotExcl, _ := WidenCandidacyByEvidence(nil, excl, idx, ok)

	var dead *Exclusion
	for i := range gotExcl {
		if gotExcl[i].Path == "mypkg/dead.py" {
			dead = &gotExcl[i]
		}
	}
	if dead == nil || dead.Reason != ReasonUncovered {
		t.Fatalf("excl = %+v, want mypkg/dead.py reasoned %q", gotExcl, ReasonUncovered)
	}
}

// A file present in the evidence WITH covering tests must promote to a
// candidate exactly as before — HasStatic (true or false) never changes
// that outcome, only the zero-covering-tests branch cares about it.
func TestWidenCandidacyPromotesRegardlessOfHasStaticWhenTestsCoverIt(t *testing.T) {
	excl := []Exclusion{{Path: "pkg/utils.py", Reason: ReasonNoPairedTest}}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/utils.py": {Tests: map[string]int{"tests/test_api.py::test_a": 4}, HasStatic: true},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotExcl) != 0 {
		t.Fatalf("excl = %+v, want utils.py promoted out of the exclusion list", gotExcl)
	}
	if len(gotCands) != 1 || gotCands[0].Path != "pkg/utils.py" {
		t.Fatalf("cands = %+v, want pkg/utils.py promoted", gotCands)
	}
	if promoted != 1 {
		t.Errorf("promoted = %d, want 1", promoted)
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
