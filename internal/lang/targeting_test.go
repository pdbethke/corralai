// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// The shared resolver matches exactly or by "::" suffix and nothing more:
// a name meaning a nested group must not resolve onto a jest or vitest test
// whose title merely holds the same words.
func TestResolveListedTestHasNoFullNameFallback(t *testing.T) {
	if got, ok := ResolveListedTest([]string{"Calc::adds x"}, "Calc::adds::x"); ok {
		t.Errorf("a nested name resolved onto %q, a different test", got)
	}
}

// mocha and rspec list a nested test as "Outer Inner::leaf" (idFromFull),
// while the static JavaScript lister names it "Outer::Inner::leaf". For
// those rows only, a name that matches nothing exactly resolves onto the one
// id whose full name it spells; the ambiguity rule still holds.
func TestResolveForRowFallsBackToFullNameOnSpaceJoinedRows(t *testing.T) {
	js, _ := ByName("javascript")
	mocha, err := RunnerByID(js, "mocha")
	if err != nil {
		t.Fatal(err)
	}
	jest, _ := RunnerByID(js, "jest")
	for _, c := range []struct {
		r      Runner
		listed []string
		name   string
		want   string
	}{
		{mocha, []string{"Calc neg::adds", "Calc::adds"}, "Calc::neg::adds", "Calc neg::adds"},
		{mocha, []string{"Calc neg::adds", "Calc::adds"}, "Calc::adds", "Calc::adds"},
		{mocha, []string{"A B::c", "A::B c"}, "A::B::c", ""},
		{mocha, []string{markNotAlone("Calc neg::adds")}, "Calc::neg::adds", markNotAlone("Calc neg::adds")},
		{mocha, []string{"Calc neg::adds"}, "Calc::neg::subs", ""},
		{jest, []string{"Calc::adds x"}, "Calc::adds::x", ""},
	} {
		got, ok := resolveForRow(c.r, c.listed, c.name)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: resolveForRow(%q, %q) = %q, %v; want %q", c.r.ID, c.listed, c.name, got, ok, c.want)
		}
	}
}
