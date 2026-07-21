// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/queue"
)

// ShadowProviderFailedResult is the sentinel a shadow (challenger) seat is
// completed with when the LLM call itself failed — a network error, a 429, a
// typo'd --shadow-model — rather than the model responding with output that
// merely failed to parse (see cmd/corral's runOneTask). The two cases MUST be
// kept distinguishable: without this sentinel, both a never-asked model and a
// model that answered with garbage would leave the same empty Result, and
// ParseMutants("") fails identically for both — the parse failure alone gives
// runShadowPass no way to tell "the model was never asked" apart from "the
// model answered with garbage". That ambiguity used to route the never-asked
// case straight into the parse-failure branch and record a MEASURED,
// DROPPED, zero-yield row for a model that never ran.
// That is data fabrication: it attributes a bad result to a model that was
// never asked the question, and it would have landed in the shared scorecard
// store that feeds model routing — exactly the corruption the `measured`
// flag exists to prevent. Recording nothing is strictly better than
// recording a fabricated zero: an absent row is honestly "we don't know",
// while a fabricated row is confidently wrong.
const ShadowProviderFailedResult = "\x00shadow-provider-call-failed\x00"

// ShadowTimeBudget is the hard wall-clock cap on ALL of a run's shadow
// measurement work, derived from the run's deadline. Shadow scoring runs real
// jail executions of the dev suite — a second full Scorer.Score per shard — so
// it must be bounded twice over: this cap bounds how long it may take, and
// runShadowPass credits whatever it does spend back to the run's deadline
// clock so it cannot consume the PRIMARY run's budget.
//
// A zero deadline (the pure-unit-test / no-backstop case) means there is no
// budget to protect, so shadow work is unbounded there.
func ShadowTimeBudget(deadline time.Duration) time.Duration {
	if deadline <= 0 {
		return 0
	}
	return deadline / 4
}

// ResolveRunDeadline sizes a run's wall-clock backstop (Driver.RunDeadline)
// so shadow work can never change the run's Status by pushing it past the
// deadline into a timeout needs-review verdict (see timeoutVerdict). When a
// shadow model is configured it widens base by ShadowTimeBudget(base) — the
// SAME allowance a caller's own outer bound should give itself alongside it.
//
// This closes a gap runShadowPass's own credit-back does not: that credit
// only returns the wall-clock runShadowPass itself spends SCORING shadow
// mutants. The challenger's mutant-GENERATION LLM calls happen entirely
// outside the driver — in cmd/corral's in-process drive loop for `certify
// --local`, and in a REMOTE worker claiming a queued task for the hosted
// brain — so nothing credits that generation time back the way runShadowPass
// credits scoring. With shadow on (the default) roughly doubling generator
// calls, that uncredited generation wall-clock can by itself carry a run past
// RunDeadline before it converges. Widening the deadline itself gives
// generation the same headroom scoring already has, on both callers.
//
// Shared by cmd/corral/certify_local.go (resolveRunDeadline, called with the
// CLI's --timeout) and internal/brain's StartAdversarialPool (called with the
// daemon's already-resolved driver.RunDeadline) — the two callers differ only
// in what "base" means for them (a per-run --timeout vs. a daemon-wide
// startup constant), not in the widening arithmetic itself.
func ResolveRunDeadline(base time.Duration, shadow string) time.Duration {
	d := base
	if strings.TrimSpace(shadow) != "" {
		d += ShadowTimeBudget(base)
	}
	return d
}

// runShadowPass scores the challenger seats' mutants against the SAME dev
// suite as the primary, so the head-to-head measures POTENCY (mutants that
// survive a good suite) rather than mere output volume. It is MEASUREMENT, and
// is held to two invariants that the role key alone cannot enforce:
//
//  1. A shadow failure is NEVER fatal. Every error path here logs and leaves
//     the seat unmeasured; nothing returns an error to Tick.
//
//  2. Shadow work can never change the run's Status. RunDeadline is a
//     wall-clock budget from run start, and exceeding it forces a
//     needs-review TIMEOUT verdict — so absent a guard, enabling shadow could
//     flip a would-be-certified run to needs-review purely by making it
//     slower. That would breach "shadow never gates" through a channel the
//     role key cannot close. Two mechanisms close it together:
//     (a) every shadow Score is bounded by ShadowTimeBudget, and the pass
//     stops as soon as that budget is spent (remaining seats are recorded
//     as UNMEASURED, never as a challenger that produced nothing); and
//     (b) the wall-clock time this pass consumes is credited back to the
//     run's deadline clock by advancing startedAt — but ONLY up to
//     ShadowTimeBudget (min(elapsed, budget); see the clamp below), so the
//     credit itself cannot exceed what (a) is supposed to bound. That cap
//     bounds shadow's charge against the deadline to at most the shadow
//     budget: any overspend beyond the budget IS charged against the primary
//     deadline. The guarantee is therefore not purely structural — it still
//     depends on Scorer.Score honoring the context this pass hands it (sctx
//     below), so (a) can actually cut a call off rather than merely being
//     ignored. The production jail Scorer does honor its context, keeping
//     the behavioral risk low, but a Scorer that ignores sctx and runs long
//     can still consume up to ShadowTimeBudget of the primary run's margin.
//     (a) also exists so the caller's own outer context (which must allow
//     deadline + this budget; see cmd/corral's certify --local) stays
//     bounded.
func (d *Driver) runShadowPass(ctx context.Context, missionID int64, run *runState) {
	shadows, serr := d.tasksByRole(missionID, RoleMutantGeneratorShadow)
	if serr != nil {
		log.Printf("advpool: run %d: shadow seats unavailable (measurement only): %v", missionID, serr)
		return
	}

	budget := ShadowTimeBudget(d.RunDeadline)
	started := d.Now()
	if budget > 0 {
		// Credit the pass's wall-clock spend back to the run's deadline clock
		// on EVERY exit path — see invariant (2b) above. Capped at `budget`:
		// crediting the raw elapsed time would let a Scorer that ignored its
		// own context (sctx below) extend the deadline arbitrarily by simply
		// running long, which would blow past the CALLER's own outer bound
		// (see cmd/corral's outerBound) — failing worse than the timeout this
		// exists to avoid (an ungraceful exit 1/no verdict, instead of the
		// honest signed needs-review the deadline is supposed to produce).
		defer func() {
			elapsed := d.Now().Sub(started)
			if elapsed > budget {
				elapsed = budget
			}
			run.startedAt = run.startedAt.Add(elapsed)
		}()
	}

	for i := range shadows {
		if shadows[i].Status != queue.StatusDone {
			// Never finished (still pending/claimed, or superseded): there is
			// nothing to measure, and — critically — this must NOT hold up the
			// primary run, which has already scored above.
			continue
		}
		if shadows[i].Result == ShadowProviderFailedResult {
			// The challenger's LLM call itself failed (see
			// ShadowProviderFailedResult) — there is no output to attempt to
			// parse, and running it through ParseMutants would fabricate an
			// observed parse failure for a model that was never asked the
			// question. Leave the seat unmeasured, exactly like the
			// still-claimed/skipped-by-budget cases below.
			continue
		}
		idx, ok := ShadowShardIndexFromKey(shadows[i].Key)
		if !ok {
			// A key this function cannot parse would otherwise silently become
			// index 0 and collapse this seat onto shard 0's row, mis-attributing
			// one region's difficulty control to another. Skip it, loudly —
			// matching the log-and-degrade pattern the rest of the shadow path
			// uses.
			log.Printf("advpool: run %d: shadow seat key %q does not parse to a shard index — skipping (measurement only)", missionID, shadows[i].Key)
			continue
		}

		sctx := ctx
		var cancel context.CancelFunc
		if budget > 0 {
			left := budget - d.Now().Sub(started)
			if left <= 0 {
				log.Printf("advpool: run %d: shadow budget (%s) spent — skipping the remaining challenger seats; they are recorded as UNMEASURED, not as zero yield", missionID, budget)
				return
			}
			sctx, cancel = context.WithTimeout(ctx, left)
		}

		parsed, perr := d.Validator.ParseMutants(shadows[i].Result, run.rs.Code)
		if perr != nil {
			// A real, observed challenger failure: it produced output the
			// validator could not use. That IS a measurement, so mark it so.
			st := run.shadowStats[idx]
			st.parseRetries++
			st.dropped = true
			st.measured = true
			run.shadowStats[idx] = st
			if cancel != nil {
				cancel()
			}
			continue
		}
		_, shadowSurvivors, sserr := d.Scorer.Score(sctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, parsed, run.rs.TestCmd)
		// Release sctx's timeout right after the call it bounded, rather than
		// deferring to the end of the pass — correctness must not depend on
		// reasoning about how many shards (and therefore deferred cancels) may
		// accumulate before this function returns.
		if cancel != nil {
			cancel()
		}
		if sserr != nil {
			// Infrastructure, not a challenger verdict — leave it unmeasured
			// rather than recording a zero the scorecard would read as yield.
			log.Printf("advpool: run %d: shadow shard %d scoring failed (measurement only): %v", missionID, idx, sserr)
			continue
		}
		st := run.shadowStats[idx]
		st.mutants = len(parsed)
		st.survived = len(shadowSurvivors)
		st.measured = true
		run.shadowStats[idx] = st
	}
}
