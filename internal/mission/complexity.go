// SPDX-License-Identifier: Elastic-2.0

package mission

import "strings"

// Tier classifies how much ceremony a directive warrants. The full
// researcher…reviewer arc (DefaultPlan) costs roughly one agent-minute per
// phase — real wall-clock — so a two-function greenfield package should not
// pay for a security audit and a performance pass it doesn't need. Scaling
// the PLAN to the mission's actual complexity keeps the credibility bar (a
// trivial mission still gets built AND tested) while cutting the unwarranted
// roles.
type Tier int

const (
	// TierLean is a trivial, self-contained, greenfield ask: a handful of
	// functions or a small utility with no external surface. build -> test
	// is the whole arc — that's the credibility bar, nothing more.
	TierLean Tier = iota
	// TierStandard is a real feature: worth a design pass and an integration
	// step, but not worth a dedicated research phase, a pentester, or a
	// perf pass of its own.
	TierStandard
	// TierFull is the complete SDLC arc (DefaultPlan): substantial features,
	// anything security/infra/data-sensitive, or repo work that must be
	// verified against a live codebase.
	TierFull
)

func (t Tier) String() string {
	switch t {
	case TierLean:
		return "lean"
	case TierStandard:
		return "standard"
	default:
		return "full"
	}
}

// fullSignals are directive keywords that always warrant the complete arc —
// security, external systems, data, or explicit production/scale framing.
// Matching any one of these is sufficient on its own; false positives here
// only cost ceremony, while false negatives would silently skip a pentester
// or perf pass on work that needed one.
var fullSignals = []string{
	"security", "auth", "authentic", "payment", "billing", "production",
	"compliance", "database", "migrat", "microservice", "distributed",
	"concurren", "multi-tenant", "encrypt", "credential", "secret",
	"infrastructure", "deploy", "pipeline", "real-time", "at scale",
	"integration with", "third-party", "third party", "public api",
	"rate limit", "audit", "gdpr", "pii", "vulnerab",
}

// leanSignals are directive keywords suggesting a small, self-contained,
// greenfield ask — the kind of "two functions in a package" mission that
// does not need a research phase, secops, perf, or a separate integrator.
var leanSignals = []string{
	"function", "utility", "helper", "small", "simple", "tiny",
	"one file", "single file", "script", "snippet", "package with",
	"toy", "trivial", "quick",
}

// classifyComplexity is a heuristic on the directive's own text: a cheap,
// deterministic stand-in for "how many files/roles will this actually take."
// It errs toward TierFull on any ambiguity that touches security/infra/data
// (see fullSignals), and toward TierLean only for short, clearly-scoped,
// greenfield asks — anything in between defaults to TierStandard.
func classifyComplexity(directive string) Tier {
	d := strings.ToLower(directive)
	for _, kw := range fullSignals {
		if strings.Contains(d, kw) {
			return TierFull
		}
	}
	words := len(strings.Fields(directive))
	leanHit := false
	for _, kw := range leanSignals {
		if strings.Contains(d, kw) {
			leanHit = true
			break
		}
	}
	if words <= 8 || (leanHit && words <= 30) {
		return TierLean
	}
	return TierStandard
}

// leanPlan is the lean tier: build then test, nothing else. This is the
// credibility floor — a trivial mission still gets built AND independently
// tested — without a research phase, designer, secops, perf pass, separate
// integrator, docs, or retro that a two-function package doesn't warrant.
func leanPlan(directive string) []PhaseSpec {
	d := directive
	verifyBuild, verifyTest := verifyCommands(directive)
	return []PhaseSpec{
		{Name: "build", Role: "builder", Count: 1, Verify: verifyBuild,
			Instruction: "Build: " + d + ". This is a small, self-contained task — implement it directly, no need to over-engineer. Use write_file with full file contents. Record what you built in SHARED memory so the tester has context."},
		{Name: "test", Role: "tester", Count: 1, DependsOn: []string{"build"}, Verify: verifyTest,
			Instruction: "Independently verify the work built for: " + d + ". Read the build notes for intent — but you did NOT build it, so test adversarially: edge cases, error paths. Record any failure in SHARED memory."},
	}
}

// standardPlan is the middle tier: a real feature worth designing and
// integrating, but not worth a dedicated research phase, a pentester, or a
// perf pass of its own — those are reserved for TierFull.
func standardPlan(directive string) []PhaseSpec {
	d := directive
	verifyBuild, verifyTest := verifyCommands(directive)
	return []PhaseSpec{
		{Name: "design", Role: "designer", Count: 1,
			Instruction: "Design the solution for: " + d + ". Use search_reference to consult the reference corpus — especially any design 'looks' the user saved (honor their style). Decide the architecture, the data model, the component/UI breakdown, and the overall approach. Record the DESIGN in SHARED memory — concrete enough that builders implement against it without guessing. Do NOT write the implementation."},
		{Name: "build-core", Role: "builder", Count: 1, DependsOn: []string{"design"}, Verify: verifyBuild,
			Instruction: "Build the SMALLEST WORKING CORE of: " + d + ". FIRST read the designer's design from SHARED memory and implement against it (don't redesign). Core = the minimum slice that compiles and does the central thing; leave secondary features for the completion pass. Use write_file with full file contents. Record what the core covers — and what it deliberately leaves out — in SHARED memory."},
		{Name: "build", Role: "builder", Count: 1, DependsOn: []string{"build-core"}, Verify: verifyBuild,
			Instruction: "Complete the build of: " + d + ". Read the design AND the core-build notes from SHARED memory, then fill in the remaining features and edge cases on top of the existing core (extend, don't rewrite). Use write_file with full file contents. Record what you built and any deviations in SHARED memory so the verifiers have full context."},
		{Name: "test", Role: "tester", Count: 1, DependsOn: []string{"build"}, Verify: verifyTest,
			Instruction: "Independently verify the feature built for: " + d + ". Read the build notes for intent — but you did NOT build it, so test adversarially: edge cases, error paths, broken assumptions. Record every failure in SHARED memory."},
		{Name: "integrate", Role: "integrator", Count: 1, DependsOn: []string{"test"}, Verify: verifyBuild,
			Instruction: "Assemble the work for: " + d + " into one coherent, working whole. Read the build/test notes from SHARED memory, wire the pieces together, resolve cross-file integration, and confirm it runs end to end. Record integration status and any remaining gaps in SHARED memory."},
		{Name: "docs", Role: "writer", Count: 1, DependsOn: []string{"integrate"},
			Instruction: "Document the feature for: " + d + ". Read the integration and build notes from SHARED memory and produce a clear README / usage / API reference. Record the docs (or where they live) in SHARED memory."},
	}
}

// ScaledPlan classifies the directive's complexity and returns the plan sized
// to it: TierLean -> leanPlan, TierStandard -> standardPlan, TierFull ->
// DefaultPlan (the complete researcher…reviewer arc). This is the entry
// point new directives should plan through; DefaultPlan itself is left
// unchanged (and still directly callable) so existing callers that
// explicitly want the full arc — tests and the CreateMission nil-plan safety
// net — are unaffected.
func ScaledPlan(directive string) []PhaseSpec {
	switch classifyComplexity(directive) {
	case TierLean:
		return leanPlan(directive)
	case TierStandard:
		return standardPlan(directive)
	default:
		return DefaultPlan(directive)
	}
}
