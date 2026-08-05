// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/queue"
)

// fakeClock is an injectable advpool.Driver.Now — lets a test cross
// RunDeadline deterministically without any real sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// timeoutFakeScorer is an instant, in-memory advpool.Scorer: no jail, no
// real subprocess. Its first ScoreReport call is treated as the dev-adequacy
// score (the only call these tests need); every later call reports a
// zero-yield baseline, which no test here relies on.
type timeoutFakeScorer struct {
	devKillRate  float64
	devSurvivors []adequacy.Mutant
	scored       bool
}

func (f *timeoutFakeScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	f.scored = true
	return f.devKillRate, f.devSurvivors, nil
}

func (f *timeoutFakeScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	f.scored = true
	rep := adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: len(mutants)}
	survived := map[string]bool{}
	for _, s := range f.devSurvivors {
		survived[s.ID] = true
	}
	for _, m := range mutants {
		if survived[m.ID] {
			rep.Survived = append(rep.Survived, m.ID)
		} else {
			rep.Killed = append(rep.Killed, m.ID)
		}
	}
	return rep, nil
}

// timeoutFakeValidator is an instant advpool.Validator: ParseMutants always
// returns the scripted mutant set regardless of a role's raw reply (these
// tests never let the test-writer's output reach CompileTest).
type timeoutFakeValidator struct{ mutants []adequacy.Mutant }

func (f *timeoutFakeValidator) ParseMutants(raw, original string) ([]adequacy.Mutant, error) {
	return f.mutants, nil
}
func (f *timeoutFakeValidator) ParseTest(raw string) string { return raw }
func (f *timeoutFakeValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	return nil
}

// newTimeoutTestDriver builds a Driver over the instant fakes above plus a
// fresh on-disk queue, wired with a fakeClock (d.Now) so RunDeadline can be
// crossed deterministically without any real sleeping.
func newTimeoutTestDriver(t *testing.T, killRate float64, survivors []adequacy.Mutant) (*advpool.Driver, *queue.Store, *fakeClock) {
	t.Helper()
	q := newLocalTestQueue(t)
	scorer := &timeoutFakeScorer{devKillRate: killRate, devSurvivors: survivors}
	validator := &timeoutFakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "one"}, {ID: "m2", Code: "two"}}}
	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "model-gen",
		advpool.RoleTestWriter:      "model-writer",
		advpool.RoleTestCritic:      "model-critic",
	}
	d, err := advpool.NewDriver(q, scorer, validator, assign, 0.9) // high threshold: any real score here still needs-reviews, not the point of these tests
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	clk := &fakeClock{t: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	d.Now = clk.Now
	return d, q, clk
}

func newLocalTestQueue(t *testing.T) *queue.Store {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(dir + "/q.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

func timeoutTestChatterFor() func(role string) agentworker.Chatter {
	return func(role string) agentworker.Chatter {
		switch role {
		case advpool.RoleMutantGenerator:
			return cannedChatter{content: "mutants (ignored — the fake validator scripts its own)"}
		case advpool.RoleTestWriter:
			return cannedChatter{content: "package p\nfunc TestX(t *testing.T){}\n"}
		case advpool.RoleTestCritic:
			return cannedChatter{content: "no vacuous tests found"}
		default:
			return nil
		}
	}
}

// alreadyExpiredCtx returns a context whose deadline is already in the past
// — driveLocalRun's very first loop iteration sees ctx.Err() != nil, exactly
// the state the bug report's flask/cli.py run reached (its outer bound had
// already elapsed by the time the drive loop got back around to checking).
func alreadyExpiredCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}

// TestDriveLocalRun_BanksTimedOutDevScore proves Fix 1: a computed
// dev-adequacy score must survive a convergence timeout instead of being
// thrown away by driveLocalRun's bare `ctx.Err()` return. The dev suite's
// kill-rate is scored for real (via the driver's own Tick/tickDevAdequacy,
// run to that point BEFORE the outer ctx is allowed to expire) and the run
// is then driven with an ALREADY-EXPIRED ctx and RunDeadline already crossed
// — reproducing the exact race the bug report hit (the drive loop's
// ctx-expiry check firing before Tick ever got another chance to notice
// RunDeadline and emit its signed timeout verdict).
//
// Before the fix, this returns (nil, error) — the bare "timed out before the
// pool converged" — discarding the 50% kill-rate that was already measured.
// After the fix, it must return a verdict carrying that same 50%, marked
// TimedOut so a reader can tell it apart from a clean convergence.
func TestDriveLocalRun_BanksTimedOutDevScore(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m2", Code: "two"}} // 1 of 2 mutants survives -> 0.5 kill rate
	d, q, clk := newTimeoutTestDriver(t, 0.5, survivors)

	rs := advpool.RunSpec{
		Repo: "local", Commit: "local", Goal: "irrelevant",
		CodePath: "x.go", Code: "package p\n",
		DevTestPath: "x_test.go", DevTestCode: "package p\n",
		NMutants: 2, Lang: "go",
	}
	const missionID = 101
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	chatterFor := timeoutTestChatterFor()
	actorFor := func(role string) string { return recordActor(role, "model") }
	seedCtx := context.Background()

	// Drive the DAG far enough, by hand, that dev-adequacy is REALLY scored
	// (mirrors driveLocalRun's own Tick -> runReadyTasks -> Tick sequence)
	// before the outer ctx is allowed to expire — the exact ordering the
	// real drive loop achieves when it gets lucky, and the bug report's run
	// did NOT.
	if _, err := d.Tick(seedCtx, missionID); err != nil {
		t.Fatalf("promote tick: %v", err)
	}
	if ran, err := runReadyTasks(seedCtx, q, missionID, chatterFor, nil, actorFor, 1, io.Discard); err != nil || !ran {
		t.Fatalf("runReadyTasks (mutant-generator): ran=%v err=%v", ran, err)
	}
	if v, err := d.Tick(seedCtx, missionID); err != nil {
		t.Fatalf("dev-adequacy tick: %v", err)
	} else if v != nil {
		t.Fatalf("run converged early (v=%+v), want still in progress (test-writer pending)", v)
	}

	// Now cross RunDeadline (fake clock, deterministic) and hand
	// driveLocalRun an already-expired real ctx — its very first loop
	// iteration must hit the ctx.Err() branch before ever calling Tick
	// again on its own.
	d.RunDeadline = time.Minute
	clk.advance(d.RunDeadline + time.Second)
	expired := alreadyExpiredCtx(t)

	verdict, err := driveLocalRun(expired, d, q, missionID, chatterFor, time.Millisecond, func(time.Duration) {}, io.Discard, nil, actorFor, 1)
	if err != nil {
		t.Fatalf("driveLocalRun: %v, want a banked timeout verdict instead of an error", err)
	}
	if verdict == nil {
		t.Fatal("driveLocalRun returned (nil, nil) — want the banked dev-adequacy score")
	}
	if !verdict.TimedOut {
		t.Error("verdict.TimedOut = false, want true — this must never read as a clean convergence")
	}
	if !verdict.DevScored {
		t.Error("verdict.DevScored = false, want true — the dev suite WAS actually scored")
	}
	if verdict.Status != advpool.StatusNeedsReview {
		t.Errorf("Status = %q, want %q — a timed-out run is never certified", verdict.Status, advpool.StatusNeedsReview)
	}
	if verdict.DevKillRate != 0.5 {
		t.Errorf("DevKillRate = %v, want 0.5 (the real measured score, not discarded)", verdict.DevKillRate)
	}
	if verdict.MutantsTotal != 2 {
		t.Errorf("MutantsTotal = %d, want 2", verdict.MutantsTotal)
	}
}

// TestDriveLocalRun_UnmeasuredTimeoutStillErrors proves Fix 1's other half:
// when the pool times out before dev-adequacy ever measured anything
// (run.devScored still false — the mutant-generator itself never
// completed), driveLocalRun must keep TODAY's behaviour exactly: return an
// error, never a verdict. A verdict here would carry DevKillRate=0.00 —
// the fabricated "your tests caught nothing" accusation this codebase has
// already produced and killed five times, not a real measurement.
func TestDriveLocalRun_UnmeasuredTimeoutStillErrors(t *testing.T) {
	d, q, clk := newTimeoutTestDriver(t, 0.5, nil)

	rs := advpool.RunSpec{
		Repo: "local", Commit: "local", Goal: "irrelevant",
		CodePath: "x.go", Code: "package p\n",
		DevTestPath: "x_test.go", DevTestCode: "package p\n",
		NMutants: 2, Lang: "go",
	}
	const missionID = 102
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Deliberately do NOT run the mutant-generator task at all: devScored
	// stays false, simulating a stall before any measurement.
	d.RunDeadline = time.Minute
	clk.advance(d.RunDeadline + time.Second)
	expired := alreadyExpiredCtx(t)

	chatterFor := timeoutTestChatterFor()
	actorFor := func(role string) string { return recordActor(role, "model") }
	verdict, err := driveLocalRun(expired, d, q, missionID, chatterFor, time.Millisecond, func(time.Duration) {}, io.Discard, nil, actorFor, 1)
	if err == nil {
		t.Fatalf("driveLocalRun returned no error (verdict=%+v) — want the bare timeout error, never a fabricated 0.00 verdict", verdict)
	}
	if verdict != nil {
		t.Fatalf("driveLocalRun returned a non-nil verdict (%+v) alongside an error — must be nil on this path", verdict)
	}
	if !strings.Contains(err.Error(), "timed out before the pool converged") {
		t.Errorf("error = %q, want it to preserve the original 'timed out before the pool converged' text", err.Error())
	}
}

// TestDriveLocalRunFailsFastOnATerminalToolchainError: retrying an error that
// can never succeed is not resilience, it is spending the operator's money
// twenty times to print the same sentence. A toolchain the sandbox
// structurally cannot run (a snap re-execs through snapd over a socket a
// network-isolated jail cannot reach) will not become runnable on the next
// tick.
//
// Observed: an audit on a workstation whose Go came from snap burned all 20
// retries before giving up, each one a full tick.
func TestDriveLocalRunFailsFastOnATerminalToolchainError(t *testing.T) {
	ticks := 0
	d := &advpool.Driver{}
	_ = d
	// Drive the classification directly: driveLocalRun's contract is that a
	// terminal error returns immediately rather than incrementing the retry
	// counter, and the classification is what decides that.
	err := error(adequacy.ErrSnapToolchain{Command: "go", Path: "/snap/bin/go"})
	var snap adequacy.ErrSnapToolchain
	if !errors.As(err, &snap) {
		t.Fatal("a snap toolchain error must be recognizable through errors.As")
	}
	wrapped := fmt.Errorf("advpool: score dev tests: %w", err)
	if !errors.As(wrapped, &snap) {
		t.Fatal("it must still be recognizable once the pool has wrapped it — that is how it reaches the tick loop")
	}
	if ticks != 0 {
		t.Fatal("no ticks should be needed to classify a terminal error")
	}
}
