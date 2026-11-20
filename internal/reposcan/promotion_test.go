// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// PromotesByEvidence is judged by running the code promotion uses on each
// language's sample: the unit landing (UnitTestFile) and the source's own
// landing check (lang.CheckTestLanding). Rust's unit landing exists, but a
// Rust source with no separate test file lands on itself and is refused, so
// it promotes nothing; Swift has no unit landing.
func TestPromotesByEvidence(t *testing.T) {
	want := map[string]bool{
		"go": true, "python": true, "javascript": true, "typescript": true, "ruby": true, "php": true,
		"rust": false, "swift": false,
	}
	for _, n := range lang.Names() {
		p, _ := lang.ByName(n)
		w, ok := want[n]
		if !ok {
			t.Errorf("%s: no expectation; add it here and a promotion sample if it runs the unit pass", n)
			continue
		}
		if got := PromotesByEvidence(p); got != w {
			t.Errorf("PromotesByEvidence(%s) = %v, want %v", n, got, w)
		}
	}
}

// Every language the unit pass serves has a promotion sample: without one
// the judge would have nothing to run, and it refuses to guess.
func TestEveryUnitPassLanguageHasAPromotionSample(t *testing.T) {
	for _, n := range lang.Names() {
		p, _ := lang.ByName(n)
		if _, perTest := p.(lang.TestEvidence); perTest {
			continue
		}
		if _, ok := p.(lang.CoverageReporter); !ok {
			continue
		}
		if _, ok := promotionSamples[n]; !ok {
			t.Errorf("%s runs the unit pass but has no promotion sample", n)
		}
	}
}

// Each language lacking promotion lacks it for the reason its exemption
// states, not because its sample is broken: Swift's source landing is
// admitted and only the unit landing is missing; Rust's unit landing exists
// and only the source landing is refused.
func TestPromotionGapsAreWhereTheExemptionsSay(t *testing.T) {
	for _, tc := range []struct {
		lang                     string
		sourceAdmitted, unitLand bool
	}{
		{"swift", true, false},
		{"rust", false, true},
	} {
		p, _ := lang.ByName(tc.lang)
		s := promotionSamples[tc.lang]
		l, err := lang.TestLanding(s.source, sampleSources(p, s.files))
		admitted := err == nil && lang.CheckTestLanding(s.source, l) == nil
		lands := UnitTestFile(p, s.files)(s.unit, s.source) != ""
		if admitted != tc.sourceAdmitted || lands != tc.unitLand {
			t.Errorf("%s: source landing admitted %v (want %v), unit lands %v (want %v)", tc.lang, admitted, tc.sourceAdmitted, lands, tc.unitLand)
		}
	}
}
