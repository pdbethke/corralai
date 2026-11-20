// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"path"
	"testing"
)

// TestSwiftAuthoredTestLandsInTheDevTestsTarget: SwiftPM compiles a test only
// inside a test target's directory, and pairs Sources/<Target>/ with
// Tests/<Target>Tests/ by name alone. A source whose dev test is named for
// behaviour (Lariat's Evaluating.swift), or which only another target's test
// covers (the test evidence names), has its authored test written into THAT
// test's directory, never into a Tests/<Target>Tests/ derived from the name,
// which may not exist and which SwiftPM would then never compile: every
// survivor would read unproven while the run looked healthy.
func TestSwiftAuthoredTestLandsInTheDevTestsTarget(t *testing.T) {
	for _, tc := range []struct{ name, code, dev, want string }{
		{"a test named for the source", "Sources/Calc/Calc.swift", "Tests/CalcTests/CalcTests.swift", "Tests/CalcTests/Calc_corralTests.swift"},
		{"a test named for behaviour", "Sources/LassoParser/Evaluator.swift", "Tests/LassoParserTests/Evaluating.swift", "Tests/LassoParserTests/Evaluator_corralTests.swift"},
		{"another target's test, as evidence names it", "Sources/Lasso/Parser.swift", "Tests/IntegrationTests/RoundTrip.swift", "Tests/IntegrationTests/Parser_corralTests.swift"},
		{"a nested test directory", "Sources/Lasso/Parser.swift", "Tests/IntegrationTests/Sub/RoundTrip.swift", "Tests/IntegrationTests/Sub/Parser_corralTests.swift"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := authoredTestPath(AuthoredLanding{}, tc.code, tc.dev, nil)
			if got != tc.want {
				t.Fatalf("authoredTestPath(%q, %q) = %q, want %q", tc.code, tc.dev, got, tc.want)
			}
			if path.Dir(got) != path.Dir(tc.dev) {
				t.Fatalf("authored test %q is not in the dev test's directory %q", got, path.Dir(tc.dev))
			}
		})
	}
}
