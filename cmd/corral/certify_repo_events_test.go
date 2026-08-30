// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/queue"
)

// eventsFakeClock is a controllable clock, local to this test file (the
// advpool package's own fakeClock is unexported to that package's tests).
// advance moves it forward by a known amount so a phase's measured duration
// is an exact, assertable number rather than a flaky "greater than zero".
type eventsFakeClock struct{ t time.Time }

func (c *eventsFakeClock) Now() time.Time          { return c.t }
func (c *eventsFakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// eventsFakeScorer is the minimal advpool.Scorer double this file's driver
// harness needs: a scripted dev pass (one survivor out of two mutants), a
// scripted pool pass (the survivor caught), and a clock tick on every score
// so Generation/DevPass/AuthoredPass/Critic all measure something real.
type eventsFakeScorer struct {
	clk       *eventsFakeClock
	dev, pool time.Duration
	devDone   bool
}

func (f *eventsFakeScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	if !f.devDone {
		f.clk.advance(f.dev)
		f.devDone = true
		return 0.5, []adequacy.Mutant{{ID: "m2", Replace: "s"}}, nil
	}
	f.clk.advance(f.pool)
	return 1.0, nil, nil
}

func (f *eventsFakeScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	kr, survivors, err := f.Score(ctx, codePath, code, test, mutants, testCmd)
	if err != nil {
		return adequacy.Report{}, err
	}
	rep := adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: 2}
	if kr >= 1.0 {
		rep.Killed = []string{"m1", "m2"}
	} else {
		rep.Killed = []string{"m1"}
	}
	for _, s := range survivors {
		rep.Survived = append(rep.Survived, s.ID)
	}
	return rep, nil
}

type eventsFakeValidator struct{ mutants []adequacy.Mutant }

func (f *eventsFakeValidator) ParseMutants(raw, original string) ([]adequacy.Mutant, error) {
	return f.mutants, nil
}
func (f *eventsFakeValidator) ParseTest(raw string) string { return raw }
func (f *eventsFakeValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	return nil
}

type eventsFakeSigner struct{}

func (eventsFakeSigner) SignVerdict(ctx context.Context, v advpool.Verdict) (int64, string, error) {
	return 1, "deadhead", nil
}

// eventsRunCode / eventsRunDevTestCode are the audited file's bytes and its
// dev suite's bytes for every run this file drives. They carry a SENTINEL
// each so a custody test can assert, by substring, that the audited source
// reached neither the local ledger's scan_events nor the warehouse's
// corral_events — see TestEventsNeverCarrySourceBytes.
const (
	eventsRunCode        = "package target\n// SENTINEL-AUDITED-SOURCE-8f21\nfunc F() {}"
	eventsRunDevTestCode = "package target\n// SENTINEL-DEV-TEST-SOURCE-3c40\nfunc TestF(t *testing.T) {}"
)

// driveEventsRun builds a Driver over eventsFakeScorer/eventsFakeValidator,
// attaches sink.forFile(path) as its EventSink, and drives one file's run
// from StartRun to a signed verdict — mirroring completeFullRun/
// TestVerdictTimingSumsWithinTotal in internal/advpool, reimplemented here
// with only exported pieces since those helpers are unexported to this
// (different) package.
func driveEventsRun(t *testing.T, sink *scanEventSink, path string, clk *eventsFakeClock) *advpool.Verdict {
	t.Helper()
	const mission int64 = 501
	scorer := &eventsFakeScorer{clk: clk, dev: 2 * time.Minute, pool: time.Minute}
	validator := &eventsFakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Replace: "k"}, {ID: "m2", Replace: "s"}}}
	q, err := queue.Open(filepath.Join(t.TempDir(), "q.sqlite3"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "model-gen",
		advpool.RoleTestWriter:      "model-writer",
		advpool.RoleTestCritic:      "model-critic",
	}
	d, err := advpool.NewDriver(q, scorer, validator, assign, 0.4)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.Now = clk.Now
	d.Signer = eventsFakeSigner{}
	d.Events = sink.forFile(path)

	rs := advpool.RunSpec{
		Repo: "example/repo", Commit: "deadbeef", Goal: "passwords >= 12 chars",
		CodePath: path, Code: eventsRunCode,
		DevTestPath: "target_test.go", DevTestCode: eventsRunDevTestCode,
		TestCmd: "go test ./...", NMutants: 3, Lang: "go",
		// PoolDuration exercises phase_pool, which the driver reports at
		// StartRun (see advpool.Driver.StartRun) rather than at a Tick-time
		// phase boundary — the workspace copy+probe already happened before
		// the driver was ever constructed.
		PoolDuration: 12 * time.Second,
	}
	if err := d.StartRun(mission, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}

	ready := map[string]*queue.Task{}
	for {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			break
		}
		ready[task.Key] = task
	}
	tc, mg := ready[advpool.RoleTestCritic], ready[advpool.RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got %v", ready)
	}
	clk.advance(4 * time.Minute) // GENERATION: the model seat is "out" for a while.
	mustCompleteEvent(t, q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), mission); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	testWriterID := findTestWriterTaskID(t, q, mission)
	clk.advance(30 * time.Second)
	tw := claimByID(t, q, testWriterID)
	mustCompleteEvent(t, q, tw.ID, "pool test source")
	if _, err := d.Tick(context.Background(), mission); err != nil {
		t.Fatalf("Tick (pool-adequacy): %v", err)
	}

	clk.advance(15 * time.Second)
	mustCompleteEvent(t, q, tc.ID, "no vacuous tests found")
	v, err := d.Tick(context.Background(), mission)
	if err != nil {
		t.Fatalf("Tick (aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a converged verdict")
	}
	return v
}

func mustCompleteEvent(t *testing.T, q *queue.Store, id int64, result string) {
	t.Helper()
	ok, err := q.Complete(id, "bee", result)
	if err != nil || !ok {
		t.Fatalf("complete %d: ok=%v err=%v", id, ok, err)
	}
}

func claimByID(t *testing.T, q *queue.Store, id int64) *queue.Task {
	t.Helper()
	for i := 0; i < 10; i++ {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			t.Fatalf("no claimable task found for id %d", id)
		}
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("could not claim task %d within attempts", id)
	return nil
}

// findTestWriterTaskID finds the LIVE test-writer task's id via the queue's
// own task list, since RunState internals are not exported. BuildDAG seeds a
// whole retry chain (test-writer, test-writer-r2, ...) up front so a
// repair round never has to enqueue mid-run — every one but the currently
// live task starts (and here stays) "superseded", so the live one is
// whichever is NOT superseded.
func findTestWriterTaskID(t *testing.T, q *queue.Store, missionID int64) int64 {
	t.Helper()
	tasks, err := q.List(missionID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, tk := range tasks {
		if tk.Role == advpool.RoleTestWriter && tk.Status != queue.StatusSuperseded {
			return tk.ID
		}
	}
	t.Fatalf("no live test-writer task found in %v", tasks)
	return 0
}

// TestEveryDriverBeatReachesTheLedger drives a full run with a scanEventSink
// wired as the driver's EventSink and asserts every phase boundary the
// driver crosses lands as a scanstore.Event: Seq strictly increasing, at
// least one event per phase this run actually ran (phase_pool,
// phase_generation, pool_dev_adequacy [dev_pass's boundary],
// phase_authored_pass, phase_critic, plus pool_subject/pool_verdict), every
// event's Path is the file this run audited, and every Detail round-trips
// through json.Unmarshal.
func TestEveryDriverBeatReachesTheLedger(t *testing.T) {
	sink := newScanEventSink(nil)
	clk := &eventsFakeClock{t: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)}
	v := driveEventsRun(t, sink, "target.go", clk)
	if v.Status == "" {
		t.Fatal("expected a signed verdict")
	}

	events := sink.drain()
	if len(events) == 0 {
		t.Fatal("expected at least one event, got none")
	}

	var lastSeq int64
	for i, e := range events {
		if e.Seq <= lastSeq {
			t.Fatalf("event %d: Seq %d did not strictly increase from %d", i, e.Seq, lastSeq)
		}
		lastSeq = e.Seq
		if e.Path != "target.go" {
			t.Errorf("event %d (%s): Path = %q, want %q", i, e.Kind, e.Path, "target.go")
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &detail); err != nil {
			t.Errorf("event %d (%s): Detail %q does not round-trip: %v", i, e.Kind, e.Detail, err)
		}
	}

	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Kind] = true
	}
	for _, want := range []string{
		"pool_subject", "phase_pool", "phase_generation", "pool_dev_adequacy",
		"phase_authored_pass", "phase_critic", "pool_verdict",
	} {
		if !seen[want] {
			t.Errorf("missing event kind %q; got %v", want, seen)
		}
	}
}

// TestEventsAreBoundedInMemory: 200k emits must not grow the sink's memory
// past the disclosed 100,000-event cap — a runaway suite must not OOM the
// runner. The sink keeps the first 100,000 real events and records ONE
// final events_truncated event whose detail.dropped names exactly how many
// were dropped: disclosed, never silent.
func TestEventsAreBoundedInMemory(t *testing.T) {
	sink := newScanEventSink(nil)
	const total = 200_000
	for i := 0; i < total; i++ {
		sink.record("f.go", "beat", "", map[string]any{"i": i})
	}
	events := sink.drain()
	if len(events) != scanEventCap+1 {
		t.Fatalf("len(events) = %d, want %d (the cap plus one truncation marker)", len(events), scanEventCap+1)
	}
	last := events[len(events)-1]
	if last.Kind != "events_truncated" {
		t.Fatalf("last event kind = %q, want %q", last.Kind, "events_truncated")
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(last.Detail), &detail); err != nil {
		t.Fatalf("events_truncated Detail does not round-trip: %v", err)
	}
	wantDropped := float64(total - scanEventCap)
	if got, _ := detail["dropped"].(float64); got != wantDropped {
		t.Errorf("detail.dropped = %v, want %v", detail["dropped"], wantDropped)
	}
	// Only ONE truncation marker, ever — a second cap-crossing beat must
	// update the existing marker's count, not add a second row.
	truncatedCount := 0
	for _, e := range events {
		if e.Kind == "events_truncated" {
			truncatedCount++
		}
	}
	if truncatedCount != 1 {
		t.Fatalf("found %d events_truncated events, want exactly 1", truncatedCount)
	}
}

// TestScanEventSinkDurationMillisNeverZero proves the NULL-not-zero rule at
// the sink boundary: a detail.duration_ms of 0 (or absent) must leave
// DurationMillis nil, never a stored 0 that would be read as "ran for free".
func TestScanEventSinkDurationMillisNeverZero(t *testing.T) {
	sink := newScanEventSink(nil)
	sink.record("f.go", "moment", "", nil)
	sink.record("f.go", "zero-duration", "", map[string]any{"duration_ms": int64(0)})
	sink.record("f.go", "real-duration", "", map[string]any{"duration_ms": int64(1500)})
	events := sink.drain()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	if events[0].DurationMillis != nil {
		t.Errorf("a moment with nil detail got DurationMillis = %v, want nil", *events[0].DurationMillis)
	}
	if events[0].Detail != "{}" {
		t.Errorf("a nil-detail event's Detail = %q, want %q (an event with no detail still exists)", events[0].Detail, "{}")
	}
	if events[1].DurationMillis != nil {
		t.Errorf("duration_ms:0 got DurationMillis = %v, want nil", *events[1].DurationMillis)
	}
	if events[2].DurationMillis == nil || *events[2].DurationMillis != 1500 {
		t.Errorf("real-duration event DurationMillis = %v, want 1500", events[2].DurationMillis)
	}
}

// TestScanEventSinkForScanIsPathEmpty proves the scan-scoped/file-scoped
// split at the sink boundary directly: forScan records Path "", forFile
// records the given path.
func TestScanEventSinkForScanIsPathEmpty(t *testing.T) {
	sink := newScanEventSink(nil)
	sink.forScan("phase_selection", "", map[string]any{"duration_ms": int64(92_000)})
	sink.forFile("a.go").Emit(0, "pool_subject", "", nil)
	events := sink.drain()
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Path != "" {
		t.Errorf("scan-scoped event Path = %q, want \"\"", events[0].Path)
	}
	if events[1].Path != "a.go" {
		t.Errorf("file-scoped event Path = %q, want %q", events[1].Path, "a.go")
	}
}

// TestScanEventSinkIsThreadSafe drives concurrent record calls (mirroring
// per-file LLM workers on the workspace substrate's own swarm budget — see
// localExecutor.perFileSwarm) through `go test -race` and asserts every
// Seq from 1..N appears exactly once, with none dropped or duplicated by a
// race.
func TestScanEventSinkIsThreadSafe(t *testing.T) {
	sink := newScanEventSink(nil)
	const n = 500
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			sink.record(fmt.Sprintf("f%d.go", i), "beat", "", map[string]any{"i": i})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	events := sink.drain()
	if len(events) != n {
		t.Fatalf("len(events) = %d, want %d", len(events), n)
	}
	seen := map[int64]bool{}
	for _, e := range events {
		if seen[e.Seq] {
			t.Fatalf("Seq %d recorded more than once", e.Seq)
		}
		seen[e.Seq] = true
	}
	for i := int64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("Seq %d missing", i)
		}
	}
}
