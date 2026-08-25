// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"testing"
)

// This file pins, as a property, the invariant dedupeCandidates documents but
// does not enforce: today, "attribute the least-specific (max) rank among
// colliding non-sibling forms" and the principled "attribute the STRONGEST
// (min) rank among forms that actually assert real directory evidence" agree
// for every shipped plugin — because no shipped plugin's TestPaths can
// currently produce a collision group that mixes a non-vacuous, non-sibling
// form with a weaker one. See dedupeCandidates' doc comment in convention.go.
//
// The principled rule, restated for this file: a raw (pre-dedupe) candidate
// asserts real directory evidence unless it is a sibling (which is ALWAYS
// real, same-directory evidence, intrinsically) or its own directory
// component has degenerated to empty (asserting nothing — it is
// indistinguishable from a flat, no-context match). When several raw forms
// collide on the same path string: a sibling among them always wins outright
// (Rank 0); otherwise, if at least one colliding form is non-vacuous, the
// merged rank is the MIN (strongest) among the non-vacuous forms' ranks —
// vacuous colliders contribute no information and must not drag a real
// claim down; only when EVERY colliding form is vacuous does the merge fall
// back to the least-informative (max) rank, because in that case there is
// truly nothing to distinguish them.
//
// rawForm is this file's OWN model of a plugin's pre-dedupe candidate list —
// deliberately reimplemented per plugin below rather than obtained by
// instrumenting dedupeCandidates itself, so the equivalence check in
// TestDedupeMatchesPrincipledRuleForShippedPlugins is not "compare production
// to itself."
type rawForm struct {
	Path    string
	Rank    int
	Sibling bool // same-directory match: intrinsically real evidence, always.
	Vacuous bool // non-sibling form whose directory component is empty: asserts nothing.
}

// principledMerge independently implements the principled rule described
// above: sibling wins outright; otherwise min-of-non-vacuous; otherwise (all
// vacuous) max. Ordering matches dedupeCandidates: each surviving path keeps
// the position of its FIRST occurrence in cands.
func principledMerge(cands []rawForm) []TestCandidate {
	firstIdx := map[string]int{}
	var order []string
	siblingAny := map[string]bool{}
	haveNonVacuous := map[string]bool{}
	minNonVacuousRank := map[string]int{}
	maxRank := map[string]int{}
	haveAny := map[string]bool{}

	for _, c := range cands {
		if _, ok := firstIdx[c.Path]; !ok {
			firstIdx[c.Path] = len(order)
			order = append(order, c.Path)
		}
		if c.Sibling {
			siblingAny[c.Path] = true
		}
		if !haveAny[c.Path] || c.Rank > maxRank[c.Path] {
			maxRank[c.Path] = c.Rank
		}
		haveAny[c.Path] = true
		if !c.Sibling && !c.Vacuous {
			if !haveNonVacuous[c.Path] || c.Rank < minNonVacuousRank[c.Path] {
				minNonVacuousRank[c.Path] = c.Rank
				haveNonVacuous[c.Path] = true
			}
		}
	}

	out := make([]TestCandidate, len(order))
	for i, p := range order {
		var rank int
		switch {
		case siblingAny[p]:
			rank = 0
		case haveNonVacuous[p]:
			rank = minNonVacuousRank[p]
		default:
			rank = maxRank[p]
		}
		out[i] = TestCandidate{Path: p, Rank: rank}
	}
	return out
}

// --- Per-plugin raw (pre-dedupe) form generators. ---
//
// Each mirrors the corresponding plugin's TestPaths body EXACTLY (same
// forms, same order, same ranks) but stops short of calling dedupeCandidates
// and instead tags each raw candidate with Sibling/Vacuous so
// principledMerge can be applied independently. Only the already-existing,
// non-dedupe helpers (splitPath / joinDir / stripFirstSegment / dirDepth)
// are reused — reusing those is reusing path arithmetic, not reusing (or
// wrapping) the merge algorithm under test.

func pythonRawForms(codePath string) []rawForm {
	dir, base, _ := splitPath(codePath)
	name := "test_" + base + ".py"
	altName := base + "_test.py"
	sub := stripFirstSegment(dir)

	out := []rawForm{
		{Path: joinDir(dir, name), Rank: 0, Sibling: true},
		{Path: joinDir(dir, altName), Rank: 0, Sibling: true},
		{Path: filepath.Join("tests", dir, name), Rank: 1, Vacuous: dir == ""},
		{Path: filepath.Join("tests", sub, name), Rank: 2, Vacuous: sub == ""},
	}
	if dirDepth(dir) <= 2 {
		out = append(out, rawForm{Path: filepath.Join("tests", name), Rank: 3, Vacuous: true})
	}
	return out
}

// jsFamilyRawForms mirrors jsPlugin/tsPlugin.TestPaths (identical shape,
// parameterized only by the literal extension each uses for every form).
func jsFamilyRawForms(codePath, ext string) []rawForm {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)
	testName := base + ".test." + ext
	specName := base + ".spec." + ext

	return []rawForm{
		{Path: joinDir(dir, testName), Rank: 0, Sibling: true},
		{Path: joinDir(dir, specName), Rank: 0, Sibling: true},
		{Path: filepath.Join(dir, "__tests__", testName), Rank: 1, Vacuous: dir == ""},
		{Path: filepath.Join("test", sub, testName), Rank: 2, Vacuous: sub == ""},
		{Path: filepath.Join("tests", sub, testName), Rank: 2, Vacuous: sub == ""},
	}
}

func javascriptRawForms(codePath string) []rawForm { return jsFamilyRawForms(codePath, "js") }
func typescriptRawForms(codePath string) []rawForm { return jsFamilyRawForms(codePath, "ts") }

func rubyRawForms(codePath string) []rawForm {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)

	return []rawForm{
		{Path: joinDir(dir, base+"_test.rb"), Rank: 0, Sibling: true},
		{Path: filepath.Join("test", sub, base+"_test.rb"), Rank: 1, Vacuous: sub == ""},
		{Path: filepath.Join("spec", sub, base+"_spec.rb"), Rank: 1, Vacuous: sub == ""},
		{Path: filepath.Join("test", sub, "test_"+base+".rb"), Rank: 2, Vacuous: sub == ""},
	}
}

// goRawForms: Go's convention is the sibling file only — never a collision
// family — included for completeness so "every shipped plugin" is literal.
func goRawForms(codePath string) []rawForm {
	dir, base, _ := splitPath(codePath)
	return []rawForm{
		{Path: joinDir(dir, base+"_test.go"), Rank: 0, Sibling: true},
	}
}

// --- Deterministic corpus of directory shapes, depths 0-3. ---

// corpusDirs spans several path vocabularies at depths 0 through 3,
// including sources that live UNDER a parallel test root (tests/x, test/a,
// spec/a) — the one family that mixes a Rank-0 sibling match with weaker
// forms in the SAME collision group (e.g. tests/utils.py, test/a/foo.rb: the
// sibling string-collides with a same-source stripped/flat form).
var corpusDirs = []string{
	// depth 0
	"",
	// depth 1
	"src", "lib", "aisuite", "agents", "pkgA", "examples", "tests", "test", "spec", "__tests__",
	// depth 2 (includes the parallel-test-root-mixing family)
	"src/pkg", "aisuite/agents", "pkgA/agents", "pkgB/agents", "examples/celery",
	"tests/x", "test/a", "spec/a", "docs/sub", "mypkg/sub",
	// depth 3
	"examples/celery/task_app", "examples/javascript/js_example", "src/flask/sansio",
	"tests/agents/nested", "test/a/b", "spec/a/b", "a/b/c",
}

var corpusBases = []string{"foo", "utils", "x", "conf", "artifact_store"}

func candidatesEqual(a, b []TestCandidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDedupeMatchesPrincipledRuleForShippedPlugins is the equivalence check.
// For every shipped plugin, across the whole corpus, production's
// dedupeCandidates (reached through the real TestPaths) must agree with this
// file's INDEPENDENTLY implemented principled merge. This is the property
// the comment on dedupeCandidates in convention.go now documents.
func TestDedupeMatchesPrincipledRuleForShippedPlugins(t *testing.T) {
	plugins := []struct {
		name      string
		ext       string
		raw       func(string) []rawForm
		testPaths func(string) []TestCandidate
	}{
		{"python", "py", pythonRawForms, pyPlugin{}.TestPaths},
		{"javascript", "js", javascriptRawForms, jsPlugin{}.TestPaths},
		{"typescript", "ts", typescriptRawForms, tsPlugin{}.TestPaths},
		{"ruby", "rb", rubyRawForms, rubyPlugin{}.TestPaths},
		{"go", "go", goRawForms, goPlugin{}.TestPaths},
	}

	for _, p := range plugins {
		t.Run(p.name, func(t *testing.T) {
			checked := 0
			for _, dir := range corpusDirs {
				for _, base := range corpusBases {
					codePath := joinDir(dir, base+"."+p.ext)
					want := principledMerge(p.raw(codePath))
					got := p.testPaths(codePath)
					if !candidatesEqual(want, got) {
						t.Errorf("%s: TestPaths(%q) = %+v, principled rule wants %+v", p.name, codePath, got, want)
					}
					checked++
				}
			}
			if checked == 0 {
				t.Fatal("empty corpus — property was not actually exercised")
			}
		})
	}
}

// --- The synthetic violator: proof the property check can fail. ---
//
// violatorPlugin is a stand-in for a FUTURE plugin (a hypothetical Python
// spec/ family, or a JS full-mirror form) that emits a non-vacuous,
// non-sibling candidate carrying real directory evidence (a literal "x"
// path segment that never degenerates) which string-collides with a
// WEAKER, vacuous form of the same source. Its TestPaths calls the REAL
// production dedupeCandidates, exactly as every shipped plugin does.
type violatorPlugin struct{}

func (violatorPlugin) TestPaths(codePath string) []TestCandidate {
	_, base, _ := splitPath(codePath)
	strong := TestCandidate{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 1}
	weak := TestCandidate{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 4}
	return dedupeCandidates([]TestCandidate{strong, weak})
}

// violatorRawForms is this file's model of the SAME two candidates, tagged
// per the principled rule: strong asserts real evidence (its "x" segment is
// a literal, never-degenerating directory component); weak is vacuous (no
// real evidence — a stand-in for a degenerate mirror/stripped/flat form).
func violatorRawForms(codePath string) []rawForm {
	_, base, _ := splitPath(codePath)
	return []rawForm{
		{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 1, Vacuous: false},
		{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 4, Vacuous: true},
	}
}

// TestSyntheticViolatorIsDetected proves the equivalence check has teeth: it
// is not a tautology that only ever passes. dedupeCandidates' max-with-
// sibling-exemption rule UNDERSTATES the violator's strong (Rank 1, real
// evidence) claim to Rank 4 (dragged down by the weaker, vacuous collider) —
// exactly the "inflated/understated rank crosses into a different source's
// comparison" failure mode described in the task: understating source A's
// evidence can hand a strict win to some other source B in
// reposcan.demoteAmbiguousPairings, which is a MISPAIRING, not an honest
// demotion. The principled rule (independently computed here) instead keeps
// the strong claim's Rank 1. If a real future plugin ever does this,
// production's dedupeCandidates result and this file's principled computation
// diverge — this test asserts that divergence is real and gets caught, not
// papered over.
func TestSyntheticViolatorIsDetected(t *testing.T) {
	codePath := "irrelevant/x.ext" // violatorPlugin ignores the directory entirely by construction
	got := violatorPlugin{}.TestPaths(codePath)
	principled := principledMerge(violatorRawForms(codePath))

	if candidatesEqual(got, principled) {
		t.Fatalf("expected the property check to FLAG the synthetic violator (production %+v vs principled %+v), but they matched — the check cannot detect a future plugin that breaks the invariant", got, principled)
	}

	// Pin the exact numbers so the reasoning above is falsifiable, not just
	// "not equal": production understates the strong claim to the weak
	// collider's Rank 4; the principled rule keeps it at Rank 1.
	wantPath := filepath.Join("spec", "x", "test_x.ext")
	if len(got) != 1 || got[0].Path != wantPath || got[0].Rank != 4 {
		t.Fatalf("production dedupeCandidates(violator) = %+v, want [{%s 4}]", got, wantPath)
	}
	if len(principled) != 1 || principled[0].Path != wantPath || principled[0].Rank != 1 {
		t.Fatalf("principledMerge(violator) = %+v, want [{%s 1}]", principled, wantPath)
	}
}

// --- The sibling exemption is load-bearing in both directions. ---
//
// dedupeWithoutSiblingExemption is a LOCAL, test-only reimplementation of
// dedupeCandidates with the `if minRank[p] == 0 { rank = 0 }` line deleted —
// i.e. pure "attribute the max rank among every colliding candidate,
// sibling or not." It exists only to demonstrate the exemption changes
// behavior on a real collision shape; production code is untouched.
func dedupeWithoutSiblingExemption(cands []TestCandidate) []TestCandidate {
	firstIdx := map[string]int{}
	maxRank := map[string]int{}
	var order []string
	for _, c := range cands {
		if _, ok := firstIdx[c.Path]; !ok {
			firstIdx[c.Path] = len(order)
			order = append(order, c.Path)
			maxRank[c.Path] = c.Rank
			continue
		}
		if c.Rank > maxRank[c.Path] {
			maxRank[c.Path] = c.Rank
		}
	}
	out := make([]TestCandidate, len(order))
	for i, p := range order {
		out[i] = TestCandidate{Path: p, Rank: maxRank[p]}
	}
	return out
}

// TestSiblingExemptionIsLoadBearing exercises the exact collision shape
// behind the "requests" end-to-end fixture in
// internal/reposcan/candidate_pairing_test.go (tests/utils.py: a source file
// that lives IN the parallel test root itself, whose sibling form
// string-collides with its own degenerate stripped/flat forms) and shows
// that dedupeCandidates (WITH the exemption) and
// dedupeWithoutSiblingExemption (without it) disagree on it — i.e. deleting
// the exemption is not a no-op. The same asymmetry is what
// internal/lang/convention_test.go pins directly at the unit level (the
// "sibling collides with a degenerate lower-specificity form" and "sibling
// wins regardless of listed order" subcases of
// TestDedupeCandidatesAttributesLeastSpecificRank) and what
// TestEnumeratePairingConventions's
// "python: requests tests/test_utils.py — pre-existing tests/utils.py
// misclassification wins the strict-rank tiebreak" subcase pins end-to-end.
func TestSiblingExemptionIsLoadBearing(t *testing.T) {
	raw := pythonRawForms("tests/utils.py")
	var cands []TestCandidate
	for _, r := range raw {
		cands = append(cands, TestCandidate{Path: r.Path, Rank: r.Rank})
	}

	withExemption := dedupeCandidates(cands)
	withoutExemption := dedupeWithoutSiblingExemption(cands)

	if candidatesEqual(withExemption, withoutExemption) {
		t.Fatalf("expected the sibling exemption to change the result for tests/utils.py's collision (with=%+v, without=%+v) — if these ever match, the exemption is dead code and the referenced convention_test.go/candidate_pairing_test.go subcases must be re-checked", withExemption, withoutExemption)
	}

	const wantPath = "tests/test_utils.py"
	found := false
	for _, c := range withExemption {
		if c.Path == wantPath {
			found = true
			if c.Rank != 0 {
				t.Errorf("with exemption: %s Rank = %d, want 0 (sibling must win)", wantPath, c.Rank)
			}
		}
	}
	if !found {
		t.Fatalf("with exemption: %s not present in %+v", wantPath, withExemption)
	}

	found = false
	for _, c := range withoutExemption {
		if c.Path == wantPath {
			found = true
			if c.Rank == 0 {
				t.Errorf("without exemption: %s Rank = 0, want it demoted (this is the regression the exemption prevents)", wantPath)
			}
		}
	}
	if !found {
		t.Fatalf("without exemption: %s not present in %+v", wantPath, withoutExemption)
	}
}
