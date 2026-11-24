// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"sort"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// promotionSample is one language's smallest repository in which its
// evidence could promote a file: a source with no paired test (source), a
// test unit that would execute it (unit), and the files the landing reads.
type promotionSample struct {
	files  map[string]string
	unit   string
	source string
}

// promotionSamples hold one sample per language the unit pass serves. Each
// is the shape the language-support row is about: a source no filename
// convention pairs, executed by a unit in its own package, crate or target.
var promotionSamples = map[string]promotionSample{
	"go": {files: map[string]string{
		"go.mod":    "module example.com/m\n",
		"a.go":      "package m\n",
		"a_test.go": "package m\n",
		"b.go":      "package m\n",
	}, unit: "example.com/m", source: "b.go"},
	"javascript": {files: map[string]string{"test/a.test.js": "", "src/b.js": ""}, unit: "test/a.test.js", source: "src/b.js"},
	"typescript": {files: map[string]string{"test/a.test.ts": "", "src/b.ts": ""}, unit: "test/a.test.ts", source: "src/b.ts"},
	"ruby":       {files: map[string]string{"spec/a_spec.rb": "", "lib/b.rb": ""}, unit: "spec/a_spec.rb", source: "lib/b.rb"},
	"php":        {files: map[string]string{"tests/ATest.php": "", "src/B.php": ""}, unit: "tests/ATest.php", source: "src/B.php"},
	"rust": {files: map[string]string{
		"Cargo.toml": "[package]\nname = \"a\"\n",
		"src/lib.rs": "pub mod b;\n",
		"src/b.rs":   "pub fn b() -> i32 { 1 }\n",
		"tests/t.rs": "#[test]\nfn t() {}\n",
	}, unit: "a/test:t", source: "src/b.rs"},
	"swift": {files: map[string]string{
		"Package.swift":             "// swift-tools-version:5.9\n",
		"Sources/A/B.swift":         "func b() -> Int { 1 }\n",
		"Tests/ATests/ATests.swift": "import XCTest\n",
	}, unit: "ATests", source: "Sources/A/B.swift"},
}

// sampleSources is the pairing.Sources over a sample's files, read the way
// DiskSources reads a checkout.
func sampleSources(p lang.Plugin, files map[string]string) pairing.Sources {
	list := make([]string, 0, len(files))
	for f := range files {
		list = append(list, f)
	}
	sort.Strings(list)
	return pairing.Sources{
		Exists:   func(cp string) bool { _, ok := files[cp]; return ok },
		Universe: func() ([]string, error) { return list, nil },
		TestModules: func(rel string) ([]pairing.TestModule, error) {
			if !readsTestModules(p.Name()) {
				return nil, pairing.ErrDeclarationsUnreadable
			}
			return repoindex.TestModules(p.Name(), []byte(files[rel])), nil
		},
	}
}

// unitSamplePromotes runs the code a unit pass's promotion goes through on
// p's sample: the source's own authored-test landing must be admitted
// (lang.CheckTestLanding; Enumerate excludes a refused one before pairing)
// AND a unit that executes it must land a test for it (UnitTestFile).
func unitSamplePromotes(t *testing.T, p lang.Plugin) (sourceAdmitted, unitLands bool) {
	t.Helper()
	s, ok := promotionSamples[p.Name()]
	if !ok {
		t.Fatalf("%s: no promotion sample", p.Name())
	}
	l, err := lang.TestLanding(s.source, sampleSources(p, s.files))
	sourceAdmitted = err == nil && lang.CheckTestLanding(s.source, l) == nil
	unitLands = UnitTestFile(p, s.files)(s.unit, s.source) != ""
	return sourceAdmitted, unitLands
}

// The declaration lang reads (lang.PromotesByEvidence, which the support
// page's row and the run's count line both ask) must match what the code
// promotion runs does, for every registered language: a declaration that
// drifts from the landing code fails here.
//
// Python's evidence is per test (lang.TestEvidence), not a unit pass, so its
// promotion is exercised through its own index: a no-paired-test file the
// per-test evidence measured a test executing is widened into a candidate.
func TestEvidencePromotionDeclarationMatchesTheLandingCode(t *testing.T) {
	for _, n := range lang.Names() {
		p, _ := lang.ByName(n)
		var does bool
		if _, perTest := p.(lang.TestEvidence); perTest {
			idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{
				"pkg/utils.py": {Tests: map[string]int{"tests/test_api.py::test_a": 4}},
			})
			_, _, promoted := WidenCandidacyByEvidence(nil, []Exclusion{{Path: "pkg/utils.py", Reason: ReasonNoPairedTest}}, idx, ok)
			if n != "python" {
				t.Fatalf("%s is a per-test evidence language with no fixture of its own here; add one", n)
			}
			does = promoted == 1
		} else {
			admitted, lands := unitSamplePromotes(t, p)
			does = admitted && lands
		}
		if declared := lang.PromotesByEvidence(p); declared != does {
			t.Errorf("%s: lang declares promotion %v, but the landing code promotes %v", n, declared, does)
		}
	}
}

// TestRustEvidencePromotesAnInlineTestedFile: a Rust source with no separate
// test file is no longer excluded before pairing. One whose tests are inline
// pairs to itself by name; one that declares no test module is a
// no-paired-test exclusion, and the library unit that executed it adds it to
// the audit. Either way its authored test lands in the source itself
// (lang.TestLanding), and the door admits it (lang.CheckTestLanding) — the
// indexmap shape (src/inner/extract.rs, executed in full by the library's
// tests) that the evidence-promotion exemption used to describe.
func TestRustEvidencePromotesAnInlineTestedFile(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":           "[package]\nname = \"a\"\nversion = \"0.1.0\"\n",
		"src/lib.rs":           "pub mod inner;\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n",
		"src/inner/mod.rs":     "pub mod extract;\n",
		"src/inner/extract.rs": "pub fn extract() -> i32 { 1 }\n",
	}
	root := writeTree(t, files)
	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	paired := map[string]string{}
	for _, c := range cands {
		paired[c.Path] = c.TestPath
	}
	if paired["src/lib.rs"] != "src/lib.rs" {
		t.Errorf("src/lib.rs (inline tests) pairs to %q, want itself; cands %+v", paired["src/lib.rs"], cands)
	}
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["src/inner/extract.rs"] != ReasonNoPairedTest {
		t.Fatalf("src/inner/extract.rs excluded as %q, want %q before the evidence", reasons["src/inner/extract.rs"], ReasonNoPairedTest)
	}

	rust, _ := lang.ByName("rust")
	ev := UnitEvidence{Ran: true, Units: []UnitResult{{Unit: "a/lib", Counts: map[string]lang.LineCounts{
		"src/lib.rs":           {Executed: 1, Executable: 1},
		"src/inner/extract.rs": {Executed: 1, Executable: 1},
	}}}}
	idx, ok := IndexFromUnits(ev, UnitFilesFor(rust, files))
	got, kept, promoted := WidenCandidacyByEvidence(cands, excl, idx, ok)
	if promoted != 1 {
		t.Fatalf("promoted %d, want src/inner/extract.rs added; kept %+v", promoted, kept)
	}
	var c Candidate
	for _, x := range got {
		if x.Path == "src/inner/extract.rs" {
			c = x
		}
	}
	if c.Path == "" || c.TestPath != "" || c.CoveringTestPath != "src/inner/extract.rs" {
		t.Fatalf("promoted candidate = %+v, want an evidence-only candidate whose unit lands on the source", c)
	}
	l, err := lang.TestLanding(c.Path, DiskSources(rust, root))
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != c.Path || !l.Into || l.OwnModule {
		t.Errorf("landing = %+v, want the source itself, declaring no test module of its own", l)
	}
	if err := lang.CheckTestLanding(c.Path, l); err != nil {
		t.Errorf("the door refuses the promoted file: %v", err)
	}
}

// A no-paired-test file that a green unit executed is added to the audit in
// Swift, whose evidence promotes since a test target has a landing; it was
// excluded as evidence-pairing-not-built before. A language that promotes
// keeps no-paired-test for a file it could not land (a Go file run only by
// another package's tests). No shipped language now reaches
// evidence-pairing-not-built: every shipped language promotes.
func TestAnExecutedFileIsAddedWhereEvidencePromotes(t *testing.T) {
	swift, _ := lang.ByName("swift")
	files := map[string]string{
		"Package.swift":             "// swift-tools-version:5.9\n",
		"Sources/A/B.swift":         "",
		"Sources/A/C.swift":         "",
		"Tests/ATests/BTests.swift": "",
	}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{{Unit: "ATests", Counts: map[string]lang.LineCounts{
		"Sources/A/B.swift": {Executed: 1, Executable: 1},
		"Sources/A/C.swift": {Executed: 1, Executable: 1},
	}}}}
	idx, ok := IndexFromUnits(ev, UnitFilesFor(swift, files))
	cands := []Candidate{{Path: "Sources/A/B.swift", TestPath: "Tests/ATests/BTests.swift", Lang: "swift"}}
	_, excl, promoted := WidenCandidacyByEvidence(cands, []Exclusion{{Path: "Sources/A/C.swift", Reason: ReasonNoPairedTest}}, idx, ok)
	if promoted != 1 || len(excl) != 0 {
		t.Fatalf("Swift: promoted %d, excl %+v; want C.swift added to the audit", promoted, excl)
	}

	goPlug, _ := lang.ByName("go")
	gofiles := map[string]string{
		"go.mod":      "module example.com/m\n",
		"a/a.go":      "package a\n",
		"b/b.go":      "package b\n",
		"b/b_test.go": "package b\n",
	}
	gev := UnitEvidence{Ran: true, Units: []UnitResult{{Unit: "example.com/m/b", Counts: map[string]lang.LineCounts{
		"a/a.go": {Executed: 1, Executable: 1},
		"b/b.go": {Executed: 1, Executable: 1},
	}}}}
	gidx, gok := IndexFromUnits(gev, UnitFilesFor(goPlug, gofiles))
	_, gexcl, _ := WidenCandidacyByEvidence(nil, []Exclusion{{Path: "a/a.go", Reason: ReasonNoPairedTest}}, gidx, gok)
	if len(gexcl) != 1 || gexcl[0].Reason != ReasonNoPairedTest {
		t.Fatalf("Go: excl %+v; want a/a.go kept as %q", gexcl, ReasonNoPairedTest)
	}
}
