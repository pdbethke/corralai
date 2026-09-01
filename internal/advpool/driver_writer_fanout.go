// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/queue"
)

// writerSeatTitle labels one survivor's writer seat, so the queue and the
// cockpit show WHICH survivor each seat is attacking rather than N identical
// rows.
func writerSeatTitle(m adequacy.Mutant) string {
	return "Write test killing survivor " + m.ID
}

// promotePerSurvivorWriters replaces the single dep-blocked test-writer task
// with ONE seat per survivor, all claimable at once.
//
// The first survivor SUPERSEDES the original task rather than being enqueued
// beside it, for the same reason the batched path supersedes: test-writer's
// DependsOn=[DevAdequacyKey] can never be satisfied by a worker (dev-adequacy
// is driver-internal bookkeeping), so the original row must be replaced, not
// left to deadlock. The rest are ordinary enqueues — they carry no dep at all.
//
// Every seat is tracked by TASK ID, never re-looked-up by key, because a
// repair supersedes the row and SupersedeTask auto-uniquifies a replacement
// that reuses the old key.
func (d *Driver) promotePerSurvivorWriters(missionID int64, run *runState, tw *queue.Task, survivors []adequacy.Mutant) error {
	first := survivors[0]
	firstSys, firstUser := renderTestWriterSeat(run.rs, run.sigs, first, "", "", "")
	newID, err := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
		Key:         TestWriterTaskKey(first.ID),
		Role:        RoleTestWriter,
		Title:       writerSeatTitle(first),
		System:      firstSys,
		Instruction: firstUser,
		Model:       tw.Model,
	})
	if err != nil {
		return fmt.Errorf("advpool: promote test-writer for survivor %s: %w", first.ID, err)
	}
	run.testWriterTaskID = newID
	run.writerAttempts[first.ID] = &writerAttempt{mutant: first, taskID: newID}
	run.writerOrder = append(run.writerOrder, first.ID)

	if len(survivors) > 1 {
		specs := make([]queue.TaskSpec, 0, len(survivors)-1)
		for _, m := range survivors[1:] {
			sys, user := renderTestWriterSeat(run.rs, run.sigs, m, "", "", "")
			specs = append(specs, queue.TaskSpec{
				Key:         TestWriterTaskKey(m.ID),
				Role:        RoleTestWriter,
				Title:       writerSeatTitle(m),
				System:      sys,
				Instruction: user,
				Model:       tw.Model,
			})
		}
		if err := d.Q.Enqueue(missionID, specs); err != nil {
			return fmt.Errorf("advpool: enqueue the remaining per-survivor writer seats: %w", err)
		}
	}

	// Resolve the enqueued seats' ids from the queue's own listing. Enqueue
	// does not hand them back, and a seat whose id this run cannot name is a
	// seat it can never repair or read.
	tasks, lerr := d.tasksByRole(missionID, RoleTestWriter)
	if lerr != nil {
		return fmt.Errorf("advpool: list the per-survivor writer seats: %w", lerr)
	}
	byKey := make(map[string]int64, len(tasks))
	for _, t := range liveTasks(tasks) {
		byKey[t.Key] = t.ID
	}
	for _, m := range survivors[1:] {
		id, ok := byKey[TestWriterTaskKey(m.ID)]
		if !ok {
			return fmt.Errorf("advpool: per-survivor writer seat for %s was enqueued but is not in the queue", m.ID)
		}
		run.writerAttempts[m.ID] = &writerAttempt{mutant: m, taskID: id}
		run.writerOrder = append(run.writerOrder, m.ID)
	}
	log.Printf("advpool: run %d: the writer seat fans out into %d per-survivor call(s) sharing one cacheable prefix", missionID, len(survivors))
	return nil
}

// tickWriterFanout is tickPoolAdequacy's per-survivor body: it advances every
// seat that has a result waiting, and returns done=false while any seat is
// still owed one.
//
// The tick is complete when EVERY attempt is terminal — proven, exhausted, or
// salvaged — never when the first one is and never on a tick count. A seat
// still pending simply leaves done false and the run ticks again; that is the
// same shape the sharded generator uses, and it is what makes the fan-out safe
// to run at whatever concurrency the caller's worker pool happens to have.
//
// The returned error mirrors the batched path's: a compile failure is reported
// as an error (so the caller's progress log says what happened) even though
// the seat was successfully reissued and the run is fine.
func (d *Driver) tickWriterFanout(ctx context.Context, missionID int64, run *runState) (done bool, err error) {
	var firstErr error
	pending := 0
	for _, id := range run.writerOrder {
		a := run.writerAttempts[id]
		if a.done {
			continue
		}
		advanced, aerr := d.advanceWriterAttempt(ctx, missionID, run, a)
		if aerr != nil && firstErr == nil {
			firstErr = aerr
		}
		if !advanced && !a.done {
			pending++
		}
	}
	if pending > 0 || firstErr != nil {
		// firstErr holds a reissue back: the seat has a fresh task and the
		// run must come round again for it, exactly as the batched path does
		// after a compile-feedback supersede.
		return false, firstErr
	}
	for _, id := range run.writerOrder {
		if !run.writerAttempts[id].done {
			return false, nil
		}
	}
	return true, nil
}

// advanceWriterAttempt moves ONE seat forward by at most one step: read its
// result, compile-check it, repair it, prove it, or salvage it. advanced
// reports whether this call actually consumed a result (as opposed to finding
// the seat still working).
func (d *Driver) advanceWriterAttempt(ctx context.Context, missionID int64, run *runState, a *writerAttempt) (advanced bool, err error) {
	task, terr := d.Q.TaskByID(a.taskID)
	if terr != nil {
		return false, fmt.Errorf("advpool: load writer seat for %s: %w", a.mutant.ID, terr)
	}
	if task == nil || task.Status != queue.StatusDone {
		return false, nil
	}
	a.attempts++

	test := d.Validator.ParseTest(task.Result)
	if cerr := d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, test); cerr != nil {
		a.repairs++
		if a.repairs >= MaxTestWriterAttempts {
			// EXHAUSTED, and only this survivor is. Under the batched shape
			// one unbuildable test spent the whole file's budget and every
			// survivor lost its chance with it; here the seat next door is
			// untouched.
			log.Printf("advpool: %s: the writer could not produce a compiling test for survivor %s after %d attempt(s) — that survivor is not proven; its siblings are unaffected: %v",
				run.rs.CodePath, a.mutant.ID, a.repairs, cerr)
			a.done = true
			return true, nil
		}
		sys, instr := renderTestWriterSeat(run.rs, run.sigs, a.mutant, "", "", "")
		if strings.TrimSpace(test) != "" {
			// A corrective retry: feed the compiler's own error back, except
			// when the model returned nothing at all — a repair prompt over
			// an empty test only begets more emptiness and burns the budget
			// in a degenerate loop, so that case gets a FRESH prompt instead.
			var ce *CompileError
			msg := cerr.Error()
			if errors.As(cerr, &ce) && strings.TrimSpace(ce.Output) != "" {
				msg = ce.Output
			}
			sys, instr = renderTestWriterSeat(run.rs, run.sigs, a.mutant, test, msg, "")
		}
		if rerr := d.reissueWriterSeat(missionID, run, a, task, sys, instr); rerr != nil {
			return true, rerr
		}
		return true, fmt.Errorf("advpool: writer seat for survivor %s does not compile, reissued for retry (%d/%d): %w",
			a.mutant.ID, a.repairs, MaxTestWriterAttempts, cerr)
	}

	a.compiled = true
	a.test = test
	only := []adequacy.Mutant{a.mutant}

	// PROVEN ALONE, against ITS survivor: the authored pass runs this test and
	// nothing else, over this mutant and nothing else. That is what makes the
	// proof name a survivor rather than being attributed to one after a
	// whole-file re-score.
	rep, serr := scoreAuthored(ctx, d.Scorer, run.rs.CodePath, run.rs.Code, test, only, run.rs.TestCmd)
	if serr != nil {
		return true, fmt.Errorf("advpool: score the authored test for survivor %s: %w", a.mutant.ID, serr)
	}

	if !rep.CompliantPass {
		// Salvage first, before spending another model call — the same
		// arithmetic-over-the-runner's-output rescue the batched path does,
		// scoped to this survivor.
		if salvaged, ids, n, ok := d.salvageByDeselect(ctx, run, test, rep, only); ok && salvaged > 0 {
			a.done, a.proven, a.measured, a.salvaged = true, true, true, true
			log.Printf("advpool: %s: the authored test for survivor %s failed on the unmutated code, but deselecting its %d failing test(s) left a sound remainder that PROVED it (%v)",
				run.rs.CodePath, a.mutant.ID, n, ids)
			return true, nil
		}
		if a.repairs < MaxTestWriterAttempts-1 {
			a.repairs++
			failure := compliantFailureOutput(ctx, d.Scorer, run, test)
			sys, instr := renderTestWriterSeat(run.rs, run.sigs, a.mutant, test, "", failure)
			if rerr := d.reissueWriterSeat(missionID, run, a, task, sys, instr); rerr != nil {
				return true, rerr
			}
			log.Printf("advpool: %s: the authored test for survivor %s compiled but FAILED on the unmutated code — reissuing with the failure fed back (%d/%d)",
				run.rs.CodePath, a.mutant.ID, a.repairs, MaxTestWriterAttempts)
			return true, nil
		}
	}

	a.done = true
	if !rep.CompliantPass || !rep.CanaryKilled || rep.Total == 0 {
		// A DIAGNOSIS, not a score. This seat's survivor is NOT proven, and
		// it is equally not disproven — nothing graded. Recorded as
		// unmeasured so it can never contribute a fabricated kill or a
		// fabricated blind spot.
		log.Printf("advpool: %s: the authored test for survivor %s compiled but did not genuinely grade (CompliantPass=%v CanaryKilled=%v Total=%d) — that survivor is unproven, not disproven",
			run.rs.CodePath, a.mutant.ID, rep.CompliantPass, rep.CanaryKilled, rep.Total)
		if rep.AuthoredTestUnreached {
			run.authoredTestNotCollected = true
		}
		return true, nil
	}
	a.measured = true
	a.proven = len(provenMutantIDs(rep, only)) > 0
	return true, nil
}

// reissueWriterSeat supersedes one seat with a new instruction and re-promotes,
// leaving the attempt pointing at the new task id. Only this survivor's row
// moves — its siblings' task ids are untouched, which is the property the
// fan-out exists for.
func (d *Driver) reissueWriterSeat(missionID int64, run *runState, a *writerAttempt, task *queue.Task, system, instruction string) error {
	newID, serr := d.Q.SupersedeTask(task.ID, queue.TaskSpec{
		Key:  TestWriterTaskKey(a.mutant.ID),
		Role: RoleTestWriter,
		// The prefix is re-rendered, not copied off the old row, and it is
		// byte-identical either way — a repaired seat must keep sharing its
		// siblings' cacheable prefix.
		System:      system,
		Title:       task.Title,
		Instruction: instruction,
		Model:       task.Model,
	})
	if serr != nil {
		return fmt.Errorf("advpool: reissue the writer seat for survivor %s: %w", a.mutant.ID, serr)
	}
	a.taskID = newID
	if a.mutant.ID == run.writerOrder[0] {
		// Keep the legacy single-task handle pointing at a live row:
		// RunStatus and the moot-cancel path still read it.
		run.testWriterTaskID = newID
	}
	if _, perr := d.Q.PromoteReady(missionID); perr != nil {
		return fmt.Errorf("advpool: promote the reissued writer seat for survivor %s: %w", a.mutant.ID, perr)
	}
	return nil
}

// writerAttemptSpread reports the min/median/max ATTEMPT count (the first
// try plus every repair) across a per-survivor run's terminal seats — see
// Verdict.WriterAttempts. nil on a batched run, or a per-survivor run whose
// fan-out never started, matching TestsPerMutantSpread's own "absent, not
// {0,0,0}" contract: a spread over zero seats is not a measurement.
func writerAttemptSpread(run *runState) *TestsPerMutantSpread {
	if run.writerMode != WriterModePerSurvivor || len(run.writerOrder) == 0 {
		return nil
	}
	counts := make([]int, 0, len(run.writerOrder))
	for _, id := range run.writerOrder {
		a, ok := run.writerAttempts[id]
		if !ok || a.attempts == 0 {
			continue
		}
		counts = append(counts, a.attempts)
	}
	if len(counts) == 0 {
		return nil
	}
	sort.Ints(counts)
	return &TestsPerMutantSpread{
		Min:    counts[0],
		Max:    counts[len(counts)-1],
		Median: counts[len(counts)/2],
	}
}

// finishWriterFanout folds every terminal seat into the run's verdict inputs:
// the proven count and its evidence, the concatenated authored file, and the
// three honesty flags.
//
// A PROVEN COUNT UNDER THIS MODE COUNTS EACH SURVIVOR AT MOST ONCE by
// construction — there is exactly one attempt per survivor and it contributes
// its own boolean — rather than by de-duplicating a list after the fact.
func (d *Driver) finishWriterFanout(run *runState) {
	var parts []golang.AuthoredPart
	compiled, measured, salvaged := 0, 0, 0
	for _, id := range run.writerOrder {
		a := run.writerAttempts[id]
		if a.compiled {
			compiled++
		}
		if a.measured {
			measured++
		}
		if a.salvaged {
			salvaged++
		}
		if !a.proven {
			continue
		}
		run.provenIDs = append(run.provenIDs, a.mutant.ID)
		parts = append(parts, golang.AuthoredPart{MutantID: a.mutant.ID, Source: a.test})
	}
	run.provenMissed = len(run.provenIDs)

	// The operator's file: the PROVEN tests only. An unproven seat's test is
	// not evidence of anything, and handing one back beside the proofs would
	// invite it into a suite on the strength of the others' claim.
	merged, extra := golang.ConcatAuthored(langFor(run.rs), parts)
	d.mu.Lock()
	run.authoredTest = merged
	d.mu.Unlock()
	run.authoredExtra = extra

	run.primaryWriterMeasured = measured > 0
	run.writerSalvaged = salvaged > 0
	// The two honest failures, kept apart exactly as the batched path keeps
	// them apart: nothing ever compiled (testWriterFailed) is a different
	// diagnosis from something compiled and never graded (poolTestUnsound),
	// and they send an operator to different places. Neither is set when at
	// least one seat graded — a file where three of four survivors were
	// proven is not a writer failure.
	switch {
	case measured > 0:
	case compiled == 0:
		run.testWriterFailed = true
		log.Printf("advpool: %s: no writer seat produced a compiling test for any of the %d survivor(s) — converging with proven_missed=0, which is NOT a clean suite",
			run.rs.CodePath, len(run.writerOrder))
	default:
		run.poolTestUnsound = true
		log.Printf("advpool: %s: %d writer seat(s) produced a compiling test but none genuinely graded — converging with proven_missed=0, which is NOT a clean suite",
			run.rs.CodePath, compiled)
	}
	run.poolScored = true

	if run.provenMissed > 0 {
		log.Printf("advpool: %s: the per-survivor writer PROVED %d of %d survivor(s) catchable by execution",
			run.rs.CodePath, run.provenMissed, len(run.writerOrder))
	} else if measured > 0 {
		log.Printf("advpool: %s: %d writer seat(s) graded soundly but killed NONE of the %d survivor(s) — a real 'tried and missed', not an ungraded run",
			run.rs.CodePath, measured, len(run.writerOrder))
	}
	if len(run.authoredExtra) > 0 {
		log.Printf("advpool: %s: %d proven test(s) could not be merged into one file (a name the language's concatenator will not rewrite) — they ride the verdict separately rather than being dropped",
			run.rs.CodePath, len(run.authoredExtra))
	}
}

// liveTasks drops the rows a supersede or a cancel left behind.
//
// tasksByRole lists EVERY task the role ever had, superseded ancestors
// included, which is right for the generator (a dropped shard is still a seat
// the run dispatched) and wrong for the writer fan-out: a seat repaired twice
// leaves two dead rows under its own key, and counting them would report three
// writer seats for one survivor — and, worse, could resolve that survivor's
// task id to a row nothing will ever complete.
func liveTasks(tasks []queue.Task) []queue.Task {
	out := make([]queue.Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Status == queue.StatusSuperseded || t.Status == queue.StatusCancelled {
			continue
		}
		out = append(out, t)
	}
	return out
}

// writerSeatsUngraded counts the per-survivor writer seats that never produced
// a test which genuinely graded — see Verdict.WriterSeatsUngraded.
//
// 0 in batched mode, which has no seats to count: that mode's total failure is
// already TestWriterFailed or PoolTestUnsound, and a 1 here would be a second,
// differently-shaped way of saying the same thing.
func (r *runState) writerSeatsUngraded() int {
	if r.writerMode != WriterModePerSurvivor {
		return 0
	}
	n := 0
	for _, id := range r.writerOrder {
		if !r.writerAttempts[id].measured {
			n++
		}
	}
	return n
}

// primarySeatMeasured reports whether the PRIMARY writer genuinely graded a
// test against THIS survivor.
//
// In batched mode there is one seat for the whole file, so the file-level flag
// is the per-survivor answer — anything narrower would silently stop recording
// rows the batched path has always recorded. Under the fan-out each survivor
// has its own seat and its own answer, and the file-level flag is true as soon
// as ANY seat grades, which is exactly why it cannot be reused here.
func (r *runState) primarySeatMeasured(mutantID string) bool {
	if r.writerMode != WriterModePerSurvivor {
		return r.primaryWriterMeasured
	}
	a, ok := r.writerAttempts[mutantID]
	return ok && a.measured
}

// shadowSeatMeasured is primarySeatMeasured for the CHALLENGER seat. The two
// sides are filtered independently: a survivor whose primary seat graded and
// whose challenger seat did not contributes the primary's row and no
// challenger row, rather than an invented pair.
func (r *runState) shadowSeatMeasured(mutantID string) bool {
	if r.writerMode != WriterModePerSurvivor {
		return r.shadowWriterMeasured
	}
	a, ok := r.shadowWriterAttempts[mutantID]
	return ok && a.measured
}

// primarySeatSalvaged reports whether THIS survivor's primary proof came from
// a deselected remainder rather than the suite the writer actually authored.
//
// Batched mode has one seat for the whole file, so the run-wide flag IS the
// per-survivor answer. Under the fan-out the rescue is per seat, and the
// run-wide flag is true as soon as ANY seat needed it — which is exactly why
// it cannot be reused here.
func (r *runState) primarySeatSalvaged(mutantID string) bool {
	if r.writerMode != WriterModePerSurvivor {
		return r.writerSalvaged
	}
	a, ok := r.writerAttempts[mutantID]
	return ok && a.salvaged
}

// primarySeatComparable is the primary side's full admission test for a
// head-to-head: the seat genuinely graded this survivor, AND its proof was
// not confounded by the salvage rescue the challenger never gets (RULING P11,
// see challengerVectors). Both consumers of the vectors — the durable attempt
// rows and the in-memory pair — filter through this one predicate so they
// cannot disagree about which survivors are comparable.
func (r *runState) primarySeatComparable(mutantID string) bool {
	return r.primarySeatMeasured(mutantID) && !r.primarySeatSalvaged(mutantID)
}
