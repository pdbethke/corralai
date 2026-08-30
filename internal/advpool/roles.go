// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"log"
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

// MaxTestWriterAttempts is how many times the test-writer's output is
// reopened after a compile failure before the run gives up on authoring a
// killing test and converges anyway.
//
// Unlike a mutant-generator shard (dropping one of several regions still
// leaves a usable exam), the test-writer is a single seat with no fallback:
// looping it unconditionally on a hard survivor spins the run to
// RunDeadline with NO signed verdict — the worst possible first impression
// for a run that already has a real, useful result (the dev kill-rate, the
// survivor, the critic findings) sitting computed and unused. Capping the
// attempts and then converging with TestWriterFailed=true trades "an
// auto-authored killing test" for "an honest signed verdict every time."
const MaxTestWriterAttempts = 3

// MaxShadowWriterAttempts bounds the CHALLENGER writer's compile retries. It is
// lower than the primary's on purpose: the challenger only records a
// comparison, so spending the primary's retry budget on it would trade a
// graded outcome for a measurement.
const MaxShadowWriterAttempts = 2

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
	b.WriteString("For each test you are certain is vacuous, file one finding: name the test and state exactly why it cannot fail. Also set scope to \"whole-test\" if the ENTIRE test can never fail, or \"dead-check\" if only a specific check inside it is dead while the test still asserts something real; set test_file to the repo-relative path of the file holding the flagged test, and test_selector to the exact runnable selector for that single test (e.g. path::Class::test_name for pytest, TestName for go). If none qualify, file nothing.\n")
	return b.String()
}

// renderTestWriter uses testgen's proven WriteTest prompt, targeted at the
// survivors the dev's tests missed: the goal is augmented with the
// surviving mutants so the worker's model writes a test that kills what the
// dev's suite let through, not a generic test of the goal.
// renderTestWriter is the RoleTestWriter Render func: the initial (survivors
// nil) and survivor-promote renders. Compile-failure retries go through
// renderTestWriterWithRepair to feed the compiler error back.
func renderTestWriter(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant) string {
	return renderTestWriterWithRepair(rs, sigs, survivors, "", "")
}

// renderTestWriterWithRepair renders the test-writer instruction, appending a
// REPAIR block when a prior attempt's test (prevTest) and its compiler error
// (compileErr) are supplied — so a compile-failure retry CORRECTS the actual
// error instead of blindly repeating it. Both empty == a normal render. The
// driver's retry path (tickPoolAdequacy) is the only caller that passes them;
// see CompileError for why a bare "does not compile" left the writer unable to
// improve.
func renderTestWriterWithRepair(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant, prevTest, compileErr string) string {
	return renderTestWriterRepairing(rs, sigs, survivors, prevTest, compileErr, "")
}

// renderTestWriterRepairing is renderTestWriterWithRepair with the second
// repair mode: cleanFailure carries the output of a test that COMPILED and
// then failed against the unmutated code. The two are mutually exclusive by
// construction (a test that does not build never runs), and only one repair
// block is ever appended.
func renderTestWriterRepairing(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant, prevTest, compileErr, cleanFailure string) string {
	goal := rs.Goal
	if len(survivors) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\nThe developer's own tests did NOT catch the following goal-violating mutants (they passed undetected). Write a test that specifically kills these survivors — proving the missed bugs are real and catchable, not equivalent mutants.\n\n%s\n", rs.Goal, testgen.WriterStrictnessNote())
		for _, m := range survivors {
			// A mutant is its hunk, not the whole file it would make: the
			// code under review is already above (once, via WriteTestPrompt's
			// TARGET FILE), so each survivor here is a small diff against it
			// rather than a second copy of the file — see RenderHunk's doc
			// comment for the 0.5M-token-per-file cost this removes.
			// RenderHunk always renders (never errors, never dumps a whole
			// file), so every survivor is named here — none can silently
			// vanish from what the writer is told to kill.
			fmt.Fprintf(&b, "\n%s\n", RenderHunk(m, rs.Code, 3))
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
	// white-box same-package; js/ts import by relative path; ruby requires by
	// relative base name). The unique-names clause heads off the single most
	// common compile failure: a white-box Go test redeclaring a helper/test
	// name the dev's own suite (also seeded into the jail, same package)
	// already defines.
	//
	// The "using that exact file name" clause is SKIPPED when importNote is
	// non-empty (python with a real ImportPath fact, known OR explicitly
	// unknown): stating both "import it by this exact file name" AND "do NOT
	// import it by its bare file base name" in the same instruction is a
	// self-contradiction in a prompt whose entire failure mode is a model
	// obeying a stale/wrong clause — see ImportNote's doc comment for why a
	// wrong instruction is worse than an absent one. The file-name fact is
	// still true and useful for python (it is what the test-writer names ITS
	// OWN file after), so only the "reference/import using that name" half is
	// dropped, not the whole sentence.
	p := langFor(rs)
	importNote := p.ImportNote(rs.ImportPath, rs.ImportPath != "")

	// Where the authored test ACTUALLY lands, computed from the same function
	// the scorer and validator overlay it with — never assumed. This sentence
	// used to assert same-directory placement unconditionally, which stopped
	// being true when the authored test moved into the DEV TEST's directory so
	// the project's own runner would collect it (e83ea8d). Go was unaffected
	// (its dev tests are siblings, so only the filename changed) and Python
	// survived on ImportNote's dotted import, but Ruby and JS/TS — whose
	// plugins return an EMPTY ImportNote, so the model is told to reference the
	// code "using that exact file name" — were handed an instruction that no
	// longer resolved from the new location.
	//
	// `base` is nil here: a collision-disambiguated name would differ in the
	// FILENAME only, never the directory, so the relative hop below stays
	// correct either way.
	authored := authoredTestPath(rs.CodePath, rs.DevTestPath, nil)
	authoredDir, codeDir := filepath.Dir(authored), filepath.Dir(rs.CodePath)
	rel, rerr := filepath.Rel(authoredDir, rs.CodePath)
	if rerr != nil {
		rel = filepath.Base(rs.CodePath)
	}
	rel = filepath.ToSlash(rel)

	var fileFact string
	if authoredDir == codeDir {
		fileFact = fmt.Sprintf("Your test file will be created at %q — the same directory as the source file under review, %q.",
			filepath.ToSlash(authored), filepath.Base(rs.CodePath))
	} else {
		fileFact = fmt.Sprintf("Your test file will be created at %q. The source file under review is %q, which from your test file's own location is %q — it is NOT in the same directory as your test.",
			filepath.ToSlash(authored), filepath.ToSlash(rs.CodePath), rel)
	}
	if importNote == "" {
		// Only when the language has no absolute-import fact to give (every
		// plugin but python). Stated as superseding, because a plugin's own
		// TestWriterSystem may carry a same-directory EXAMPLE — ruby's says
		// "require_relative the target module by its file's base name" — and a
		// prompt that asserts both a path and its contradiction fails exactly
		// the way ImportNote's doc warns about: the model obeys the stale half.
		fileFact += fmt.Sprintf(" Reference or import the code under test by that exact path (%q) — not by its bare base name, and not by any other name. This overrides any same-directory example shown above.", rel)
	}
	named := fmt.Sprintf("%s Your test may share the package/namespace with the developer's OWN tests, so give your test function(s) and any helpers UNIQUE names — never redeclare an identifier the existing suite may already define.\n\n%s%s%s",
		fileFact, harnessExemplar(rs), importNote, goal)
	if strings.TrimSpace(cleanFailure) != "" {
		// A DIFFERENT failure from a compile error, and it must not be worded
		// as one: this test BUILT and RAN, and then failed against the
		// unmutated, correct code — so it asserts something untrue about
		// working software, and every mutant it might have caught is
		// discarded (an invalid test may never earn a kill rate).
		//
		// The measured shape this exists for: a writer produced 13 tests
		// against flask's internals, TEN of which passed; three carried wrong
		// API assumptions and, because the compliant check is all-or-nothing
		// per FILE, took the other ten down with them. Naming the failing
		// tests specifically is what lets a model drop or fix three
		// assumptions instead of rewriting thirteen tests it mostly got right.
		named = fmt.Sprintf("%s\n\n--- YOUR PREVIOUS ATTEMPT FAILED ON THE UNMUTATED, CORRECT CODE ---\nYou wrote:\n%s\n\nRun against the ORIGINAL (unmodified) source, your test reported:\n%s\n\nThat means your test asserts something that is NOT true of the correct code — a wrong assumption about the API, not a bug you have found. The whole file is discarded when ANY test in it fails this way, so tests of yours that were fine are being thrown away too.\n\nReturn a corrected FULL test file that PASSES against the unmodified source. Fix or DELETE only the assertions that failed; keep the ones that already passed. Do not weaken a test into something that would pass against a broken implementation too — a test that cannot fail proves nothing.", named, prevTest, strings.TrimSpace(cleanFailure))
	}
	if strings.TrimSpace(compileErr) != "" {
		named = fmt.Sprintf("%s\n\n--- YOUR PREVIOUS ATTEMPT DID NOT COMPILE ---\nYou wrote:\n%s\n\nThe compiler reported:\n%s\n\nReturn a corrected FULL test file that compiles cleanly. Fix exactly what the compiler flagged; if it is a redeclared/duplicate identifier, rename yours to something unique.", named, prevTest, strings.TrimSpace(compileErr))
	}
	system, user := testgen.WriteTestPrompt(harnessOverride(p.TestWriterSystem(), rs), named, rs.Code, sigs)
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

// RoleTestWriterShadow is the CHALLENGER writer seat: a second model authoring
// its own suite against the SAME mutant set as the primary writer, for a
// mutant-controlled head-to-head.
//
// It is a DISTINCT role key on purpose, exactly as RoleMutantGeneratorShadow
// is: tasksByRole(RoleTestWriter) therefore CANNOT return a shadow task, and
// the exclusion is structural rather than a boolean someone must remember to
// check. This is the gate.
//
// Scoring both writers against the IDENTICAL mutant set is the controlled-
// comparison invariant. Two writers facing different mutants is confounded by
// mutant difficulty, exactly as assigning models to different SHARDS would be
// confounded by region difficulty.
//
// The seat NEVER gates: its outcome cannot reach the verdict, the aggregate,
// or the certification record.
const RoleTestWriterShadow = "test-writer-shadow"

// ResolveShadowModel resolves an operator's shadow-model override into the
// RunSpec.ShadowModel value: "off"/"none" (case-insensitive) disables the
// challenger, and anything else passes through verbatim as the challenger's
// model name. Shared by `certify --local`'s --shadow-model flag and the brain's
// per-run/env overrides so the spelling means the same thing on both paths.
//
// THE CHALLENGER IS OFF UNLESS NAMED. It used to default to a Claude model,
// which made it the quietest way corral forced a vendor on someone: an operator
// who moved the writer, mutant-generator and critic to another provider still
// had an Anthropic seat running, still needed that key, and got an error naming
// a vendor they had deliberately left behind. The challenger is a measurement
// seat that never gates a verdict, so the cost of it being off by default is
// nothing but a comparison nobody asked for.
func ResolveShadowModel(flag string) string {
	return ResolveOptionalModel(flag, "")
}

// ResolveOptionalModel is the shared resolution for every role a run may turn
// OFF: "off"/"none" (case-insensitive) disables the role, an empty flag takes
// def, anything else passes through verbatim.
//
// Factored out when the test-critic became disablable so the two roles cannot
// drift on what "off" means — an operator who learns `--shadow-model off`
// should not discover that `--critic-model off` was interpreted as a MODEL
// NAMED "off" and sent to a provider.
func ResolveOptionalModel(flag, def string) string {
	f := strings.TrimSpace(flag)
	switch strings.ToLower(f) {
	case "off", "none":
		return ""
	case "":
		return def
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
		// An UNASSIGNED test-critic seeds no task at all. The critic is the one
		// role that is purely advisory — its findings ride the verdict as
		// unverified review and never gate certification — so an operator with
		// no second model available may legitimately run without it.
		//
		// Seeding it anyway with an empty Model would be worse than useless: the
		// worker would fall back to the base backend's default model, which on a
		// deliberately single-vendor run is a model that vendor does not serve
		// (a Claude critic aimed at the Gemini endpoint 404s), and the run would
		// then wait forever on a task nothing can complete.
		//
		// Only the critic is skippable. The mutant-generator and test-writer
		// PRODUCE the execution-proven measurement; a run without them has
		// nothing to certify.
		if role.Name == RoleTestCritic && strings.TrimSpace(assign[RoleTestCritic]) == "" {
			continue
		}
		// A run handed a fixed mutant set generates NOTHING. Skipping the
		// generator ROLE (rather than emptying `shards`) covers both fan-out
		// shapes at once: the sharded seats, the unsharded whole-file seat
		// this loop would otherwise fall through to, AND the challenger seats,
		// which are only ever emitted from inside the generator branch. There
		// is no fourth place a generator task can come from, so there is no
		// path by which a preset run pays for generation.
		if role.Name == RoleMutantGenerator && rs.PresetMutants != nil {
			// Said out loud when a challenger model WAS configured. The skip
			// is correct — there is nothing to challenge when the exam is
			// fixed — but the operator asked for a second opinion and is not
			// getting one, and an UNMEASURED challenger must never be left
			// looking like a challenger that found nothing.
			if m := strings.TrimSpace(assign[RoleMutantGeneratorShadow]); m != "" {
				log.Printf("advpool: --mutants replays a recorded set, so nothing is generated: the challenger generator seat (%s) is SKIPPED — its yield this run is unmeasured, not zero", m)
			}
			continue
		}
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

// harnessExemplarLimit bounds how much of the dev's test file is quoted into
// the writer's instruction. The point is to show the harness and the import
// style, which live in the first lines; a 3000-line suite would otherwise
// crowd out the survivors the writer is actually being asked to kill.
const harnessExemplarLimit = 6000

// harnessExemplar shows the test-writer the developer's OWN test file as the
// authority on how this project's tests are written.
//
// Each language plugin encodes exactly one harness — tsPlugin.TestCmd is
// `node --experimental-strip-types --test`, so the writer authored
// `import { test } from 'node:test'` against a project that runs vitest. The
// test compiled, was overlaid into the project, and then did not run at all:
// the survivors were real and the authored test may well have been correct,
// but nothing could be proven and the verdict reported proven_missed 0. The
// same mismatch is waiting in Python (pytest vs unittest) and Ruby (rspec vs
// minitest): a plugin picks one harness, a real project picks its own.
//
// Detecting the framework from the test command or package.json was the
// alternative. This is better: the dev's test file is ground truth that corral
// already holds — it shows the harness, the import style, the assertion
// idiom and the project's own conventions at once, with nothing to keep in
// sync as ecosystems change. It is quoted as an example to MATCH, never as
// something to edit; the writer's output is a separate new file.
//
// Returns "" when there is no dev test to show, so the instruction is simply
// unchanged rather than carrying an empty, confusing block.
func harnessExemplar(rs RunSpec) string {
	code := strings.TrimSpace(rs.DevTestCode)
	if code == "" {
		return ""
	}
	truncated := ""
	if len(code) > harnessExemplarLimit {
		code = code[:harnessExemplarLimit]
		truncated = "\n… (truncated — the harness and import style above are what matter)"
	}
	path := rs.DevTestPath
	if path == "" {
		path = "the project's existing test file"
	}
	return fmt.Sprintf("THE PROJECT'S OWN TEST FILE (%s) IS SHOWN BELOW AS AN EXAMPLE TO MATCH. Use the SAME test framework, the same import style and the same assertion idiom it uses — that harness is what this project actually runs, and a test written for a different framework will not execute here even if it compiles. Do NOT edit or reproduce this file; write your own new test file in its style.\n\n--- EXISTING TESTS (%s) ---\n%s%s\n--- END EXISTING TESTS ---\n\n",
		path, path, code, truncated)
}

// harnessOverride appends an explicit precedence rule to the test-writer's
// SYSTEM prompt when the project has tests of its own to copy the style of.
//
// Every plugin's TestWriterSystem pins one harness, because in single-file mode
// corral owns the harness and pinning it is correct. tsPlugin's says "using the
// builtin node:test runner", names the exact imports, and adds "Builtin modules
// only. No external packages" — which does not merely fail to suggest vitest,
// it FORBIDS it. Showing the project's own vitest tests in the task was not
// enough: the model obeyed the system prompt and wrote node:test anyway, the
// authored test never ran in the project's runner, and proven_missed stayed 0
// with real survivors on the table.
//
// So the override is stated as a precedence rule rather than left implicit. A
// prompt that contains two contradictory instructions and no way to rank them
// fails exactly the way ImportNote's doc comment warns about — the model obeys
// the stale half. Here the ranking is explicit and last.
//
// Returns system unchanged when there is no project harness to defer to, which
// keeps single-file mode exactly as it was.
func harnessOverride(system string, rs RunSpec) string {
	if strings.TrimSpace(rs.DevTestCode) == "" {
		return system
	}
	return system + "\n\nOVERRIDE — THE PROJECT'S OWN TEST HARNESS WINS. The task below shows this project's existing test file. Use ITS test framework, ITS import style and ITS assertion idiom, even where that contradicts the instructions above: ignore any rule above that names a specific runner, mandates particular imports, or restricts you to builtin modules only. Importing the framework that existing file imports is REQUIRED and permitted. A test written for a different runner will not execute in this project, so it proves nothing no matter how correct it is."
}
