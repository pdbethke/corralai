// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"path/filepath"
	"strconv"
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

// MaxShardRetries is how many times a mutant-generator shard whose result will
// not parse is reopened before it is DROPPED and the run proceeds without it.
//
// Straight-lining the pre-shard "retry until the run dies" semantics would
// make sharding actively worse: with 8 seats the odds that at least one
// misbehaves rise ~8x, and one flaky shard would waste the other seven seats'
// spend. Dropping converges; the shortfall is recorded, never swallowed.
const MaxShardRetries = 2

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

// renderTestCritic asks a (different) model to read BOTH the code under review
// and the dev's own tests, and flag ONLY the demonstrably vacuous ones —
// freeform, so the worker runs its normal LLM+jail loop and files findings.
//
// The code is included deliberately: an earlier version handed the critic only
// the test file, so it speculated about the API and filed false positives
// (e.g. accusing a valid `tabulate(func, -1)` call of violating the recipe when
// a negative start is legitimate). Grounding it in the real source, plus a
// strict "only if certain" rubric, is what makes the critic safe to point at
// real, respected projects: a false accusation against a good test is worse
// than a miss, and "no vacuous tests" is a correct and common answer.
func renderTestCritic(rs RunSpec, _ []repoindex.Signature, _ []adequacy.Mutant) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a TEST CRITIC. Below are the code under review and the developer's own tests for it (goal: %s).\n\n", rs.Goal)
	fmt.Fprintf(&b, "CODE UNDER REVIEW (%s):\n%s\n\n", rs.CodePath, rs.Code)
	fmt.Fprintf(&b, "DEV TEST FILE (%s):\n%s\n\n", rs.DevTestPath, rs.DevTestCode)
	b.WriteString("Flag ONLY a test that is DEMONSTRABLY vacuous: it asserts nothing, its assertion is tautological (true regardless of the implementation), or it could not fail even if the code were broken in a way that violates the goal. Reason strictly from the CODE ABOVE — never guess a function's signature or behavior. If the code shows a call or argument is valid, it IS valid; do not flag a test for it.\n\n")
	b.WriteString("Do NOT flag a test merely because it is narrow, checks one case, exercises an implementation detail, uses a mock, or does not fully cover the documented behavior — those are normal, not vacuous. If you are not certain a test is vacuous, do NOT flag it. Many suites have zero vacuous tests, and reporting none is the correct answer.\n\n")
	b.WriteString("For each test you are certain is vacuous, file one finding: name the test and state exactly why it cannot fail. If none qualify, file nothing.\n")
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

// DefaultMaxShards is the stock generator width. It matches
// cmd/corral.localSwarmAutoCap so a default run's shard count and its
// concurrent-worker bound agree rather than one throttling the other.
const DefaultMaxShards = 8

// ShardTaskKey is the queue key for shard i of the mutant-generator role.
// Sharded keys are distinct from the bare role name so an unsharded run's
// task key is unchanged.
func ShardTaskKey(index int) string {
	return RoleMutantGenerator + "/" + strconv.Itoa(index)
}

// ShardIndexFromKey returns the shard index encoded in a mutant-generator task
// key, and whether the key was a sharded one. The bare role key (an unsharded
// run) and any malformed suffix report (0, false).
func ShardIndexFromKey(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, RoleMutantGenerator+"/")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}

// renderMutantGeneratorShard renders one shard's prompt: the SAME testgen
// prompt and the SAME whole-file context as the unsharded path, with the goal
// augmented by an aiming directive and the signature list filtered to this
// shard's symbols. The file is never fragmented — patch-based mutants anchor
// against the whole original.
func renderMutantGeneratorShard(rs RunSpec, sigs []repoindex.Signature, sh Shard) string {
	aimed := rs
	aimed.Goal = fmt.Sprintf(
		"%s\n\nATTACK ONLY THESE FUNCTIONS: %s. Every mutation you produce MUST edit code inside one of them. Other functions in the file are being attacked by other seats — do not mutate them, and do not report that you skipped them.",
		rs.Goal, strings.Join(sh.Symbols, ", "))
	return renderMutantGenerator(aimed, filterSignatures(sigs, sh.Symbols), nil)
}

// filterSignatures keeps only the signatures whose symbolIdentity is in
// want, preserving input order, so a shard's prompt lists exactly the
// surface it is aimed at. want holds qualified identities (symbolIdentity
// output, e.g. "*Engine.String"), matching Shard.Symbols exactly — matching
// on bare Signature.Name would conflate same-named methods on different
// receivers, letting both leak into a shard whose "ATTACK ONLY THESE
// FUNCTIONS" directive only meant one of them.
func filterSignatures(sigs []repoindex.Signature, want []string) []repoindex.Signature {
	keep := make(map[string]bool, len(want))
	for _, w := range want {
		keep[w] = true
	}
	var out []repoindex.Signature
	for _, s := range sigs {
		if keep[symbolIdentity(s)] {
			out = append(out, s)
		}
	}
	return out
}

// shardTitle labels a shard task with the region it attacks, so the queue and
// the cockpit show WHICH functions each seat is on.
func shardTitle(sh Shard) string {
	return "Generate mutants for " + strings.Join(sh.Symbols, ", ")
}

// RoleMutantGeneratorShadow is the CHALLENGER generator seat: a second model
// attacking the SAME region as its primary, for a region-controlled head-to-head.
//
// It is a DISTINCT role key on purpose. tasksByRole(RoleMutantGenerator)
// therefore CANNOT return a shadow task — the exclusion is structural, not a
// boolean someone must remember to check at each of four call sites. This is
// the gate; a flag would be the wrong mechanism.
//
// Assigning different models to different SHARDS instead would be no comparison
// at all: it is confounded by region exactly as raw per-shard yield is, and it
// would blend the exam's difficulty (the generator SETS the difficulty, so a
// weaker model on one shard plants easier mutants, the dev suite kills them,
// and the kill-rate rises) under a fixed certification threshold.
const RoleMutantGeneratorShadow = "mutant-generator-shadow"

// DefaultShadowModel is the challenger seat's stock model: cheap, and it
// shares the same provider credential (Anthropic) the mutant-generator's own
// cold-start default routinely runs under once an operator sets
// MODEL_BACKEND=anthropic — no NEW credential is required to turn shadow on,
// only a worker capable of serving it. Named here (not in cmd/corral) so both
// `certify --local` and the hosted brain resolve the SAME default rather than
// keeping two constants in lockstep by hand.
const DefaultShadowModel = "claude-haiku-4-5"

// ResolveShadowModel resolves an operator's shadow-model override into the
// RunSpec.ShadowModel value: "off"/"none" (case-insensitive) disables the
// challenger entirely, an empty string uses DefaultShadowModel, and anything
// else passes through verbatim as the challenger's model name. Shared by
// `certify --local`'s --shadow-model flag and the brain's per-run/env
// overrides so "off" means the same thing on both paths.
func ResolveShadowModel(flag string) string {
	f := strings.TrimSpace(flag)
	switch strings.ToLower(f) {
	case "off", "none":
		return ""
	case "":
		return DefaultShadowModel
	}
	return f
}

// ShadowShardTaskKey is the queue key for the challenger seat on shard i.
func ShadowShardTaskKey(index int) string {
	return RoleMutantGeneratorShadow + "/" + strconv.Itoa(index)
}

// ShadowShardIndexFromKey mirrors ShardIndexFromKey for challenger seats.
func ShadowShardIndexFromKey(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, RoleMutantGeneratorShadow+"/")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
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
	shards := ShardSymbols(sigs, rs.MaxShards)
	specs := make([]queue.TaskSpec, 0, len(roles)+len(shards))
	for _, role := range roles {
		// The mutant-generator fans out into one seat per shard when the file
		// has an extractable symbol surface; otherwise it stays exactly one
		// whole-file seat with an unchanged key and a byte-identical prompt.
		if role.Name == RoleMutantGenerator && len(shards) > 0 {
			for _, sh := range shards {
				specs = append(specs, queue.TaskSpec{
					Key:         ShardTaskKey(sh.Index),
					Role:        RoleMutantGenerator,
					Title:       shardTitle(sh),
					Instruction: renderMutantGeneratorShard(rs, sigs, sh),
					Model:       assign[RoleMutantGenerator],
				})
			}
			// The challenger fans out over the SAME shards, one seat per region,
			// under its OWN role key (RoleMutantGeneratorShadow) — never under
			// RoleMutantGenerator — so tasksByRole(RoleMutantGenerator) structurally
			// cannot return a shadow task. See RoleMutantGeneratorShadow's doc for
			// why this is a role key and not a boolean field.
			if strings.TrimSpace(rs.ShadowModel) != "" {
				for _, sh := range shards {
					specs = append(specs, queue.TaskSpec{
						Key:         ShadowShardTaskKey(sh.Index),
						Role:        RoleMutantGeneratorShadow,
						Title:       "Challenger: " + shardTitle(sh),
						Instruction: renderMutantGeneratorShard(rs, sigs, sh),
						Model:       rs.ShadowModel,
					})
				}
			}
			continue
		}
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
