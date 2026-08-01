// SPDX-License-Identifier: Elastic-2.0

package reposcan

import "testing"

// LanguageProfile turns data the scan ALREADY computes and then throws away
// into the inventory a first-look surface needs.
//
// Language detection runs on every walked file — that is where the
// "no-language" tally comes from — but the result is kept only as a rejection
// count. So a scan can say "121 files aren't code" and cannot say "this repo is
// Python: 68 files, 6 auditable, 21 with no paired test", which is the more
// useful and more actionable statement, and is free: no model call, no jail, no
// key. It is exactly what `--dry-run` should be able to hand a UI.
func TestLanguageProfile_CountsCandidatesAndExclusionsPerLanguage(t *testing.T) {
	cands := []Candidate{
		{Path: "src/a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "src/b.py", TestPath: "tests/test_b.py", Lang: "python"},
		{Path: "pkg/c.go", TestPath: "pkg/c_test.go", Lang: "go"},
	}
	excl := []Exclusion{
		{Path: "src/lonely.py", Reason: ReasonNoPairedTest},
		{Path: "src/other.py", Reason: ReasonNoPairedTest},
		{Path: "tests/test_a.py", Reason: ReasonIsTest},
		{Path: "web/app.js", Reason: ReasonNoPairedTest},
		{Path: "README.md", Reason: ReasonNoLanguage},
		{Path: ".editorconfig", Reason: ReasonNoLanguage},
	}

	got := BuildLanguageProfile(cands, excl)

	byLang := map[string]LanguageStat{}
	for _, s := range got {
		byLang[s.Lang] = s
	}

	if py := byLang["python"]; py.Auditable != 2 || py.NoPairedTest != 2 {
		t.Errorf("python = %+v, want 2 auditable and 2 without a paired test", py)
	}
	if g := byLang["go"]; g.Auditable != 1 {
		t.Errorf("go = %+v, want 1 auditable", g)
	}
	// A JS file with no pair must still appear: a language corral can DETECT but
	// is failing to pair is the single most useful thing this profile can show
	// (see the deliberately-pinned expressjs/express zero in the CI sweep).
	if js := byLang["javascript"]; js.NoPairedTest != 1 {
		t.Errorf("javascript = %+v, want 1 without a paired test — a detected-but-unpaired language must not vanish", js)
	}

	// Files no plugin recognises are NOT a language and must never be invented
	// as one: README.md and .editorconfig are not a "markdown project".
	if _, invented := byLang[""]; invented {
		t.Error("unrecognised files must not become an empty-named language row")
	}
	for _, s := range got {
		if s.Lang == "no-language" || s.Lang == "markdown" {
			t.Errorf("invented a language row %q from unrecognised files", s.Lang)
		}
	}
}

// TestLanguageProfile_IsTestFilesAreNotSourceFiles pins that a repo's own test
// files are counted as tests, never as unpaired source. Folding them in would
// inflate every "files with no test" number — turning a well-tested repo into a
// scary one, which is the false-accusation shape this project guards against.
func TestLanguageProfile_IsTestFilesAreNotSourceFiles(t *testing.T) {
	got := BuildLanguageProfile(nil, []Exclusion{
		{Path: "tests/test_a.py", Reason: ReasonIsTest},
		{Path: "tests/test_b.py", Reason: ReasonIsTest},
		{Path: "src/c.py", Reason: ReasonNoPairedTest},
	})
	for _, s := range got {
		if s.Lang != "python" {
			continue
		}
		if s.TestFiles != 2 {
			t.Errorf("python TestFiles = %d, want 2", s.TestFiles)
		}
		if s.NoPairedTest != 1 {
			t.Errorf("python NoPairedTest = %d, want 1 — test files must not count as unpaired source", s.NoPairedTest)
		}
	}
}

// TestLanguageProfile_StableOrder pins a deterministic ordering: this feeds a
// report and a UI, and a listing that reshuffles between identical runs reads as
// churn that isn't there.
func TestLanguageProfile_StableOrder(t *testing.T) {
	cands := []Candidate{
		{Path: "a.go", Lang: "go"},
		{Path: "b.py", Lang: "python"},
		{Path: "c.py", Lang: "python"},
		{Path: "d.rb", Lang: "ruby"},
	}
	first := BuildLanguageProfile(cands, nil)
	for i := 0; i < 5; i++ {
		again := BuildLanguageProfile(cands, nil)
		for j := range first {
			if first[j].Lang != again[j].Lang {
				t.Fatalf("ordering is not stable: %v vs %v", first, again)
			}
		}
	}
	// Most auditable first — the rows a reader acts on come first.
	if first[0].Lang != "python" {
		t.Errorf("first row = %q, want python (2 auditable, the most)", first[0].Lang)
	}
}
