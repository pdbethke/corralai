// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testmap"
)

// rspecUnitMap is a Ruby unit-evidence map read by the rspec row: each listed
// spec file a unit, lib/a.rb executed by the units in covering.
func rspecUnitMap(listed []string, covering ...string) testmap.Map {
	m := testmap.Map{Schema: testmap.Schema, Lang: "ruby", Runner: "rspec", Method: testmap.MethodUnitEvidence,
		Of: len(listed), Command: []string{"bundle", "exec", "rspec"},
		Sources: map[string]testmap.Source{"lib/a.rb": {HasStatements: true}},
		Tests:   map[string]testmap.Node{}}
	for _, u := range listed {
		m.Tests[u] = testmap.Node{ID: u, Grain: testmap.GrainUnit, Files: []string{u}}
	}
	for _, u := range covering {
		m.Edges = append(m.Edges, testmap.Edge{Test: u, Source: "lib/a.rb", Executed: 2})
	}
	return m
}

// rspecRecordingJail records every rspec run; the unmutated code passes and
// any mutant is killed. Non-rspec runs (the compile gate) pass unrecorded.
type rspecRecordingJail struct {
	codePath, code string
	cmds           [][]string
}

func (j *rspecRecordingJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	if !slices.Contains(cmd, "rspec") {
		return true, nil
	}
	j.cmds = append(j.cmds, slices.Clone(cmd))
	return files[j.codePath] == j.code, nil
}

func rspecScorer(t *testing.T, jail adequacy.Jail) (JailScorer, string) {
	t.Helper()
	const codePath, devTest = "lib/a.rb", "spec/a_spec.rb"
	m := rspecUnitMap([]string{devTest, "spec/b_spec.rb"}, devTest)
	p, _ := lang.ByName("ruby")
	r, err := lang.RunnerByID(p, "rspec")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := testmap.Select(m, r, m.Command, "/repo", codePath, devTest)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	s := JailScorer{
		Jail: jail, Lang: "ruby", Selection: sel, SelectionMap: m, RepoDir: "/repo", DevTestPath: devTest,
		BaseFiles: map[string]string{codePath: "X = 1\n", devTest: "RSpec.describe('a') { it('x') {} }\n", "spec/b_spec.rb": ""},
	}
	return s, codePath
}

// A salvaged authored pass (hashie, 2026-11-10): the writer's test had one
// example that failed on the unmutated code, so salvage excluded it by name
// and re-scored. The excluding command used to reach the scorer as the run's
// base, the rspec row refuses to build a unit command from argv carrying a
// name filter, and the pass silently fell back to the WHOLE suite, while the
// verdict, judging the unsalvaged base, recorded "proven by the authored test
// alone". The salvaged pass must run exactly what an unsalvaged pass runs (the
// authored test alone) with the failing examples excluded from it.
func TestASalvagedAuthoredPassStillRunsTheAuthoredTestAlone(t *testing.T) {
	jail := &rspecRecordingJail{code: "X = 1\n"}
	s, codePath := rspecScorer(t, jail)
	jail.codePath = codePath
	authored := s.AuthoredTestPath(codePath)

	ids := []string{"A rejects bad input"}
	base := "bundle exec rspec"
	deselected, ok := lang.DeselectCmd(mustPlugin(t, "ruby"), lang.Invocation{}.WithArgv(adequacy.ShellSplit(base)), ids)
	if !ok {
		t.Fatal("fixture: the rspec row must deselect")
	}
	mutants := []adequacy.Mutant{{ID: "m1", Replace: "X = 2\n"}, {ID: "m2", Replace: "X = 3\n"}}
	if _, err := s.ScoreAuthoredDeselected(context.Background(), codePath, "X = 1\n", "RSpec.describe('a') {}\n", mutants, base, adequacy.ShellJoin(deselected), ids); err != nil {
		t.Fatal(err)
	}
	want := []string{"bundle", "exec", "rspec", authored, "-E", `\A(?!(?:A rejects bad input)\z)`}
	if len(jail.cmds) < 3 {
		t.Fatalf("want baseline + canary + survivors, ran %d: %v", len(jail.cmds), jail.cmds)
	}
	for i, c := range jail.cmds {
		if !reflect.DeepEqual(c, want) {
			t.Errorf("run %d = %v; a salvaged authored pass must run the authored test alone, failing examples excluded: %v", i, c, want)
		}
	}
}

// The verdict's claim for the same run: AuthoredAlone is spec-derived, so it
// is only true if a salvaged pass can never quietly widen. Asserted here on
// the scorer and the verdict together, for the Ruby unit grain that broke.
func TestTheVerdictAndTheSalvagedPassAgreeOnAlone(t *testing.T) {
	jail := &rspecRecordingJail{code: "X = 1\n"}
	s, codePath := rspecScorer(t, jail)
	jail.codePath = codePath
	rs := RunSpec{Lang: "ruby", CodePath: codePath, DevTestPath: s.DevTestPath, TestCmd: "bundle exec rspec",
		Selection: s.Selection, SelectionMap: s.SelectionMap, RepoDir: s.RepoDir, AuthoredTestPath: s.AuthoredTestPath(codePath)}
	if !verdictFromSpec(rs).TestSelection.AuthoredAlone {
		t.Fatal("fixture: an rspec unit selection proves alone")
	}
	ids := []string{"A rejects bad input"}
	deselected, _ := lang.DeselectCmd(mustPlugin(t, "ruby"), rs.inv(), ids)
	if _, err := s.ScoreAuthoredDeselected(context.Background(), codePath, "X = 1\n", "x", []adequacy.Mutant{{ID: "m1", Replace: "X = 2\n"}}, rs.TestCmd, adequacy.ShellJoin(deselected), ids); err != nil {
		t.Fatal(err)
	}
	for _, c := range jail.cmds {
		if slices.Contains(c, "spec/b_spec.rb") || !slices.Contains(c, rs.AuthoredTestPath) || len(c) == len(deselected) {
			t.Errorf("the verdict says alone, but the pass ran %v", c)
		}
	}
}

func mustPlugin(t *testing.T, name string) lang.Plugin {
	t.Helper()
	p, ok := lang.ByName(name)
	if !ok {
		t.Fatalf("no %s plugin", name)
	}
	return p
}
