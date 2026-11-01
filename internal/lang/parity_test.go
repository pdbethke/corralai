// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// Every language has every capability, or a written reason it does not. An
// exemption for a capability the language now has fails too, so the list
// only shrinks (the same staleness rule repoindex's langSpecs exemptions
// follow).
func TestEveryLanguageHasEveryCapabilityOrSaysWhy(t *testing.T) {
	keys := map[string]bool{}
	for _, c := range Capabilities {
		keys[c.Key] = true
	}
	exempt := map[[2]string]bool{}
	for _, e := range ParityExemptions {
		if e.Reason == "" {
			t.Errorf("%s/%s: an exemption needs a reason", e.Lang, e.Capability)
		}
		if _, ok := ByName(e.Lang); !ok || !keys[e.Capability] {
			t.Errorf("%s/%s: exemption names no registered language or capability", e.Lang, e.Capability)
		}
		if exempt[[2]string{e.Lang, e.Capability}] {
			t.Errorf("%s/%s: exempted twice", e.Lang, e.Capability)
		}
		exempt[[2]string{e.Lang, e.Capability}] = true
	}
	for _, n := range Names() {
		p, _ := ByName(n)
		if _, ok := paritySamples[n]; !ok {
			t.Errorf("%s has no parity sample: critic-list cannot be judged", n)
		}
		for _, c := range Capabilities {
			has, ex := c.Has(p), exempt[[2]string{n, c.Key}]
			switch {
			case !has && !ex:
				t.Errorf("%s lacks %q and has no exemption saying why", n, c.Title)
			case has && ex:
				t.Errorf("%s now has %q: delete its exemption", n, c.Title)
			}
		}
	}
}

// The negative control for the selection capability, which is judged on
// the code that grades by evidence rather than declared: a language has it
// only when it produces evidence (per-test, or coverage the unit pass runs
// per unit) AND some row can run a chosen set (TestsCmd or UnitsCmd). A row
// with neither command gives it to nobody, whatever the plugin produces;
// a plugin that produces no evidence gets it from no row.
func TestSelectionCapabilityNeedsEvidenceAndARowThatRunsASet(t *testing.T) {
	ruby, _ := ByName("ruby")
	bare := Runner{ID: "parity-bare", Languages: []string{"ruby"}}
	if selectionCapable(ruby, []Runner{bare}) {
		t.Error("a row with no TestsCmd and no UnitsCmd must not give selection")
	}
	units := bare
	units.UnitsCmd = func(argv, u []string) ([]string, bool) { return append(argv, u...), true }
	if !selectionCapable(ruby, []Runner{bare, units}) {
		t.Error("positive control: a coverage-reporting language with a UnitsCmd row selects")
	}
	tests := bare
	tests.TestsCmd = func(argv, ids []string) ([]string, bool) { return append(argv, ids...), true }
	if !selectionCapable(ruby, []Runner{tests}) {
		t.Error("positive control: a TestsCmd row selects too")
	}
	other := units
	other.Languages = []string{"php"}
	if selectionCapable(ruby, []Runner{bare, other}) {
		t.Error("another language's row gives ruby nothing")
	}
	if selectionCapable(struct{ Plugin }{ruby}, []Runner{units}) {
		t.Error("a plugin with no evidence form (neither TestEvidence nor CoverageReporter) must not select")
	}
}
