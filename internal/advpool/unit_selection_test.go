// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testmap"
)

// vitestUnitMap is a TypeScript unit-evidence map read by the vitest row:
// each listed unit a node, src/one.ts executed by the units in covering.
func vitestUnitMap(listed []string, covering ...string) testmap.Map {
	m := testmap.Map{Schema: testmap.Schema, Lang: "typescript", Runner: "vitest", Method: testmap.MethodUnitEvidence,
		Of: len(listed), Command: []string{"vitest", "run"},
		Sources: map[string]testmap.Source{"src/one.ts": {HasStatements: true}, "src/two.ts": {HasStatements: true}},
		Tests:   map[string]testmap.Node{}}
	for _, u := range listed {
		m.Tests[u] = testmap.Node{ID: u, Grain: testmap.GrainUnit, Files: []string{u}}
	}
	for _, u := range covering {
		m.Edges = append(m.Edges, testmap.Edge{Test: u, Source: "src/one.ts", Executed: 2})
	}
	return m
}

// unitRunSpec is the RunSpec a scan hands the pool for codePath, its
// Selection made by the engine from m exactly as the scan makes it.
func unitRunSpec(t *testing.T, m testmap.Map, codePath, devTestPath string) RunSpec {
	t.Helper()
	p, _ := lang.ByName("typescript")
	r, err := lang.RunnerByID(p, m.Runner)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := testmap.Select(m, r, m.Command, "/repo", codePath, devTestPath)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return RunSpec{Lang: "typescript", CodePath: codePath, DevTestPath: devTestPath, TestCmd: "vitest run",
		Selection: sel, SelectionMap: m, RepoDir: "/repo",
		// What the scan's scorer resolves for a workspace with no colliding
		// file (JailScorer.AuthoredTestPath).
		AuthoredTestPath: authoredTestPath(AuthoredLanding{}, codePath, devTestPath, nil)}
}

// The dev pass of a language graded by units runs exactly the selected
// units: the plugin has no per-test evidence (lang.TestEvidence), and that
// is no longer a reason to grade it whole-suite — the row comes from the
// map, as Python's does.
func TestAUnitSelectionNarrowsTheDevPass(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "tests/three.test.ts"}, "tests/one.test.ts")
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	want := []string{"vitest", "run", "tests/one.test.ts"}
	if got := DevCommandArgv(rs.Selection, rs.SelectionMap, rs.Lang, adequacy.ShellSplit(rs.TestCmd), rs.RepoDir, rs.DevTestPath); !reflect.DeepEqual(got, want) {
		t.Errorf("dev command = %v, want the selected unit %v", got, want)
	}
	v := verdictFromSpec(rs)
	if v.TestSelection.Method != testmap.MethodUnitEvidence || v.TestSelection.Selected != 1 || v.TestSelection.Of != 3 || v.TestSelection.Fallback != "" {
		t.Errorf("verdict selection = %+v", v.TestSelection)
	}
	if !v.TestSelection.AuthoredAlone {
		t.Errorf("the authored unit runs alone through the vitest row; AuthoredAlone must say so: %+v", v.TestSelection)
	}
}

// Review Focus 1, on the record: the number of units a selection carries
// only because they reported no evidence reaches the verdict at BOTH
// construction sites (the converged verdict and the timed-out one) and its
// wire form, which is what the ledger stores.
func TestUnreportedCountReachesTheVerdict(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "tests/three.test.ts"}, "tests/one.test.ts")
	n := m.Tests["tests/three.test.ts"]
	n.Err = "could not run: the jail could not start it"
	m.Tests["tests/three.test.ts"] = n
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	if !reflect.DeepEqual(rs.Selection.Tests, []string{"tests/one.test.ts", "tests/three.test.ts"}) {
		t.Fatalf("fixture: selection %v must carry the unreported unit", rs.Selection.Tests)
	}

	converged := verdictFromSpec(rs)
	timedOut := (&Driver{}).timeoutVerdict(&runState{rs: rs})
	for name, v := range map[string]Verdict{"converged": converged, "timeout": timedOut} {
		if v.TestSelection.Unreported != 1 || v.TestSelection.Selected != 2 {
			t.Errorf("%s verdict: %+v, want 2 selected of which 1 unreported", name, v.TestSelection)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"unreported":1`) {
			t.Errorf("%s verdict_json does not carry the count: %s", name, b)
		}
	}

	// Negative control: every unit reported, nothing added, nothing counted.
	clean := unitRunSpec(t, vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts"}, "tests/one.test.ts"), "src/one.ts", "tests/one.test.ts")
	if v := verdictFromSpec(clean); v.TestSelection.Unreported != 0 {
		t.Errorf("no unit failed, yet Unreported = %d", v.TestSelection.Unreported)
	}
}

// When the engine refuses the authored pass's command — the authored unit's
// own vitest filter would also run a listed unit nobody named — the pass
// runs the run's whole command instead. That is a different measurement, so
// the verdict says it (AuthoredFallback) and never claims the authored test
// ran alone. Before this the refusal reached only a log line.
func TestAnAuthoredRefusalIsDisclosedOnTheVerdict(t *testing.T) {
	authored := authoredTestPath(AuthoredLanding{}, "src/one.ts", "tests/one.test.ts", nil)
	collides := "elsewhere/" + filepath.Base(authored)
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", collides}, "tests/one.test.ts")
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")

	v := verdictFromSpec(rs)
	if v.TestSelection.AuthoredAlone {
		t.Errorf("the engine refused the authored-alone command; AuthoredAlone must be false: %+v", v.TestSelection)
	}
	if !strings.Contains(v.TestSelection.AuthoredFallback, collides) {
		t.Errorf("AuthoredFallback = %q, want the refusal naming %s", v.TestSelection.AuthoredFallback, collides)
	}
	if v.TestSelection.Fallback != "" || v.TestSelection.Method != testmap.MethodUnitEvidence {
		t.Errorf("the DEV pass still ran its selection; only the authored pass fell back: %+v", v.TestSelection)
	}
	// The scorer runs what the verdict says it ran.
	s := JailScorer{Lang: rs.Lang, Selection: rs.Selection, SelectionMap: m, RepoDir: rs.RepoDir, DevTestPath: rs.DevTestPath}
	if got := s.authoredCmd(rs.CodePath, adequacy.ShellSplit(rs.TestCmd)); !reflect.DeepEqual(got, []string{"vitest", "run"}) {
		t.Errorf("authored command = %v, want the whole command the verdict discloses", got)
	}

	// Negative control: nothing collides, nothing to disclose.
	ok := unitRunSpec(t, vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts"}, "tests/one.test.ts"), "src/one.ts", "tests/one.test.ts")
	if v := verdictFromSpec(ok); v.TestSelection.AuthoredFallback != "" || !v.TestSelection.AuthoredAlone {
		t.Errorf("no refusal, yet %+v", v.TestSelection)
	}
}

// An UNCOVERED file's dev pass runs its paired test's unit alone. When the
// engine refuses that command, the dev pass runs the whole command, and the
// verdict says why in Fallback while staying Uncovered: the evidence still
// says no unit executes the file, so the rate stays withheld.
func TestAnUncoveredDevRefusalIsDisclosedOnTheVerdict(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "more/two.test.ts"}, "tests/one.test.ts")
	rs := unitRunSpec(t, m, "src/two.ts", "tests/two.test.ts")
	if len(rs.Selection.Tests) != 0 || rs.Selection.Method == "" {
		t.Fatalf("fixture: src/two.ts must be uncovered: %+v", rs.Selection)
	}
	if got := DevCommandArgv(rs.Selection, m, rs.Lang, adequacy.ShellSplit(rs.TestCmd), rs.RepoDir, rs.DevTestPath); !reflect.DeepEqual(got, []string{"vitest", "run"}) {
		t.Fatalf("fixture: the paired unit coruns more/two.test.ts, so the dev pass runs the whole command; got %v", got)
	}
	v := verdictFromSpec(rs)
	if !v.Uncovered || !strings.Contains(v.TestSelection.Fallback, "more/two.test.ts") {
		t.Errorf("verdict = Uncovered %v, Fallback %q; want uncovered, with the refusal naming more/two.test.ts", v.Uncovered, v.TestSelection.Fallback)
	}
}

// No defaults: a selection whose map names no runner row builds no command
// (the dev pass runs the whole command), and the verdict names the missing
// row rather than claiming a narrowed measurement.
func TestASelectionWithNoRunnerRowIsDisclosed(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts"}, "tests/one.test.ts")
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	rs.SelectionMap.Runner = ""
	if got := DevCommandArgv(rs.Selection, rs.SelectionMap, rs.Lang, adequacy.ShellSplit(rs.TestCmd), rs.RepoDir, rs.DevTestPath); !reflect.DeepEqual(got, []string{"vitest", "run"}) {
		t.Errorf("dev command = %v, want the whole command", got)
	}
	v := verdictFromSpec(rs)
	if !strings.Contains(v.TestSelection.Fallback, "names no runner row") || v.TestSelection.AuthoredAlone {
		t.Errorf("verdict selection = %+v, want a fallback naming the missing runner row", v.TestSelection)
	}
}

// Fix round 1. The verdict judges the authored test at the path the scorer
// really writes it to. The scorer renames a path that collides with a file
// already in the workspace, and for vitest a renamed file is a different
// unit, with different corun answers: the verdict computing its own path
// without the workspace said "alone" for a pass the scorer ran whole. The
// path is resolved once, by the scorer, and carried on the RunSpec.
func TestTheVerdictJudgesTheAuthoredPathTheScorerRuns(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "elsewhere/one_corral1.test.ts"}, "tests/one.test.ts")
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	s := JailScorer{Lang: rs.Lang, Selection: rs.Selection, SelectionMap: m, RepoDir: rs.RepoDir, DevTestPath: rs.DevTestPath,
		BaseFiles: map[string]string{"src/one.ts": "", "tests/one.test.ts": "", "tests/one_corral.test.ts": ""}}
	path := s.AuthoredTestPath(rs.CodePath)
	if path != "tests/one_corral1.test.ts" {
		t.Fatalf("fixture: the scorer must rename past the colliding file, got %q", path)
	}
	if rs.AuthoredTestPath == path {
		t.Fatal("fixture: the workspace-blind path must differ from the scorer's")
	}
	if v := verdictFromSpec(rs); !v.TestSelection.AuthoredAlone {
		t.Fatalf("fixture: the workspace-blind path coruns nothing, so it reads alone: %+v", v.TestSelection)
	}

	rs.AuthoredTestPath = path
	v := verdictFromSpec(rs)
	if v.TestSelection.AuthoredAlone || !strings.Contains(v.TestSelection.AuthoredFallback, "elsewhere/one_corral1.test.ts") {
		t.Errorf("verdict = %+v, want not alone, the refusal naming the unit the renamed file coruns", v.TestSelection)
	}
	if got := s.authoredCmd(rs.CodePath, adequacy.ShellSplit(rs.TestCmd)); !reflect.DeepEqual(got, []string{"vitest", "run"}) {
		t.Errorf("the scorer ran %v; the verdict says the whole command", got)
	}

	// No defaults: a repository run carrying no resolved path claims nothing.
	rs.AuthoredTestPath = ""
	if v := verdictFromSpec(rs); v.TestSelection.AuthoredAlone || !strings.Contains(v.TestSelection.AuthoredFallback, "no resolved authored test path") {
		t.Errorf("no resolved path: %+v", v.TestSelection)
	}
}

// The scan line says what a unit IS and why the added ones were added, so
// both reach the verdict from BOTH construction sites: the row's noun (never
// a default) and the first unreported unit's own reason, in selection order.
func TestUnitNounAndFirstReasonReachTheVerdict(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "tests/three.test.ts", "tests/four.test.ts"}, "tests/one.test.ts")
	for id, why := range map[string]string{"tests/three.test.ts": "could not run: the jail could not start it", "tests/four.test.ts": "timed out"} {
		n := m.Tests[id]
		n.Err = why
		m.Tests[id] = n
	}
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	for name, v := range map[string]Verdict{"converged": verdictFromSpec(rs), "timeout": (&Driver{}).timeoutVerdict(&runState{rs: rs})} {
		if v.TestSelection.Unreported != 2 {
			t.Errorf("%s: Unreported = %d, want 2", name, v.TestSelection.Unreported)
		}
		if v.TestSelection.UnitNoun != "test files" {
			t.Errorf("%s: UnitNoun = %q, want the vitest row's \"test files\"", name, v.TestSelection.UnitNoun)
		}
		if v.TestSelection.UnreportedReason != "timed out" {
			t.Errorf("%s: UnreportedReason = %q, want the first unreported unit in selection order (tests/four sorts before tests/three)", name, v.TestSelection.UnreportedReason)
		}
	}
	clean := unitRunSpec(t, vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts"}, "tests/one.test.ts"), "src/one.ts", "tests/one.test.ts")
	if v := verdictFromSpec(clean); v.TestSelection.UnreportedReason != "" {
		t.Errorf("nothing unreported, yet UnreportedReason = %q", v.TestSelection.UnreportedReason)
	}
}

// The baseline retry, on the record. A selection that left out units whose
// run failed unmutated (lang.Selection.Excluded) is a different exam from
// the one the evidence chose, so how many were left out, and the first
// one's reason, reach the verdict at both construction sites and its wire
// form. A selection that left out nothing counts nothing.
func TestExcludedRedUnitsReachTheVerdict(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "tests/three.test.ts"}, "tests/one.test.ts")
	n := m.Tests["tests/three.test.ts"]
	n.Err = "run failed: exit 1"
	m.Tests["tests/three.test.ts"] = n
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	rs.Selection.Excluded = []string{"tests/three.test.ts"}

	converged := verdictFromSpec(rs)
	timedOut := (&Driver{}).timeoutVerdict(&runState{rs: rs})
	for name, v := range map[string]Verdict{"converged": converged, "timeout": timedOut} {
		if v.TestSelection.Excluded != 1 || v.TestSelection.ExcludedReason != "run failed: exit 1" {
			t.Errorf("%s verdict: %+v, want 1 excluded with the unit's own reason", name, v.TestSelection)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"excluded":1`) || !strings.Contains(string(b), `"excluded_reason":"run failed: exit 1"`) {
			t.Errorf("%s verdict_json does not carry the exclusion: %s", name, b)
		}
	}
	rs.Selection.Excluded = nil
	if v := verdictFromSpec(rs); v.TestSelection.Excluded != 0 || v.TestSelection.ExcludedReason != "" {
		t.Errorf("nothing excluded, yet %+v", v.TestSelection)
	}
}

// The second retry, on the record: the units the evidence pass ran green
// and a red baseline named failing reach both construction sites by name,
// with why, after the units red in the evidence pass; the count holds both.
func TestUnitsRedInTheBaselineReachTheVerdictByName(t *testing.T) {
	m := vitestUnitMap([]string{"tests/one.test.ts", "tests/two.test.ts", "tests/three.test.ts"}, "tests/one.test.ts", "tests/two.test.ts")
	n := m.Tests["tests/three.test.ts"]
	n.Err = "run failed: exit 1"
	m.Tests["tests/three.test.ts"] = n
	rs := unitRunSpec(t, m, "src/one.ts", "tests/one.test.ts")
	rs.Selection.Excluded = []string{"tests/three.test.ts", "tests/two.test.ts"}
	rs.Selection.ExcludedWhy = map[string]string{"tests/two.test.ts": "red beside the others"}

	converged := verdictFromSpec(rs)
	timedOut := (&Driver{}).timeoutVerdict(&runState{rs: rs})
	for name, v := range map[string]Verdict{"converged": converged, "timeout": timedOut} {
		ts := v.TestSelection
		if ts.Excluded != 2 || ts.ExcludedReason != "run failed: exit 1" || ts.ExcludedRedInBaseline != "tests/two.test.ts: red beside the others" {
			t.Errorf("%s verdict: %+v", name, ts)
		}
	}
	rs.Selection.ExcludedWhy = nil
	if v := verdictFromSpec(rs); v.TestSelection.ExcludedRedInBaseline != "" {
		t.Errorf("no unit red in the baseline, yet %q", v.TestSelection.ExcludedRedInBaseline)
	}
}
