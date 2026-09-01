// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"sort"
	"strings"
	"testing"
)

// ForSpan must select the SAME tests it always did — ordering them by how much
// of the mutated span each one reaches changes the sequence and nothing else.
// The set is the measurement; the order is only what it costs.
func TestForSpanOrdersKillLikeliestFirstWithoutChangingTheSet(t *testing.T) {
	p, _ := ByName("python")
	ts := p.(TestSelector)

	span := LineRange{Start: 10, End: 19} // ten lines
	sel := Selection{
		Base:  []string{"pytest"},
		Tests: []string{"t/a.py::one", "t/b.py::two", "t/c.py::three", "t/d.py::four"},
		Lines: map[string][]LineRange{
			// one line of the span
			"t/a.py::one": {{Start: 1, End: 10}},
			// the whole span
			"t/b.py::two": {{Start: 10, End: 19}},
			// three lines
			"t/c.py::three": {{Start: 17, End: 25}},
			// none of it — must not be selected at all
			"t/d.py::four": {{Start: 40, End: 50}},
		},
		Method: "coverage-context",
	}
	sel.Cmd = append(append([]string{}, sel.Base...), sel.Tests...)

	_, got, rule := ts.ForSpan(sel, span)
	if rule != SpanRuleLines {
		t.Fatalf("rule = %q, want %q", rule, SpanRuleLines)
	}

	// THE SET: exactly the tests whose coverage reaches the span.
	set := append([]string{}, got...)
	sort.Strings(set)
	if strings.Join(set, ",") != "t/a.py::one,t/b.py::two,t/c.py::three" {
		t.Fatalf("selected set = %v — ordering must not change WHICH tests run", set)
	}

	// THE ORDER: most of the span first.
	if strings.Join(got, ",") != "t/b.py::two,t/c.py::three,t/a.py::one" {
		t.Errorf("order = %v, want the test covering most of the span first", got)
	}
}

// Two tests reaching the span equally must still order deterministically — a
// signed record cannot have two runs of the same evidence disagree.
func TestForSpanOrderIsTotalAndDeterministic(t *testing.T) {
	p, _ := ByName("python")
	ts := p.(TestSelector)
	span := LineRange{Start: 5, End: 6}
	sel := Selection{
		Base:  []string{"pytest"},
		Tests: []string{"t/z.py::z", "t/a.py::a"},
		Lines: map[string][]LineRange{
			"t/z.py::z": {{Start: 5, End: 6}},
			"t/a.py::a": {{Start: 5, End: 6}},
		},
		Method: "coverage-context",
	}
	sel.Cmd = append(append([]string{}, sel.Base...), sel.Tests...)
	_, first, _ := ts.ForSpan(sel, span)
	_, second, _ := ts.ForSpan(sel, span)
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("two calls disagreed: %v vs %v", first, second)
	}
	if strings.Join(first, ",") != "t/a.py::a,t/z.py::z" {
		t.Errorf("tie broken as %v, want the id order", first)
	}
}
