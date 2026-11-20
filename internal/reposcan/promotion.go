// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"sort"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/repoindex"
)

func init() { lang.SetEvidencePromotionJudge(PromotesByEvidence) }

// promotionSample is one language's smallest repository in which a unit
// pass could promote a file: a source with no paired test (source), a test
// unit that would execute it (unit), and the files the landing reads.
type promotionSample struct {
	files  map[string]string
	unit   string
	source string
}

// promotionSamples hold one sample per language the unit pass serves
// (TestEveryUnitPassLanguageHasAPromotionSample). Each is the shape the
// language-support page's row is about: a source no filename convention
// pairs, executed by a unit in its own package, crate or target.
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

// PromotesByEvidence reports whether p's evidence can add a source with no
// paired test to the audit (WidenCandidacyByEvidence), judged by running the
// code that decides it rather than by a declared flag:
//
//   - a per-test evidence language (lang.TestEvidence, Python) promotes onto
//     the test the evidence measured, which ParseEvidenceIndex names as the
//     landing for every file it measured;
//   - a unit-pass language promotes when, on its sample, the source's own
//     authored-test landing is admitted (lang.CheckTestLanding: Rust refuses
//     a source whose test would land on the source itself, which every Rust
//     source without a separate test file does) AND a unit that executes it
//     lands a test for it (UnitTestFile, which has no Swift case);
//   - any other language produces no evidence, and promotes nothing.
//
// The language-support page's row (lang's "evidence-promotion" capability)
// and the run's count line (evidencePromotionNote in cmd/corral) both ask
// this, so the page and the run cannot disagree.
func PromotesByEvidence(p lang.Plugin) bool {
	if _, ok := p.(lang.TestEvidence); ok {
		return true
	}
	s, ok := promotionSamples[p.Name()]
	if !ok {
		return false
	}
	landing, err := lang.TestLanding(s.source, sampleSources(p, s.files))
	if err != nil || lang.CheckTestLanding(s.source, landing) != nil {
		return false
	}
	return UnitTestFile(p, s.files)(s.unit, s.source) != ""
}

// sampleSources is the pairing.Sources over a sample's files, read the way
// DiskSources reads a checkout: existence, the listing, and each source's
// declared test modules from its text.
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
