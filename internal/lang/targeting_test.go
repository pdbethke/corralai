// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// mocha and rspec list a nested test as "Outer Inner::leaf" (idFromFull),
// while the static JavaScript lister names it "Outer::Inner::leaf", the form
// jest, vitest and node list. When nothing matches exactly or as a suffix,
// the name resolves onto the one id whose full name it spells; the
// ambiguity rule still holds.
func TestResolveListedTestFallsBackToFullName(t *testing.T) {
	for _, c := range []struct {
		listed []string
		name   string
		want   string
	}{
		{[]string{"Calc neg::adds", "Calc::adds"}, "Calc::neg::adds", "Calc neg::adds"},
		{[]string{"Calc neg::adds", "Calc::adds"}, "Calc::adds", "Calc::adds"},
		{[]string{"A B::c", "A::B c"}, "A::B::c", ""},
		{[]string{markNotAlone("Calc neg::adds")}, "Calc::neg::adds", markNotAlone("Calc neg::adds")},
		{[]string{"Calc neg::adds"}, "Calc::neg::subs", ""},
	} {
		got, ok := ResolveListedTest(c.listed, c.name)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("ResolveListedTest(%q, %q) = %q, %v; want %q", c.listed, c.name, got, ok, c.want)
		}
	}
}
