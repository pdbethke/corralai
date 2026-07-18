// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/testgen"
)

// DevAdequacyKey is the synthetic dependency key the driver satisfies once
// it has scored the developer's own tests against the mutant-generator's
// output (Task 4.2). test-writer depends on it because it needs the
// survivors the dev's tests missed before it has anything to target.
const DevAdequacyKey = "dev-adequacy"

// Role names, also used as queue.TaskSpec.Key/Role.
const (
	RoleMutantGenerator = "mutant-generator"
	RoleTestWriter      = "test-writer"
	RoleTestCritic      = "test-critic"
)

// Role is a role defined as data: a prompt-render, a result contract
// (Structured vs freeform-findings), and its DAG deps. New adversarial
// roles compose by adding an entry here — no new driver plumbing.
type Role struct {
	Name       string
	Structured bool
	Deps       []string
	// Render builds the task instruction from the run + signatures + (for
	// deps-satisfied roles) the survivors the dev's tests missed.
	Render func(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant) string
}

// joinPrompt folds a structured role's system/user prompt pair into a
// single task Instruction — the worker's structured fast path has no
// system/user split, just one instruction string.
func joinPrompt(system, user string) string {
	return system + "\n\n" + user
}

// langFor resolves the run's plugin, defaulting to go for back-compat when
// Lang is unset. Falls back to go if an unknown name slips through (the
// brain has already preflighted; this keeps rendering total).
func langFor(rs RunSpec) golang.Plugin {
	if p, ok := golang.ByName(rs.Lang); ok {
		return p
	}
	p, _ := golang.ByName("go")
	return p
}

// renderMutantGenerator uses testgen's proven GenerateMutants prompt,
// unchanged, so the worker's model sees the exact prompt the in-process
// generator would have used.
func renderMutantGenerator(rs RunSpec, sigs []repoindex.Signature, _ []adequacy.Mutant) string {
	p := langFor(rs)
	system, user := testgen.GenerateMutantsPrompt(p.MutantSystem(), rs.Goal, rs.Code, sigs, rs.NMutants)
	return joinPrompt(system, user)
}

// renderTestCritic asks a (different) model to read the dev's own tests and
// flag vacuous/tautological/designed-to-pass patterns — freeform, so the
// worker runs its normal LLM+jail loop and files findings.
func renderTestCritic(rs RunSpec, _ []repoindex.Signature, _ []adequacy.Mutant) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a TEST CRITIC. Read the developer's own tests below (for %s, goal: %s) and flag any that are vacuous, tautological, or designed to pass without actually exercising the goal (CI-theater). File a finding for each such test, naming it and explaining what it fails to check.\n\n", rs.DevTestPath, rs.Goal)
	fmt.Fprintf(&b, "DEV TEST FILE (%s):\n%s\n", rs.DevTestPath, rs.DevTestCode)
	return b.String()
}

// renderTestWriter uses testgen's proven WriteTest prompt, targeted at the
// survivors the dev's tests missed: the goal is augmented with the
// surviving mutants so the worker's model writes a test that kills what the
// dev's suite let through, not a generic test of the goal.
func renderTestWriter(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant) string {
	goal := rs.Goal
	if len(survivors) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\nThe developer's own tests did NOT catch the following goal-violating mutants (they passed undetected). Write a test that specifically kills these survivors — proving the missed bugs are real and catchable, not equivalent mutants.\n", rs.Goal)
		for _, m := range survivors {
			fmt.Fprintf(&b, "\n--- SURVIVOR %s ---\n%s\n", m.ID, m.Code)
		}
		goal = b.String()
	}
	// Tell the writer the actual file name. WriteTestPrompt hands the model the
	// code CONTENT but not its path, so without this it cannot form a correct
	// relative import and falls back to the prompt's EXAMPLE filename — which a
	// real module-resolving compile check (tsc: TS2307) rejects, and which a
	// syntax-only check (py_compile / ruby -c / node --check) silently lets
	// through as a latent runtime break. The authored test lands in the SAME
	// directory as the code, so a same-directory reference by this base name is
	// correct. Stated as a fact so it stays right across languages (Go stays
	// white-box same-package; python/js/ts import by name/extension).
	named := fmt.Sprintf("The source file under review is named %q, and your test file will be placed in the SAME directory as it. Reference or import the code under test as appropriate for the language, using that exact file name — do not invent or assume any other name.\n\n%s", filepath.Base(rs.CodePath), goal)
	p := langFor(rs)
	system, user := testgen.WriteTestPrompt(p.TestWriterSystem(), named, rs.Code, sigs)
	return joinPrompt(system, user)
}

// Roles returns the pool's three worker roles: mutant-generator and
// test-critic run in parallel with no deps; test-writer depends on
// dev-adequacy (the survivors it needs to target).
func Roles() []Role {
	return []Role{
		{Name: RoleMutantGenerator, Structured: true, Render: renderMutantGenerator},
		{Name: RoleTestWriter, Structured: true, Deps: []string{DevAdequacyKey}, Render: renderTestWriter},
		{Name: RoleTestCritic, Structured: false, Render: renderTestCritic},
	}
}

// BuildDAG renders each role's task instruction and stamps the assigned
// model, producing the initial task set for a run: mutant-generator and
// test-critic have no deps and are immediately claimable; test-writer
// DependsOn dev-adequacy and is promoted once that's satisfied (Task 4.2).
// test-writer's instruction here is rendered with no survivors yet — the
// driver re-renders it once the dev's tests have been scored.
//
// CRITICAL: Verify is never set on structured tasks (mutant-generator,
// test-writer) — the worker's structured fast path has no tool loop to run
// a Verify command, and a Verify suffix would pollute the rendered testgen
// prompt.
func BuildDAG(rs RunSpec, assign RoleAssignment, sigs []repoindex.Signature) []queue.TaskSpec {
	roles := Roles()
	specs := make([]queue.TaskSpec, 0, len(roles))
	for _, role := range roles {
		specs = append(specs, queue.TaskSpec{
			Key:         role.Name,
			Role:        role.Name,
			Title:       roleTitle(role.Name),
			Instruction: role.Render(rs, sigs, nil),
			DependsOn:   role.Deps,
			Model:       assign[role.Name],
		})
	}
	return specs
}

// roleTitle gives each role's task a short UI label.
func roleTitle(role string) string {
	switch role {
	case RoleMutantGenerator:
		return "Generate seeded-violation mutants"
	case RoleTestWriter:
		return "Write test targeting survivors"
	case RoleTestCritic:
		return "Critique the dev's tests"
	default:
		return role
	}
}
