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
// zero-valued MutantBudget would also read as "generated nothing". The same
// generation also gained ExamCoverage (symbols and decision points the
// mutants reached), MutantRef.Span, and ExamIndicative/IndicativeReason
// (a rate that cleared the threshold on an exam too small to certify is
// needs-review), and PriorsApplied/PriorDigest/PriorSource (a run handed a
// prior sat a different exam) — the SAME change, one bump.
// "10" (2026-10-04): the Verdict gained ShadowSelection — a challenger seat
// DRAWN from an operator-named pool, with each member's posterior and the
// seed that replays the draw (docs/design/shadow-seat-selection.md). The
// bump is for the Verdict's SHAPE: shape and generation change together
// (VerdictShapeSHA256 below), so a cached "9" document is re-measured once
// rather than served under a generation whose fields it never had.
// The scorecard's challenger-writer row and lang column landed in the same
// change; the bump itself is for the Verdict shape.
// (Corrected 2026-10-04, final branch review: this entry first said a "9"
// document "would present a drawn challenger's run as a hand-named one".
// No "9" verdict was ever drawn — pools did not exist — and a served verdict
// already shows how it was measured, so that reason was false.)
// "11" (2026-10-05): the Verdict gained CriticIncomplete. The critic's loop
// read one tool call per reply and stopped after six replies, so a suite with
// more vacuous tests than that reported at most six findings, recorded under a
// canned summary that read as a complete review; and a timed-out verdict never
// read the critic's findings at all. Both now say the review is incomplete. A
// cached "10" document unmarshals the field as false, which would present a
// possibly-truncated count as complete, and shape and generation change
// together in any case.
// "12" (2026-10-05): the critic seat changed from a tool loop to a typed
// single-call review (agentworker.runCritic), retried once on an answer that
// does not parse and falling back to the loop after that. The Verdict's shape
// is unchanged, so the fingerprint below cannot force this; it is the
// behaviour half. A cached "11" verdict carries a critic count the loop
// produced, and on the bench the two disagree (the loop missed tests the
// typed critic caught and flagged sound tests it did not), so it is
// re-measured once rather than served as this generation's review.
// "13" (2026-10-06): the typed critic now asks its provider to hold the
// answer to its schema (agentworker.ResponseFormat), so a model that used to
// answer off-shape, and fall back to a retry or the loop, now answers in
// shape. The Verdict's shape is unchanged; the critic count a "12" verdict
// carries may have come from the fallback path, so it is re-measured once.
// "14" (2026-10-06): for Go and Python the critic is now handed the test
// file's tests by name (lang.Plugin.TestNamesInSource) and must judge every
// one: the schema requires each name, and an answer that skips or invents
// one is retried, then handed to the loop. A local model that listed only
// the tests it flagged under "13" now cannot. Shape unchanged; behaviour
// bump.
// "15" (2026-10-06): a test file with more than 20 tests is now judged in
// batches of at most 20 names per call. Under "14" one keyed schema carried
// every name, which both providers refuse past their grammar limits, so on a
// large file the critic fell to the loop; its count came from a different
// path than a "15" critic's. Shape unchanged; behaviour bump.
// "16" (2026-10-07): the test list the critic must judge changed for Python
// (a scanner that tracks strings and brackets now finds tests it missed and
// stops inventing ones inside strings) and for Go (a renamed "testing"
// import), a test defined twice is listed once, and a transient provider
// error no longer drops the answer schema for the rest of a review. A "15"
// critic count may have come from a list that forbade judging real tests, or
// counted one twice. Shape unchanged; behaviour bump.
// "17" (2026-10-07): two more ways the critic's work was cut short without
// saying so. The Python scanner now honours a backslash inside a triple-quoted
// string (an escaped triple quote used to close it early and drop every test
// after it from the list), and lists nothing when it ends inside a string or
// an open bracket instead of an undercount. And a 401, 403 or 408 from a
// provider no longer sends the critic's plain re-ask (which spent a second
// large prompt to hit the same refusal); only 400, 413 and 422 do. A "16"
// critic count may have been held to a list that was missing real tests.
// Shape unchanged; behaviour bump.
// "18" (2026-10-13): the Verdict gained AuthoredLanding — the file a Rust
// authored test (a `mod corral_tests` block) must be appended to, the one file
// it compiles in. A cached "17" document unmarshals it as "", which would hand
// the block over with nowhere to put it; shape and generation change
// together. The same change admitted Rust files whose tests live in a file
// the source declares, so the exam also changed for them (no "17" verdict
// for such a file could exist: every .rs file was refused). The same bump
// covers BaselineFlaky (the tests a passing baseline passed only on a nextest
// retry), added in the same change: a "17" document unmarshals it as nil,
// which would read as "no test leaned on a retry". On certify --repo it is
// the union of the stability runs before the audit and the audit's own dev
// pass (folded in localExecutor.Execute before the verdict is recorded or
// cached). That fold landed before "18" left this branch, so no "18"
// verdict holds the narrower list and it needed no generation of its own.
// The same holds for the workspace pool's downgrade note
// (Verdict.Concurrency.Note), reworded under "18" to stop blaming the suite
// for what may be an incomplete copy: every verdict carrying the old words is
// a "17" one, which "18" already retires.
// "21" (2026-10-14): a seat can now be a coding-agent CLI. Two changes ride
// it. advpool.ModelCall gained UnmeteredCalls (model_calls[].unmetered_calls),
// so a `text`-wire seat's calls count as not metered rather than as measured
// zeros; that is inside a nested type the shallow fingerprint below does not
// see, and a cached document without it would read an unmetered seat's calls
// as free. And the Verdict gained SeatTools, each agent seat's CLI and version
// beside the plain spec in ModelsByRole (mirroring review's ReviewerTool), so
// a cached verdict without it would read an agent seat as an API one. The
// bug-catching rows a converged verdict feeds now mark an unpinned agent seat
// so per-model analysis leaves it out, and the cache key names an agent
// seat's tool (seatToolProbe.keyName, via modelSetKeyWith in cmd/corral), so
// a CLI upgrade re-measures. "19" and "20" are burned, never assigned: the
// agent-seats branch stamped "18" and "19" on its own acceptance runs before
// "18" landed on main for Rust, and an unreleased build combining both
// branches stamped "20" into a local ledger. Skipping both keeps any of those
// entries from being read as a verdict of this shape.
// "22" (2026-10-15): for PHP the critic is now handed the test file's tests
// by name (lang.Plugin.TestNamesInSource, PHPUnit's Class::method) and must
// judge every one, as Go and Python have since "14". Without the list a
// claude-haiku-4-5 critic on webmozart/assert's AssertTest.php judged 1, 5,
// 6 or 0 of its 14 tests across four replays, named data-provider cases as
// tests, and twice answered with no tests at all, which sent it to the tool
// loop. A "21" PHP critic count may be a partial review read as complete.
// Shape unchanged; behaviour bump.
// "23" (2026-10-15): Rust gets the same list (lang.Plugin.TestNamesInSource:
// libtest's test functions by their module path within the file), so a
// Rust critic must judge every test as Go, Python and PHP critics do. A
// "22" Rust critic count came from an answer nothing held to the file's
// tests. Shape unchanged; behaviour bump.
// "24" (2026-10-15): the Verdict gained RefutedFindings, the critic claims
// execution disproved, and VacuousFindings no longer holds them. A cached
// "23" document unmarshals RefutedFindings as nil and still counts its
// refuted claims (and, from certify --repo, never ran the check at all), so
// its critic count would be served as one that excludes them.
const VerdictGeneration = "24"

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
const VerdictShapeSHA256 = "896d49ca72d396cf32101ea0f91527b315f761e7a47f9069a91605843a0fc1cc"
