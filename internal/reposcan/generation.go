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
const VerdictGeneration = "2"
