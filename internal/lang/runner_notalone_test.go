// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// A test the runner reports but corral cannot run alone still makes a name
// ambiguous: dropping it from the list would let a critic's name resolve to
// the one remaining test that shares it, and that test's kills would refute
// a claim about the other. Each case is a list where the name must NOT
// resolve, though exactly one runnable test carries it.
func TestATestThatCannotRunAloneStillMakesANameAmbiguous(t *testing.T) {
	rb, _ := ByName("ruby")
	js, _ := ByName("javascript")
	// rspec: a namespaced group (the usual Rails idiom) beside a plain one.
	rspecOut := `{"examples":[
	 {"description":"adds","full_description":"MyApp::Calc adds"},
	 {"description":"adds","full_description":"helpers adds"}]}`
	if id, ok := ResolveForRun(rb, []string{"rspec"}, ParseTestList(rb, []string{"rspec"}, rspecOut), "adds"); ok {
		t.Errorf("rspec: adds resolved to %q; MyApp::Calc also has one", id)
	}
	// node: a subtest of a test (unrunnable alone) beside a suite's test.
	nodeTap := "TAP version 13\n# Subtest: parent\n    # Subtest: adds\n    ok 1 - adds\n      ---\n      type: 'test'\n      ...\n    1..1\nok 1 - parent\n  ---\n  type: 'test'\n  ...\n# Subtest: Calc\n    # Subtest: adds\n    ok 1 - adds\n      ---\n      type: 'test'\n      ...\n    1..1\nok 2 - Calc\n  ---\n  type: 'suite'\n  ...\n"
	argv := []string{"node", "--test"}
	if id, ok := ResolveForRun(js, argv, ParseTestList(js, argv, nodeTap), "adds"); ok {
		t.Errorf("node: adds resolved to %q; parent's subtest is also adds", id)
	}
	// jest: a title holding the id separator beside a plain one.
	jestOut := `{"testResults":[{"assertionResults":[
	 {"ancestorTitles":["A::B"],"title":"adds"},
	 {"ancestorTitles":["C"],"title":"adds"}]}]}`
	jargv := []string{"npx", "jest"}
	if id, ok := ResolveForRun(js, jargv, ParseTestList(js, jargv, jestOut), "adds"); ok {
		t.Errorf("jest: adds resolved to %q; A::B also has one", id)
	}
}

// A namespaced rspec group is listed and runnable: its full description is
// "MyApp::Calc adds", which -E must match exactly.
func TestRspecNamespacedGroupRunsAlone(t *testing.T) {
	rb, _ := ByName("ruby")
	argv := []string{"rspec"}
	ids := ParseTestList(rb, argv, `{"examples":[{"description":"adds","full_description":"MyApp::Calc adds"}]}`)
	id, ok := ResolveForRun(rb, argv, ids, "adds")
	if !ok {
		t.Fatalf("adds did not resolve over %q", ids)
	}
	cmd, _ := SingleTestCmd(rb, argv, "spec/calc_spec.rb", id)
	if want := `\AMyApp::Calc adds\z`; len(cmd) < 3 || cmd[2] != want {
		t.Errorf("single = %q, want -E %s", cmd, want)
	}
}
