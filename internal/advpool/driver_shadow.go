// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
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

// WriterProviderFailedResult is the same sentinel for the PRIMARY test-writer
// seat: the model call itself failed (unreachable, 429, 5xx), so there is no
// test to compile, score, or blame the model for.
//
// It exists because without it the driver's caller returned the transport
// error, the whole file became "could not audit: running role test-writer:
// model unreachable", and the dev-adequacy result — minutes of real scoring,
// already computed and logged as "dev's OWN tests scored 50%" — was thrown
// away as COULD-NOT-GRADE. That is the repository's dominant defect shape: a
// measurement computed and then discarded. A writer that never answered is
// no different, for the verdict, from a writer that never compiled; the run
// converges with the measured kill rate, provenMissed 0, and the flag set.
const WriterProviderFailedResult = "\x00writer-provider-call-failed\x00"

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
		// The SAME command the primary dev pass ran (DevCommand), not the
		// run's raw TestCmd: this is a region-controlled head-to-head, and
		// grading the challenger's mutants against the whole suite while the
		// primary's were graded against the selection compares two different
		// exams and calls the difference a model result.
		// (And, when the scorer can grade per mutant, the same per-mutant
		// closure: a challenger mutant must face the tests that reach ITS
		// span, exactly as the primary's did.)
		_, shadowSurvivors, sserr := scoreDevSurvivors(sctx, d.Scorer, run.rs, parsed)
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

// enqueueShadowWriter adds the CHALLENGER writer seat to the run, carrying the
// SAME instruction and the same target as the primary writer's task under its
// own role key. Called once, from tickDevAdequacy, at the moment the primary
// is promoted with the survivors.
//
// Never fatal, and never returns an error: the seat records a comparison and
// nothing else, so an enqueue failure logs and leaves the challenger absent —
// exactly the outcome an operator who never named a challenger model gets.
func (d *Driver) enqueueShadowWriter(missionID int64, run *runState, title, instruction string) {
	if run.shadowWriterTaskID != 0 {
		return
	}
	if err := d.Q.Enqueue(missionID, []queue.TaskSpec{{
		Key:         RoleTestWriterShadow,
		Role:        RoleTestWriterShadow,
		Title:       "Challenger: " + title,
		Instruction: instruction,
		Model:       run.rs.ShadowWriterModel,
	}}); err != nil {
		log.Printf("advpool: run %d: could not enqueue the challenger writer seat (measurement only): %v", missionID, err)
		return
	}
	tasks, lerr := d.tasksByRole(missionID, RoleTestWriterShadow)
	if lerr != nil || len(tasks) == 0 {
		log.Printf("advpool: run %d: challenger writer seat enqueued but not found (measurement only): %v", missionID, lerr)
		return
	}
	run.shadowWriterTaskID = tasks[len(tasks)-1].ID
}

// runShadowWriterPass scores the CHALLENGER writer's own authored suite against
// run.devSurvivors — the IDENTICAL set the primary writer is scored against
// (tickPoolAdequacy's scoreAuthored call), and the set both writers were
// actually asked to kill — so the head-to-head measures the WRITERS and not the
// difficulty of two different exams.
//
// RULING P9: the universe is devSurvivors, NOT run.mutants. The paired vectors
// are the two WRITERS' proven-kill sets — run.provenIDs for the primary and
// run.shadowWriterKilled here — never run.devKilled, which is the DEV SUITE's
// vector over every mutant. Pairing a writer against the dev suite would
// compare a writer to the developer's own tests, which is a different question
// from the one this seat exists to answer. devSurvivors is typically small, so
// the downstream comparison will often be honestly under-powered; a correct
// measurement that says "insufficient data" beats a confident measurement of
// the wrong quantity.
//
// It is MEASUREMENT, held to the same invariants runShadowPass is:
//
//  1. A challenger failure is NEVER fatal. Every error path logs and leaves the
//     seat unmeasured; nothing returns an error to Tick.
//
//  2. It cannot change the run's grade. It never writes devKilled,
//     devSurvivors, testWriterAttempts, testWriterFailed or poolTestUnsound;
//     its outcome never reaches aggregate() or the Verdict; it has its OWN
//     retry budget (MaxShadowWriterAttempts) so a challenger that will not
//     compile cannot starve the graded seat's retries; and the wall-clock it
//     spends is credited back to the run's deadline clock (bounded, in
//     aggregate across every entry, by ShadowTimeBudget) so enabling the
//     challenger cannot tip a would-be-certified run into a needs-review
//     TIMEOUT.
//
// UNMEASURED IS NOT ZERO. A suite that never compiled, never passed its
// baseline, or never reached the file under audit produces an all-survive
// vector that would read as a catastrophic blind spot in the challenger. Every
// such path leaves shadowWriterMeasured false — recording nothing rather than a
// fabricated zero, for the same reason the challenger generator's `measured`
// flag exists.
func (d *Driver) runShadowWriterPass(ctx context.Context, missionID int64, run *runState) {
	if strings.TrimSpace(run.rs.ShadowWriterModel) == "" {
		return
	}
	if run.writerMode == WriterModePerSurvivor {
		// The challenger fans out with the primary; its per-survivor body is
		// runShadowWriterFanout. Routed here rather than at the call site so
		// tickPoolAdequacy has one challenger entry point either way.
		d.runShadowWriterFanout(ctx, missionID, run)
		return
	}
	if run.shadowWriterMeasured || run.shadowWriterCompileRetries >= MaxShadowWriterAttempts {
		return
	}
	if run.shadowWriterTaskID == 0 {
		return
	}
	task, terr := d.Q.TaskByID(run.shadowWriterTaskID)
	if terr != nil {
		log.Printf("advpool: run %d: challenger writer seat unavailable (measurement only): %v", missionID, terr)
		return
	}
	if task == nil || task.Status != queue.StatusDone {
		// Never finished (still pending/claimed, or superseded): there is
		// nothing to measure, and — critically — this must NOT hold up the
		// primary run.
		return
	}
	if task.Result == ShadowProviderFailedResult {
		// The challenger's LLM call itself failed: there is no output to parse,
		// and parsing it anyway would record a fabricated failure for a model
		// that was never asked the question. Leave it unmeasured, and do NOT
		// charge its own retry budget for something it never did.
		return
	}

	budget := ShadowTimeBudget(d.RunDeadline)
	started := d.Now()
	if budget > 0 {
		// Credit this pass's wall-clock spend back to the run's deadline clock
		// on EVERY exit path, capped so the CUMULATIVE credit across every
		// entry cannot exceed the shadow budget — see runShadowPass's
		// invariant (2b) for why the cap matters, and shadowWriterSpent for why
		// it is cumulative here.
		defer func() {
			elapsed := d.Now().Sub(started)
			if left := budget - run.shadowWriterSpent; elapsed > left {
				elapsed = left
			}
			if elapsed <= 0 {
				return
			}
			run.shadowWriterSpent += elapsed
			run.startedAt = run.startedAt.Add(elapsed)
		}()
	}

	shadowTest := d.Validator.ParseTest(task.Result)
	if cerr := d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, shadowTest); cerr != nil {
		// The challenger's OWN budget — never run.testWriterAttempts.
		run.shadowWriterCompileRetries++
		if run.shadowWriterCompileRetries >= MaxShadowWriterAttempts {
			log.Printf("advpool: run %d: the challenger writer produced no compiling test after %d attempt(s) — recorded as UNMEASURED, not as zero kills: %v",
				missionID, run.shadowWriterCompileRetries, cerr)
			return
		}
		// A corrective retry, mirroring the primary's: feed the compiler's own
		// error back, except when the model returned nothing at all (a repair
		// prompt over an empty test only begets more emptiness) — then re-issue
		// a fresh prompt.
		instr := task.Instruction
		if strings.TrimSpace(shadowTest) != "" {
			var ce *CompileError
			compileMsg := cerr.Error()
			if errors.As(cerr, &ce) && strings.TrimSpace(ce.Output) != "" {
				compileMsg = ce.Output
			}
			instr = renderTestWriterWithRepair(run.rs, run.sigs, run.devSurvivors, shadowTest, compileMsg)
		}
		newID, serr := d.Q.SupersedeTask(task.ID, queue.TaskSpec{
			Key:         RoleTestWriterShadow,
			Role:        RoleTestWriterShadow,
			Title:       task.Title,
			Instruction: instr,
			Model:       run.rs.ShadowWriterModel,
		})
		if serr != nil {
			log.Printf("advpool: run %d: could not reissue the challenger writer (measurement only): %v", missionID, serr)
			return
		}
		run.shadowWriterTaskID = newID
		if _, perr := d.Q.PromoteReady(missionID); perr != nil {
			log.Printf("advpool: run %d: could not promote the reissued challenger writer (measurement only): %v", missionID, perr)
		}
		return
	}

	sctx := ctx
	if budget > 0 {
		left := budget - run.shadowWriterSpent - d.Now().Sub(started)
		if left <= 0 {
			log.Printf("advpool: run %d: shadow budget (%s) spent — the challenger writer is recorded as UNMEASURED, not as zero kills", missionID, budget)
			return
		}
		var cancel context.CancelFunc
		sctx, cancel = context.WithTimeout(ctx, left)
		defer cancel()
	}

	// The SAME adequacy path the primary's authored test goes through, against
	// the SAME slice (run.devSurvivors). Neither the mutants nor the survivor
	// set is ever regenerated for the challenger: two writers facing different
	// mutants is confounded by mutant difficulty.
	rep, serr := scoreAuthored(sctx, d.Scorer, run.rs.CodePath, run.rs.Code, shadowTest, run.devSurvivors, run.rs.TestCmd)
	if serr != nil {
		// Infrastructure, not a challenger verdict — leave it unmeasured rather
		// than recording a zero the comparison would read as a blind spot.
		log.Printf("advpool: run %d: challenger writer scoring failed (measurement only): %v", missionID, serr)
		return
	}
	// BEFORE any kill data is read: a suite that did not pass on the unmutated
	// code, whose canary survived, whose own file the command never reached, or
	// that scored nothing at all never ran the code under audit. Its empty
	// Killed list is an ABSENCE OF MEASUREMENT, and reading it as a kill vector
	// would report a challenger that never ran as one that caught nothing.
	if !rep.CompliantPass || !rep.CanaryKilled || rep.AuthoredTestUnreached || rep.Total == 0 {
		log.Printf("advpool: run %d: the challenger writer's test compiled but did not genuinely grade (CompliantPass=%v CanaryKilled=%v AuthoredTestUnreached=%v Total=%d) — recorded as UNMEASURED, not as zero kills",
			missionID, rep.CompliantPass, rep.CanaryKilled, rep.AuthoredTestUnreached, rep.Total)
		return
	}
	// Derived through provenMutantIDs — the SAME function that produces the
	// primary's run.provenIDs — so the two paired vectors are computed by one
	// rule and can never disagree about what "proven" means.
	run.shadowWriterKilled = provenRefs(rep, run.devSurvivors)
	run.shadowWriterMeasured = true
	log.Printf("advpool: run %d: the challenger writer (%s) proved %d of %d survivor(s) — measurement only, it does not gate this verdict",
		missionID, run.rs.ShadowWriterModel, len(run.shadowWriterKilled), len(run.devSurvivors))
}

// provenRefs is provenMutantIDs in MutantRef shape: the survivors an authored
// suite actually killed, carrying ParentSHA256 so a recorded attempt names the
// mutant the way scan_mutants does. Deliberately derived FROM provenMutantIDs
// rather than re-deriving the same set from rep.Killed — one rule for "proven",
// shared by both writers' vectors, so MutantRef.ID pairs one-for-one with the
// primary's run.provenIDs.
func provenRefs(rep adequacy.Report, survivors []adequacy.Mutant) []MutantRef {
	byID := make(map[string]adequacy.Mutant, len(survivors))
	for _, m := range survivors {
		byID[m.ID] = m
	}
	ids := provenMutantIDs(rep, survivors)
	refs := make([]MutantRef, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			refs = append(refs, MutantRef{ID: m.ID, ParentSHA256: m.ParentSHA256, Span: m.Span, Shape: adequacy.ShapeOf(m)})
		}
	}
	return refs
}

// enqueueShadowWritersPerSurvivor adds ONE challenger writer seat per
// survivor, mirroring the primary's fan-out exactly.
//
// The shapes must match or the comparison is confounded by the shape rather
// than by the model: a challenger asked one batched question while the primary
// answers N per-survivor ones is being given a different (and harder) task,
// and the difference would be recorded as a model result. Enqueued here,
// beside the primary, so the challenger costs the run no extra ticks.
//
// Never fatal: the seat records a comparison and nothing else, so an enqueue
// failure logs and leaves the challenger absent.
func (d *Driver) enqueueShadowWritersPerSurvivor(missionID int64, run *runState, survivors []adequacy.Mutant) {
	if len(run.shadowWriterOrder) > 0 {
		return
	}
	specs := make([]queue.TaskSpec, 0, len(survivors))
	for _, m := range survivors {
		sys, user := renderTestWriterSeat(run.rs, run.sigs, m, "", "", "")
		specs = append(specs, queue.TaskSpec{
			Key:         ShadowTestWriterTaskKey(m.ID),
			Role:        RoleTestWriterShadow,
			Title:       "Challenger: " + writerSeatTitle(m),
			System:      sys,
			Instruction: user,
			Model:       run.rs.ShadowWriterModel,
		})
	}
	if err := d.Q.Enqueue(missionID, specs); err != nil {
		log.Printf("advpool: run %d: could not enqueue the per-survivor challenger writer seats (measurement only): %v", missionID, err)
		return
	}
	tasks, lerr := d.tasksByRole(missionID, RoleTestWriterShadow)
	if lerr != nil {
		log.Printf("advpool: run %d: challenger writer seats enqueued but not found (measurement only): %v", missionID, lerr)
		return
	}
	byKey := make(map[string]int64, len(tasks))
	for _, t := range liveTasks(tasks) {
		byKey[t.Key] = t.ID
	}
	run.shadowWriterAttempts = map[string]*writerAttempt{}
	for _, m := range survivors {
		id, ok := byKey[ShadowTestWriterTaskKey(m.ID)]
		if !ok {
			// UNMEASURED, not zero: a seat this run cannot name is a seat it
			// can never read, and the challenger must be absent from the
			// comparison rather than present with an empty kill vector.
			log.Printf("advpool: run %d: challenger writer seat for survivor %s is missing — it is UNMEASURED, not zero kills", missionID, m.ID)
			continue
		}
		run.shadowWriterAttempts[m.ID] = &writerAttempt{mutant: m, taskID: id}
		run.shadowWriterOrder = append(run.shadowWriterOrder, m.ID)
	}
}

// runShadowWriterFanout is runShadowWriterPass's per-survivor body: it
// advances each challenger seat the same way the primary's advances, and
// records the challenger's proven-kill vector only once every seat is
// terminal.
//
// It is held to the SAME invariants runShadowWriterPass is (see that
// function's doc): never fatal, never able to change the run's grade, its own
// retry budget, and UNMEASURED IS NOT ZERO — a seat that never compiled, never
// passed its baseline or never reached the file leaves no entry in the vector
// and does not, by its absence, claim the challenger missed that survivor.
// That last point is why shadowWriterMeasured is set only when at least one
// seat genuinely graded.
func (d *Driver) runShadowWriterFanout(ctx context.Context, missionID int64, run *runState) {
	if run.shadowWriterDone {
		return
	}
	for _, id := range run.shadowWriterOrder {
		a := run.shadowWriterAttempts[id]
		if a.done {
			continue
		}
		task, terr := d.Q.TaskByID(a.taskID)
		if terr != nil {
			log.Printf("advpool: run %d: challenger writer seat for %s unavailable (measurement only): %v", missionID, id, terr)
			a.done = true
			continue
		}
		if task == nil || task.Status != queue.StatusDone {
			// Still working. This must never hold up the primary, so the
			// pass simply returns and comes round again.
			return
		}
		if task.Result == ShadowProviderFailedResult {
			// The call itself failed: there is no output to parse, and
			// parsing it anyway would record a fabricated failure for a model
			// that was never asked the question.
			a.done = true
			continue
		}

		test := d.Validator.ParseTest(task.Result)
		if cerr := d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, test); cerr != nil {
			a.repairs++
			if a.repairs >= MaxShadowWriterAttempts {
				log.Printf("advpool: run %d: the challenger produced no compiling test for survivor %s after %d attempt(s) — UNMEASURED, not zero kills: %v",
					missionID, id, a.repairs, cerr)
				a.done = true
				continue
			}
			sys, instr := renderTestWriterSeat(run.rs, run.sigs, a.mutant, "", "", "")
			if strings.TrimSpace(test) != "" {
				var ce *CompileError
				msg := cerr.Error()
				if errors.As(cerr, &ce) && strings.TrimSpace(ce.Output) != "" {
					msg = ce.Output
				}
				sys, instr = renderTestWriterSeat(run.rs, run.sigs, a.mutant, test, msg, "")
			}
			newID, serr := d.Q.SupersedeTask(task.ID, queue.TaskSpec{
				Key:         ShadowTestWriterTaskKey(a.mutant.ID),
				Role:        RoleTestWriterShadow,
				Title:       task.Title,
				System:      sys,
				Instruction: instr,
				Model:       run.rs.ShadowWriterModel,
			})
			if serr != nil {
				log.Printf("advpool: run %d: could not reissue the challenger writer for %s (measurement only): %v", missionID, id, serr)
				a.done = true
				continue
			}
			a.taskID = newID
			if _, perr := d.Q.PromoteReady(missionID); perr != nil {
				log.Printf("advpool: run %d: could not promote the reissued challenger writer for %s (measurement only): %v", missionID, id, perr)
			}
			return
		}

		rep, serr := scoreAuthored(ctx, d.Scorer, run.rs.CodePath, run.rs.Code, test, []adequacy.Mutant{a.mutant}, run.rs.TestCmd)
		if serr != nil {
			log.Printf("advpool: run %d: challenger writer scoring for %s failed (measurement only): %v", missionID, id, serr)
			a.done = true
			continue
		}
		a.done = true
		if !rep.CompliantPass || !rep.CanaryKilled || rep.AuthoredTestUnreached || rep.Total == 0 {
			continue
		}
		a.measured = true
		a.proven = len(provenMutantIDs(rep, []adequacy.Mutant{a.mutant})) > 0
	}

	run.shadowWriterDone = true
	measured := 0
	for _, id := range run.shadowWriterOrder {
		a := run.shadowWriterAttempts[id]
		if !a.measured {
			continue
		}
		measured++
		if a.proven {
			run.shadowWriterKilled = append(run.shadowWriterKilled,
				MutantRef{ID: a.mutant.ID, ParentSHA256: a.mutant.ParentSHA256, Span: a.mutant.Span, Shape: adequacy.ShapeOf(a.mutant)})
		}
	}
	if measured == 0 {
		// Nothing graded anywhere: the challenger is ABSENT from the
		// comparison rather than present with an all-survive vector.
		return
	}
	run.shadowWriterMeasured = true
	log.Printf("advpool: run %d: the challenger writer (%s) proved %d of %d survivor(s) across %d graded seat(s) — measurement only, it does not gate this verdict",
		missionID, run.rs.ShadowWriterModel, len(run.shadowWriterKilled), len(run.shadowWriterOrder), measured)
}
