// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
// pkg/__init__.py is deliberately IN excl too (also unmeasured — absent
// from idx below) — not because this test cares about it, but because
// WITHOUT it, "pkg" is not a known package to the library-code gate
// (isLibraryCode), and pkg/never_imported.py would be left untouched by
// THAT gate instead of by the absence-of-evidence rule this test exists to
// pin: the two produce the identical outcome (untouched, ReasonNoPairedTest)
// for entirely different reasons, and a fixture that lets the wrong one
// fire still passes while leaving the absence invariant completely
// unpinned. Verified by temporarily deleting the `case !measured:` branch
// in WidenCandidacyByEvidence: WITH pkg/__init__.py present here, this test
// then fails (pkg/never_imported.py gets relabeled); confirming the fixture
// actually exercises the branch it claims to.
func TestWidenCandidacyLeavesAnUnmeasuredFileAsNoPairedTest(t *testing.T) {
	excl := []Exclusion{
		{Path: "pkg/never_imported.py", Reason: ReasonNoPairedTest},
		{Path: "pkg/__init__.py", Reason: ReasonNoPairedTest},
	}
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/other.py": {Tests: map[string]int{"tests/test_x.py::test_x": 1}},
	})

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)

	if len(gotCands) != 0 {
		t.Fatalf("cands = %+v, want none", gotCands)
	}
	// pkg/__init__.py itself is ALSO unmeasured (absent from idx), so it
	// must keep its own original reason too — the assertion below checks
	// the whole slice, both entries, not just never_imported.py.
	want := []Exclusion{
		{Path: "pkg/__init__.py", Reason: ReasonNoPairedTest},
		{Path: "pkg/never_imported.py", Reason: ReasonNoPairedTest},
	}
	sort.Slice(gotExcl, func(i, j int) bool { return gotExcl[i].Path < gotExcl[j].Path })
	if !reflect.DeepEqual(gotExcl, want) {
		t.Fatalf("excl = %+v, want both original no-paired-test entries untouched: %+v", gotExcl, want)
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

// The unit-kind twin of TestWidenCandidacyLeavesAnAlreadyPairedCandidateByteIdentical
// (ruling R2): an index built from test UNITS counts units, not tests, so it
// must never write Candidate.CoveringTests — the ledger's covering_tests
// column means "tests". An already-paired candidate comes back with every
// pairing field byte-identical to what pairing produced, and a promoted one
// carries its landing file with CoveringTests left nil. The one field a
// unit index may write is CoveringUnits (ruling R3), ledger metadata in its
// own column, exactly as CoveringTests is for a per-test index.
func TestWidenCandidacyByUnitsLeavesAnAlreadyPairedCandidateByteIdentical(t *testing.T) {
	ts, ok := lang.ByName("typescript")
	if !ok {
		t.Fatal("no typescript plugin")
	}
	before := Candidate{Path: "src/a.ts", TestPath: "test/a.test.ts", Lang: "typescript", ViaSearch: true}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{
		{Unit: "test/a.test.ts", Counts: map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 10}}},
		{Unit: "tests/other.test.ts", Counts: map[string]lang.LineCounts{
			"src/a.ts": {Executed: 9, Executable: 10},
			"src/b.ts": {Executed: 3, Executable: 4},
		}},
	}}
	idx, idxOK := IndexFromUnits(ev, UnitFilesFor(ts, map[string]string{"test/a.test.ts": "", "tests/other.test.ts": ""}))
	if !idxOK || !idx.UnitKind() {
		t.Fatalf("fixture: want a unit-kind index, got ok=%v unitKind=%v", idxOK, idx.UnitKind())
	}

	gotCands, gotExcl, promoted := WidenCandidacyByEvidence([]Candidate{before},
		[]Exclusion{{Path: "src/b.ts", Reason: ReasonNoPairedTest}}, idx, idxOK)

	if len(gotExcl) != 0 || promoted != 1 || len(gotCands) != 2 {
		t.Fatalf("cands=%+v excl=%+v promoted=%d, want src/b.ts promoted beside the paired src/a.ts", gotCands, gotExcl, promoted)
	}
	paired := before
	paired.CoveringUnits = intPtr(2)
	if !reflect.DeepEqual(gotCands[0], paired) {
		t.Errorf("paired candidate = %+v, want %+v (pairing fields byte-identical, no CoveringTests from a unit index, CoveringUnits 2)", gotCands[0], paired)
	}
	want := Candidate{Path: "src/b.ts", Lang: "typescript", CoveringTestPath: "tests/other.test.ts", CoveringUnits: intPtr(1)}
	if !reflect.DeepEqual(gotCands[1], want) {
		t.Errorf("promoted candidate = %+v, want %+v (CoveringTests nil: units are not tests)", gotCands[1], want)
	}
	if n, measured := idx.UnitsFor("src/b.ts"); !measured || n != 1 {
		t.Errorf("UnitsFor(src/b.ts) = %d, %v, want 1, true — the printed count's source", n, measured)
	}
}

// CoveringTests means Python's per-test count and nothing else: a unit-kind
// index never writes it, on a paired or a promoted candidate, and writes
// CoveringUnits instead — the units UnitsFor counts. A per-test index is the
// mirror image: CoveringTests set, CoveringUnits nil, so neither count ever
// stands in for the other in the ledger.
func TestCoveringTestsNeverSetFromUnits(t *testing.T) {
	ts, ok := lang.ByName("typescript")
	if !ok {
		t.Fatal("no typescript plugin")
	}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{
		{Unit: "test/a.test.ts", Counts: map[string]lang.LineCounts{"src/a.ts": {Executed: 2, Executable: 10}}},
		{Unit: "test/b.test.ts", Counts: map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 10}, "src/c.ts": {Executed: 1, Executable: 3}}},
		{Unit: "test/c.test.ts", Counts: map[string]lang.LineCounts{"src/c.ts": {Executed: 2, Executable: 3}}},
	}}
	idx, idxOK := IndexFromUnits(ev, UnitFilesFor(ts, map[string]string{"test/a.test.ts": "", "test/b.test.ts": "", "test/c.test.ts": ""}))
	if !idxOK || !idx.UnitKind() {
		t.Fatalf("fixture: want a unit-kind index, got ok=%v unitKind=%v", idxOK, idx.UnitKind())
	}
	cands, _, _ := WidenCandidacyByEvidence(
		[]Candidate{{Path: "src/a.ts", TestPath: "test/a.test.ts", Lang: "typescript"}},
		[]Exclusion{{Path: "src/c.ts", Reason: ReasonNoPairedTest}}, idx, idxOK)
	if len(cands) != 2 {
		t.Fatalf("cands = %+v, want the paired src/a.ts and the promoted src/c.ts", cands)
	}
	for _, c := range cands {
		if c.CoveringTests != nil {
			t.Errorf("%s: CoveringTests = %d from a unit index; units are not tests", c.Path, *c.CoveringTests)
		}
		want, measured := idx.UnitsFor(c.Path)
		if !measured || c.CoveringUnits == nil || *c.CoveringUnits != want || want != 2 {
			t.Errorf("%s: CoveringUnits = %v, want UnitsFor's %d (measured %v), which is 2", c.Path, c.CoveringUnits, want, measured)
		}
	}

	perTest, perTestOK := widenFixtureIndex(t, map[string]lang.FileCoverage{
		"pkg/a.py": {Tests: map[string]int{"tests/test_a.py::t1": 1, "tests/test_a.py::t2": 1}},
		"pkg/u.py": {Tests: map[string]int{"tests/test_a.py::t1": 1}},
	})
	pcands, _, _ := WidenCandidacyByEvidence(
		[]Candidate{{Path: "pkg/a.py", TestPath: "tests/test_a.py", Lang: "python"}},
		[]Exclusion{{Path: "pkg/u.py", Reason: ReasonNoPairedTest}}, perTest, perTestOK)
	if len(pcands) != 2 {
		t.Fatalf("per-test cands = %+v, want two", pcands)
	}
	for _, c := range pcands {
		if c.CoveringUnits != nil {
			t.Errorf("%s: CoveringUnits = %d from a per-test index; it counts units only", c.Path, *c.CoveringUnits)
		}
		if c.CoveringTests == nil {
			t.Errorf("%s: CoveringTests nil from a per-test index", c.Path)
		}
	}
}

func intPtr(n int) *int { return &n }

// UNIT EVIDENCE SAYS ONLY WHAT ITS UNITS RAN (ruling R24). A file a unit
// LOADED whose functions never ran is import-only, the claim the codebase
// already has for that shape; a file measured at zero that no unit even
// loaded is uncovered under a reason scoped to the command's units, never
// Python's "no test executes this file", which speaks for the whole suite.
// REA's composition units made the unscoped claim 200 times (review I1).
func TestWidenCandidacyByUnitsSaysOnlyWhatTheUnitsRan(t *testing.T) {
	ts, ok := lang.ByName("typescript")
	if !ok {
		t.Fatal("no typescript plugin")
	}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{
		{Unit: "test/a.test.ts", Counts: map[string]lang.LineCounts{
			"src/loaded.ts": {Executed: 0, Executable: 6},
			"src/dead.ts":   {Executed: 0, Executable: 4},
		}, Loaded: []string{"src/loaded.ts"}},
	}}
	idx, idxOK := IndexFromUnits(ev, UnitFilesFor(ts, map[string]string{"test/a.test.ts": ""}))
	if !idxOK || !idx.UnitKind() {
		t.Fatalf("fixture: want a unit-kind index, got ok=%v unitKind=%v", idxOK, idx.UnitKind())
	}
	_, excl, _ := WidenCandidacyByEvidence(nil, []Exclusion{
		{Path: "src/loaded.ts", Reason: ReasonNoPairedTest},
		{Path: "src/dead.ts", Reason: ReasonNoPairedTest},
	}, idx, idxOK)
	got := map[string]string{}
	for _, e := range excl {
		got[e.Path] = e.Reason
	}
	if got["src/loaded.ts"] != ReasonImportOnly {
		t.Errorf("src/loaded.ts = %q, want %q: a unit loaded it and ran none of its functions", got["src/loaded.ts"], ReasonImportOnly)
	}
	if got["src/dead.ts"] != ReasonUncoveredByUnits {
		t.Errorf("src/dead.ts = %q, want %q: scoped to the units this command runs", got["src/dead.ts"], ReasonUncoveredByUnits)
	}
	if ReasonUncoveredByUnits == ReasonUncovered || !IsUncovered(ReasonUncoveredByUnits) || !IsUncovered(ReasonUncovered) || IsUncovered(ReasonImportOnly) {
		t.Error("the scoped reason is its own string, and both uncovered reasons are uncovered")
	}
}

// R24 HOLDS IN RUBY AND PHP TOO (final review I1). The reports are the ones
// a real run of internal/lang's fixture suites printed (held to a live run
// there): lib/dead.rb and lib/Dead.php are required by the unit, their class
// bodies and constants run, and no method of theirs is called. They are
// import-only. Before the reducers printed `# loaded`, both came out
// "uncovered — no test unit this command runs executes this file", which
// the unit had in fact done.
func TestWidenCandidacyByUnitsCallsARubyOrPHPFileLoadedNeverCalledImportOnly(t *testing.T) {
	cases := []struct{ lang, report, unit, dead string }{
		{"ruby", "ruby-report.txt", "test/calc_test.rb", "lib/dead.rb"},
		{"php", "php-report.txt", "test/CalcTest.php", "lib/Dead.php"},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			p, ok := lang.ByName(c.lang)
			if !ok {
				t.Fatalf("no %s plugin", c.lang)
			}
			raw, err := os.ReadFile(filepath.Join("..", "lang", "testdata", "coverage", c.report)) // #nosec G304 -- a fixed testdata path
			if err != nil {
				t.Fatal(err)
			}
			counts, err := p.(lang.CoverageReporter).CoverageCounts(string(raw), "")
			if err != nil {
				t.Fatal(err)
			}
			ev := UnitEvidence{Ran: true, Units: []UnitResult{{Unit: c.unit, Counts: counts, Loaded: lang.LoadedCoverage(string(raw))}}}
			idx, idxOK := IndexFromUnits(ev, UnitFilesFor(p, map[string]string{c.unit: ""}))
			if !idxOK {
				t.Fatal("fixture: want a unit-kind index")
			}
			_, excl, _ := WidenCandidacyByEvidence(nil, []Exclusion{{Path: c.dead, Reason: ReasonNoPairedTest}}, idx, idxOK)
			if len(excl) != 1 || excl[0].Reason != ReasonImportOnly {
				t.Errorf("%s exclusions = %+v, want one %q: the unit loaded it and called none of its methods", c.dead, excl, ReasonImportOnly)
			}
		})
	}
}
