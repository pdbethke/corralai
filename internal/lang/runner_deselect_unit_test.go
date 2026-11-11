// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

// deselectUnitBases is one operator command and one unit per row that both
// salvages (DeselectCmd) and runs units (UnitsCmd). The test below derives
// the rows from the table and fails on a row missing here, so a new row
// cannot skip the check by not being listed.
var deselectUnitBases = map[string]struct {
	argv []string
	unit string
}{
	"go-test":   {[]string{"go", "test", "./..."}, "example.com/m/pkg"},
	"jest":      {[]string{"jest"}, "tests/a.test.js"},
	"vitest":    {[]string{"vitest", "run"}, "tests/a.test.ts"},
	"node-test": {[]string{"node", "--test"}, "tests/a.test.js"},
	"rspec":     {[]string{"bundle", "exec", "rspec"}, "spec/a_corral_spec.rb"},
	"phpunit":   {[]string{"vendor/bin/phpunit"}, "tests/ACorralTest.php"},
}

// A salvaged authored pass is built from the run's base like any other
// (advpool's JailScorer.ScoreAuthoredDeselected) and then has the failing
// examples excluded by the row's own DeselectCmd. A row that salvages and
// runs units must be able to exclude from its own unit command: if it
// cannot, that row's salvage records nothing where an unsalvaged pass would
// have proven alone. (rspec's unit command refused argv carrying the -E an
// exclusion adds, which is how hashie's salvaged passes ran the whole suite
// under a verdict saying "alone", 2026-11-10; this order, units first, is
// the one that works.)
func TestEveryUnitRowCanExcludeFromItsOwnUnitCommand(t *testing.T) {
	for _, r := range runners {
		if r.DeselectCmd == nil || r.UnitsCmd == nil {
			continue
		}
		b, ok := deselectUnitBases[r.ID]
		if !ok {
			t.Errorf("%s salvages and runs units but has no entry in deselectUnitBases", r.ID)
			continue
		}
		unit, ok := r.UnitsCmd(b.argv, []string{b.unit})
		if !ok {
			t.Errorf("%s: fixture: UnitsCmd(%q, [%q]) refused", r.ID, b.argv, b.unit)
			continue
		}
		out, ok := r.DeselectCmd(unit, []string{"A rejects bad input"})
		if !ok {
			t.Errorf("%s: DeselectCmd cannot exclude from its own unit command %q", r.ID, unit)
			continue
		}
		if len(out) <= len(unit) {
			t.Errorf("%s: DeselectCmd(%q) = %q excluded nothing", r.ID, unit, out)
		}
	}
	for id := range deselectUnitBases {
		if !slices.ContainsFunc(runners, func(r Runner) bool { return r.ID == id && r.DeselectCmd != nil && r.UnitsCmd != nil }) {
			t.Errorf("deselectUnitBases names %q, which is no row that both salvages and runs units", id)
		}
	}
}
