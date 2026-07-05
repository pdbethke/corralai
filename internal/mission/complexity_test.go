// SPDX-License-Identifier: Elastic-2.0

package mission

import "testing"

// TestScaledPlanLeanForTrivialDirective is the motivating case: a two-function
// greenfield package must not get a research phase, a designer, a pentester,
// a perf pass, a separate integrator, docs, or a retro. It still gets built
// AND independently tested — that's the credibility floor, not the ceremony.
func TestScaledPlanLeanForTrivialDirective(t *testing.T) {
	plan := ScaledPlan("Write a Go package with Max(a,b) and Min(a,b)")
	if got := classifyComplexity("Write a Go package with Max(a,b) and Min(a,b)"); got != TierLean {
		t.Fatalf("classifyComplexity = %v, want TierLean", got)
	}
	names := map[string]bool{}
	for _, p := range plan {
		names[p.Name] = true
	}
	for _, want := range []string{"build", "test"} {
		if !names[want] {
			t.Errorf("lean plan missing phase %q: %+v", want, plan)
		}
	}
	for _, unwanted := range []string{"research", "design", "secops", "perf", "integrate", "docs", "retro"} {
		if names[unwanted] {
			t.Errorf("lean plan should not include phase %q: %+v", unwanted, plan)
		}
	}
	if len(plan) != 2 {
		t.Errorf("lean plan should have exactly 2 phases (build, test), got %d: %+v", len(plan), plan)
	}
	// Roles staffed must be minimal too — fewer roles is strictly good (less
	// staffing ceremony for the multi-role worker pool).
	roles := map[string]bool{}
	for _, p := range plan {
		roles[p.Role] = true
	}
	if len(roles) != 2 || !roles["builder"] || !roles["tester"] {
		t.Errorf("lean plan should staff exactly builder+tester, got %v", roles)
	}
}

// TestScaledPlanFullForSubstantialDirective ensures a directive that touches
// security/data/infra still gets the complete researcher…reviewer arc — the
// scaling-down must never scale down work that's actually warranted.
func TestScaledPlanFullForSubstantialDirective(t *testing.T) {
	directive := "Add OAuth2 authentication with third-party provider support, audit logging, and rate limiting to the public API, backed by the production database."
	if got := classifyComplexity(directive); got != TierFull {
		t.Fatalf("classifyComplexity = %v, want TierFull", got)
	}
	plan := ScaledPlan(directive)
	want := DefaultPlan(directive)
	if len(plan) != len(want) {
		t.Fatalf("full-tier ScaledPlan should equal DefaultPlan: got %d phases, want %d", len(plan), len(want))
	}
	for i := range plan {
		if plan[i].Name != want[i].Name || plan[i].Role != want[i].Role {
			t.Errorf("phase %d = %+v, want %+v", i, plan[i], want[i])
		}
	}
}

// TestScaledPlanStandardForOrdinaryFeature covers the middle tier: a real
// feature that's worth designing and integrating but doesn't need a
// dedicated research phase, a pentester, or a perf pass of its own.
func TestScaledPlanStandardForOrdinaryFeature(t *testing.T) {
	directive := "Build a wishlist page where signed-in shoppers can save products from the catalog, reorder them, and remove items they no longer want."
	if got := classifyComplexity(directive); got != TierStandard {
		t.Fatalf("classifyComplexity = %v, want TierStandard", got)
	}
	plan := ScaledPlan(directive)
	names := map[string]bool{}
	for _, p := range plan {
		names[p.Name] = true
	}
	for _, want := range []string{"design", "build-core", "build", "test", "integrate", "docs"} {
		if !names[want] {
			t.Errorf("standard plan missing phase %q: %+v", want, plan)
		}
	}
	for _, unwanted := range []string{"research", "secops", "perf", "retro"} {
		if names[unwanted] {
			t.Errorf("standard plan should not include phase %q: %+v", unwanted, plan)
		}
	}
}

// TestClassifyComplexityFullSignalsWin makes sure a full-tier signal
// (security/infra/data) outranks an otherwise-lean-looking short directive —
// false negatives here would silently skip a pentester or perf pass.
func TestClassifyComplexityFullSignalsWin(t *testing.T) {
	if got := classifyComplexity("add a small auth helper"); got != TierFull {
		t.Fatalf("classifyComplexity = %v, want TierFull (auth signal must win over 'small')", got)
	}
}

// TestScaledPlanAlwaysGatesBuildAndTest checks every tier still verifies build
// and test — the scaling must never drop the actual build+test gate, only the
// surrounding ceremony.
func TestScaledPlanAlwaysGatesBuildAndTest(t *testing.T) {
	directives := []string{
		"Write a Go package with Max(a,b) and Min(a,b)",
		"Build a wishlist page where signed-in shoppers can save products from the catalog, reorder them, and remove items they no longer want.",
		"Add OAuth2 authentication with third-party provider support and a production database migration.",
	}
	for _, d := range directives {
		plan := ScaledPlan(d)
		var sawBuild, sawTest bool
		for _, p := range plan {
			if p.Name == "build" || p.Name == "build-core" {
				if p.Verify == "" {
					t.Errorf("%q: build phase %q must be verify-gated", d, p.Name)
				}
				sawBuild = true
			}
			if p.Name == "test" {
				if p.Verify == "" {
					t.Errorf("%q: test phase must be verify-gated", d)
				}
				sawTest = true
			}
		}
		if !sawBuild || !sawTest {
			t.Errorf("%q: plan missing build or test phase: %+v", d, plan)
		}
	}
}
