package reposcan

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
)

type fakeExec struct {
	calls    atomic.Int32
	failOn   string
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *fakeExec) Execute(ctx context.Context, j Job) (FileResult, error) {
	n := f.inFlight.Add(1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	defer f.inFlight.Add(-1)
	f.calls.Add(1)
	time.Sleep(5 * time.Millisecond)
	if j.Path == f.failOn {
		return FileResult{}, errors.New("boom")
	}
	return FileResult{
		Job:        j,
		Verdict:    advpool.Verdict{DevKillRate: 0.5, MutantsTotal: 10},
		Gradable:   true,
		ComputedAt: time.Now(),
	}, nil
}

type mapCache map[string]FileResult

func (m mapCache) Get(owner, key string) (FileResult, bool) { r, ok := m[owner+"/"+key]; return r, ok }
func (m mapCache) Put(owner string, r FileResult)           { m[owner+"/"+r.Job.CacheKey] = r }

func jobsN(n int) []Job {
	var js []Job
	for i := 0; i < n; i++ {
		js = append(js, Job{Owner: "o", Path: string(rune('a'+i)) + ".go", CacheKey: string(rune('a' + i))})
	}
	return js
}

func TestScanRunsEveryJobAndPreservesOrder(t *testing.T) {
	js := jobsN(5)
	ex := &fakeExec{}
	out := Scan(context.Background(), js, ex, mapCache{}, 3)

	if len(out) != 5 {
		t.Fatalf("want 5 results, got %d", len(out))
	}
	for i := range js {
		if out[i].Job.Path != js[i].Path {
			t.Fatalf("result %d out of order: %s != %s", i, out[i].Job.Path, js[i].Path)
		}
	}
	if int(ex.calls.Load()) != 5 {
		t.Fatalf("executor called %d times, want 5", ex.calls.Load())
	}
}

func TestScanRespectsWorkerBound(t *testing.T) {
	ex := &fakeExec{}
	Scan(context.Background(), jobsN(12), ex, mapCache{}, 3)
	if got := ex.maxSeen.Load(); got > 3 {
		t.Fatalf("concurrency %d exceeded the bound of 3", got)
	}
}

// An executor failure is accounted for, never dropped and never scored.
func TestScanExecutorFailureIsUngradable(t *testing.T) {
	js := jobsN(2)
	ex := &fakeExec{failOn: js[1].Path}
	out := Scan(context.Background(), js, ex, mapCache{}, 2)

	if len(out) != 2 {
		t.Fatalf("failed job was dropped: got %d results", len(out))
	}
	if out[1].Gradable {
		t.Error("failed job reported as gradable")
	}
	if out[1].Reason != ReasonExecutorError {
		t.Errorf("Reason = %q, want %q", out[1].Reason, ReasonExecutorError)
	}
}

// A cache hit skips execution and is disclosed as a hit.
func TestScanUsesCacheAndDisclosesHits(t *testing.T) {
	js := jobsN(2)
	cache := mapCache{}
	earlier := time.Now().Add(-72 * time.Hour)
	cache.Put("o", FileResult{Job: js[0], Gradable: true, ComputedAt: earlier})

	ex := &fakeExec{}
	out := Scan(context.Background(), js, ex, cache, 2)

	if int(ex.calls.Load()) != 1 {
		t.Fatalf("executor called %d times, want 1 (one job was cached)", ex.calls.Load())
	}
	if !out[0].CacheHit {
		t.Error("cache hit not disclosed")
	}
	if !out[0].ComputedAt.Equal(earlier) {
		t.Error("cache hit lost its original ComputedAt — reuse must stay visible")
	}
	if out[1].CacheHit {
		t.Error("freshly computed result marked as a cache hit")
	}
}

// TestScanCancelledContextIsAccountedNotDropped covers the one path where a
// scan returns results the operator did not ask to be cut short: every job
// still gets a result, and it is ungradable with the "cancelled" reason —
// never a zero kill rate that would read as "terrible tests".
func TestScanCancelledContextIsAccountedNotDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	js := jobsN(3)
	ex := &fakeExec{}
	out := Scan(ctx, js, ex, mapCache{}, 2)

	if len(out) != 3 {
		t.Fatalf("cancelled jobs were dropped: got %d results, want 3", len(out))
	}
	if n := ex.calls.Load(); n != 0 {
		t.Errorf("executor ran %d job(s) under a cancelled context, want 0", n)
	}
	for i, r := range out {
		if r.Gradable {
			t.Errorf("result %d reported as gradable after cancellation", i)
		}
		if r.Reason != ReasonCancelled {
			t.Errorf("result %d Reason = %q, want %q", i, r.Reason, ReasonCancelled)
		}
		if r.Job.Path != js[i].Path {
			t.Errorf("result %d lost its job identity: %+v", i, r.Job)
		}
		if r.ComputedAt.IsZero() {
			t.Errorf("result %d has no ComputedAt", i)
		}
	}

	rep := Aggregate("o", "r", "c", 3, len(js), out, nil)
	if rep.Ungradable[ReasonCancelled] != 3 {
		t.Errorf("cancelled jobs must be accounted by reason: %+v", rep.Ungradable)
	}
}

// TestScanDoesNotClobberExecutorCacheDisclosure: a queue-backed executor with
// a cache of its own is the only component that knows a verdict was reused.
// Scan's own cache miss must not overwrite that disclosure — "reuse is always
// disclosed" is an invariant of the report, not of Scan's cache.
func TestScanDoesNotClobberExecutorCacheDisclosure(t *testing.T) {
	out := Scan(context.Background(), jobsN(1), reusingExec{}, nil, 1)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	if !out[0].CacheHit {
		t.Error("the executor disclosed a reused verdict and Scan erased it")
	}
}

// reusingExec stands in for an executor that served the verdict from its own
// cache.
type reusingExec struct{}

func (reusingExec) Execute(ctx context.Context, j Job) (FileResult, error) {
	return FileResult{
		Verdict:    advpool.Verdict{DevKillRate: 0.5, MutantsTotal: 10},
		Gradable:   true,
		CacheHit:   true,
		ComputedAt: time.Now().Add(-24 * time.Hour),
	}, nil
}
