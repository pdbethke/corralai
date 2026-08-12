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
// Getting it wrong in the safe direction — bumping when you did not have to —
// only costs money. Not bumping when you should serves a stale verdict, which
// signs a claim about behaviour that was never measured. When unsure, bump.
//
// It is also the purge lever: a generation of verdicts later found to be wrong
// is invalidated wholesale by bumping this, without deleting any ledger data.
const VerdictGeneration = "1"
