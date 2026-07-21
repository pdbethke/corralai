// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/queue"
)

// localTickMaxErrors mirrors internal/brain/advpool.go's advPoolTickMaxErrors:
// the brain's tick loop tolerates up to this many CONSECUTIVE Tick errors on a
// run before giving up, because the driver deliberately returns a Tick error
// on RECOVERABLE conditions (an unparseable mutant-generator result or a
// non-compiling test-writer result) after REOPENING the task so a fresh claim
// re-prompts the model — see driver.go's tickDevAdequacy/tickPoolAdequacy
// ReopenTask+"reissued for retry" paths. A --local run must tolerate the same
// bound the brain does: giving up on the first such error would abort the
// whole audit on a frontier model's common non-compiling first attempt.
const localTickMaxErrors = 20

// driveLocalRun is the in-process drive loop — the testable seam. It advances
// the pure driver to convergence exactly the way the brain's tick loop + a
// remote worker interoperate, but with BOTH sides in one process: Tick advances
// the DAG (dev-adequacy scoring, test-writer promotion, pool-adequacy, aggregate,
// signing) while runReadyTasks claims each ready role task and runs it through
// the in-process agentworker.RunRole, completing it (and filing the critic's
// findings) back onto the same queue. The order is Tick, then drain every ready
// task, repeat — Tick between drains is what promotes the dependent test-writer
// once the survivors are known, so the two must interleave, never run one to
// exhaustion before the other.
//
// A Tick error is tolerated up to localTickMaxErrors CONSECUTIVE times — the
// same bound and "reissued for retry" tolerance internal/brain/advpool.go's
// tick loop applies — because the driver has already reopened the offending
// task, so the next drain re-claims and re-runs it. The counter resets to zero
// on any Tick that makes progress (returns without error); only after
// localTickMaxErrors CONSECUTIVE errors does the loop give up and return the
// error (an infra failure, not a recoverable artifact).
//
// chatterFor maps a task's role to the model backend that runs it (injected so
// tests can supply fakes). It returns the converged Verdict, or an error if
// ctx expired before convergence, a role's LLM call failed outright, or Tick
// errored localTickMaxErrors times in a row.
func driveLocalRun(ctx context.Context, d *advpool.Driver, q *queue.Store, missionID int64, chatterFor func(role string) agentworker.Chatter, poll time.Duration, sleep func(time.Duration), progress io.Writer, rec *recordSink, actorFor func(role string) string, swarm int) (*advpool.Verdict, error) {
	consecutiveTickErrors := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("timed out before the pool converged: %w", err)
		}
		verdict, err := d.Tick(ctx, missionID)
		if err != nil {
			consecutiveTickErrors++
			fmt.Fprintf(progress, "certify --local: tick error, reissued for retry (%d/%d): %v\n", consecutiveTickErrors, localTickMaxErrors, err)
			if consecutiveTickErrors >= localTickMaxErrors {
				return nil, fmt.Errorf("giving up after %d consecutive tick errors: %w", localTickMaxErrors, err)
			}
			sleep(poll)
			continue
		}
		consecutiveTickErrors = 0
		if verdict != nil {
			return verdict, nil
		}
		ran, err := runReadyTasks(ctx, q, missionID, chatterFor, rec, actorFor, swarm, progress)
		if err != nil {
			return nil, err
		}
		if !ran {
			// Nothing was claimable this round (e.g. the only ready task is a
			// dependent one the next Tick will promote) — wait, then re-tick.
			sleep(poll)
		}
	}
}

// runReadyTasks claims and runs every currently-ready task on the queue,
// returning whether it ran at least one. Each claimed task is routed to the
// in-process agentworker.RunRole for its role; a test-critic's findings are
// stamped with the run/task/reporter context and filed on the queue BEFORE the
// task is completed, so the driver's aggregate step sees them. A role LLM
// error (the Chatter call itself failing, e.g. a network/API error) aborts the
// run — that is not the recoverable case. The recoverable case (a malformed or
// non-compiling artifact) is handled one layer up: the driver reopens the
// task and returns an error from Tick, which driveLocalRun tolerates up to
// localTickMaxErrors times, so the reopened task IS re-claimed and re-run here
// on the next drain — a real retry, not just a reissue that nothing consumes.
// runReadyTasks drains every currently-claimable task through a BOUNDED pool of
// `swarm` concurrent workers, returning whether at least one ran. This is the
// execution substrate of the swarm: independent role tasks (e.g. the
// mutant-generator and the test-critic, both ready at the start of a run) run in
// parallel instead of one-at-a-time. It stays inside the Tick→drain→Tick
// structure — a drain never unlocks new tasks (only Tick promotes dependents),
// so a worker that sees no claimable task is genuinely done for this drain.
//
// Bounded by design (the cost answer): at most `swarm` workers run at once, and
// `swarm` itself is clamped to the host + the operator budget upstream. The
// first hard error cancels the rest and is returned; the queue's atomic claim
// guarantees no task is run twice even under concurrent workers.
func runReadyTasks(ctx context.Context, q *queue.Store, missionID int64, chatterFor func(role string) agentworker.Chatter, rec *recordSink, actorFor func(role string) string, swarm int, progress io.Writer) (bool, error) {
	if swarm < 1 {
		swarm = 1
	}
	// Workers write progress concurrently; serialize so two notices can't
	// interleave mid-line.
	out := &syncWriter{w: progress}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		ranAny   bool
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel() // stop the other workers promptly
	}

	for i := 0; i < swarm; i++ {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				task, err := q.ClaimNext(workerID, nil, localLeaseSeconds)
				if err != nil {
					fail(fmt.Errorf("claiming next task: %w", err))
					return
				}
				if task == nil {
					return // nothing claimable → this worker is done for the drain
				}
				mu.Lock()
				ranAny = true
				mu.Unlock()
				if err := runOneTask(ctx, q, missionID, workerID, task, chatterFor, rec, actorFor, out); err != nil {
					fail(err)
					return
				}
			}
		}(fmt.Sprintf("%s-%d", localBee, i))
	}
	wg.Wait()
	return ranAny, firstErr
}

// runOneTask claims-through a single task: routes it to its role's in-process
// agentworker, files any findings, records the tape lifecycle, and completes it
// on the queue under workerID. Pure per-task work — safe to run concurrently
// with other workers (distinct tasks, atomic queue ops, mutex-guarded tape).
func runOneTask(ctx context.Context, q *queue.Store, missionID int64, workerID string, task *queue.Task, chatterFor func(role string) agentworker.Chatter, rec *recordSink, actorFor func(role string) string, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	ch := chatterFor(task.Role)
	if ch == nil {
		return fmt.Errorf("no model backend for role %q", task.Role)
	}
	// Tape (no-op when rec is nil): the task appears, is claimed by its seat,
	// files its findings, and completes — the lifecycle the cockpit renders.
	actor := ""
	if actorFor != nil {
		actor = actorFor(task.Role)
	}
	rec.add("task_created", "", task.Key, map[string]any{"role": task.Role, "title": task.Title})
	rec.add("task_claimed", actor, task.Key, map[string]any{"role": task.Role, "title": task.Title})
	result, findings, rerr := agentworker.RunRole(ctx, ch, task.Role, task.Instruction)
	if rerr != nil {
		if task.Role == advpool.RoleMutantGeneratorShadow {
			// A challenger seat is MEASUREMENT, never the gate — so its LLM
			// call failing must not take the certification down with it. This
			// path is otherwise role-blind: runReadyTasks turns any error here
			// into fail(), which cancels every in-flight PRIMARY worker, and
			// driveLocalRun returns it directly (outside the
			// consecutiveTickErrors tolerance, which only wraps d.Tick) — so
			// the run exits 1 with NO verdict. With shadow on by default, a
			// challenger-model 429, a network blip, or a typo'd
			// --shadow-model would kill an audit the primary seats were about
			// to pass.
			//
			// Completing with the ShadowProviderFailedResult sentinel (rather
			// than leaving the task pending) is what lets the run proceed: the
			// primary all-shards-terminal gate never waits on a shadow task at
			// all, and the driver recognizes the sentinel and leaves the seat
			// UNMEASURED rather than attempting to parse it.
			//
			// The sentinel matters, not just "some non-empty completion": an
			// EMPTY result is indistinguishable, downstream, from a real
			// challenger reply that failed to parse — ParseMutants("") always
			// errors, which used to fall into the driver's ordinary
			// parse-failure branch and record a MEASURED, DROPPED, zero-yield
			// row for a model that was never actually asked the question. That
			// is fabricated data landing in the shared scorecard that feeds
			// model routing (see advpool.ShadowProviderFailedResult) — worse
			// than recording nothing.
			fmt.Fprintf(progress, "certify --local: challenger seat %s failed (%v) — measurement skipped, the audit continues\n", task.Key, rerr)
			result, findings = advpool.ShadowProviderFailedResult, nil
		} else {
			return fmt.Errorf("running role %q: %w", task.Role, rerr)
		}
	}
	for _, f := range findings {
		f.MissionID = missionID
		f.TaskID = task.ID
		f.Reporter = task.Role
		normalizeFinding(&f)
		if _, err := q.AddFinding(f); err != nil {
			return fmt.Errorf("recording %q finding: %w", task.Role, err)
		}
		rec.add("finding_reported", actor, f.Target, map[string]any{
			"severity": f.Severity, "type": f.Type, "evidence": f.Evidence, "role": task.Role,
		})
	}
	rec.add("task_done", actor, task.Key, map[string]any{"role": task.Role, "result": result})
	if _, err := q.Complete(task.ID, workerID, result); err != nil {
		return fmt.Errorf("completing role %q: %w", task.Role, err)
	}
	return nil
}

// syncWriter serializes concurrent writes from the bounded worker pool onto
// one progress stream, so two workers' notices cannot interleave mid-line. A
// nil target is treated as io.Discard by the caller.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return len(p), nil
	}
	return s.w.Write(p)
}

func normalizeFinding(f *queue.Finding) {
	if !validFindingType[f.Type] {
		f.Type = "note"
	}
	if !validFindingSeverity[f.Severity] {
		f.Severity = "low"
	}
}
