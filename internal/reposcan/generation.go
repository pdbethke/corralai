// SPDX-License-Identifier: Elastic-2.0

package reposcan

// VerdictGeneration identifies the engine behaviour a cached verdict was
// earned under. It is the EngineVersion component of KeyInputs.
//
// It is bumped BY HAND, and deliberately. It used to be corral's release
// version, which meant every release invalidated every cached verdict for
// every tenant: v0.3.6 was a documentation-only release and would have thrown
// away the entire corpus for no behavioural reason. Our release cadence must
// not set a customer's recompute bill.
//
// BUMP THIS when a change can move a verdict for unchanged source: anything in
// internal/adequacy, internal/advpool, or a plugin in internal/lang. It is
// part of the review checklist for those packages.
//
// BUMP IT ALSO for any change to advpool.Verdict's own FIELDS — the serialized
// shape, not just the behaviour that fills it in. A cached verdict is stored
// as verdict_json and read back with encoding/json, which unmarshals an older
// document CLEANLY and leaves every new field at its zero value: no error, no
// warning. Add DevKilledMutants-shaped detail and a reused pre-change verdict
// yields a scan_files row claiming mutants_total=42 next to ZERO scan_mutants
// rows — a ledger that contradicts itself, in a record that is
// tamper-evident and therefore uncorrectable afterwards.
//
// Getting it wrong in the safe direction — bumping when you did not have to —
// only costs money. Not bumping when you should serves a stale verdict, which
// signs a claim about behaviour that was never measured. When unsure, bump.
//
// It is also the purge lever: a generation of verdicts later found to be wrong
// is invalidated wholesale by bumping this, without deleting any ledger data.
//
// "2" (2026-08-28): coverage-guided test selection changed what the scorer runs.
// "3" (2026-08-29): per-mutant selection changed what the dev pass runs per mutant.
// "4" (2026-08-30): the authored pass proves each survivor with the authored
// test alone — a proven count can no longer come from a flaking dev test.
// "5" (2026-09-01): TEN commits of drift, caught by review rather than by a
// gate. Since "4" the scorer, the pool and the plugins changed under it —
// per-mutant fail-fast changed the graded command (#212), hunk-native mutants
// changed the generator prompt AND the writer call shape (#174), coverage
// became candidacy (#192), a sixth language landed (#188) — and Verdict itself
// gained THIRTEEN fields, every one of which an older cached document
// unmarshals to its zero value in silence. The behaviour half of this contract
// is still enforced by review; the SHAPE half is now gated, below.
// "6" (2026-09-01): the Verdict gained PoolScored, the discriminator that says
// whether ProvenMissed was MEASURED. A cached "5" verdict unmarshals it as
// false, which reads as "the pool never scored" — turning a proven gap back
// into an unproven one on every cache hit. Exactly the silent zero-fill the
// comment above describes, caught by the gate below on its first outing.
// "7" (2026-09-02): a banked TIMEOUT verdict now carries ChallengerAgreement,
// which was assigned on the converged path only. The shape is unchanged, so the
// fingerprint below does not move and could not have forced this — which is the
// behaviour half the comment above says no fingerprint can catch. A cached "6"
// timeout verdict has nil where a fresh one has a real coefficient, and the
// rule for that case is stated three paragraphs up: when unsure, bump.
// "8" (2026-09-03): the Verdict gained WriterProviderFailed (the writer's
// provider never answered — TestWriterFailed without the model's fault), and
// the cache now REFUSES any verdict whose writer half never graded, so a
// cached "7" document with TestWriterFailed set would not be served either
// way; the flag is what lets the scorecard tell the two apart.
// "9" (2026-09-04): the Verdict gained MutantBudget, and the EXAM CHANGED —
// a run with no explicit --n-mutants now plants a complexity-derived budget
// (floor 5, ceiling 40) instead of a flat 5 per seat, and the authored pass
// runs the authored test alone for its baseline and canary too. A cached "8"
// verdict measured a different exam and must not be served as this one; its
// zero-valued MutantBudget would also read as "generated nothing".
const VerdictGeneration = "9"

// VerdictShapeSHA256 fingerprints advpool.Verdict's serialized shape: every
// exported field's name, type and json tag, sorted by name and hashed.
//
// IT CHANGES TOGETHER WITH VerdictGeneration ABOVE, and that is the entire
// point. TestVerdictShapeIsPinnedToItsGeneration fails the moment a field is
// added, removed, retyped or re-tagged, and the only way to make it pass is to
// edit this constant — which puts a reviewer's eye directly on the line above
// it. Generation "5" exists because thirteen fields arrived without one.
//
// Sorted by name, so REORDERING fields is deliberately not a change: field
// order does not affect what encoding/json produces or accepts.
//
// WHAT IT DOES NOT COVER, stated plainly because a guard trusted past its
// scope is worse than no guard: it is SHALLOW. A change inside a nested type
// that Verdict merely holds — Timing's own fields, modelcorr.Pair's,
// golang.AuthoredPart's — alters the serialized document without altering this
// fingerprint. It matches the contract the comment above states ("advpool.
// Verdict's own FIELDS") and nothing wider. Nor can any fingerprint catch the
// behaviour half: a scorer that computes a DIFFERENT number into the SAME
// field is invisible here, and remains a review responsibility.
const VerdictShapeSHA256 = "52af485389f731949a343febfc324a004aa1b579eb562159f02d4e61986b6f98"
