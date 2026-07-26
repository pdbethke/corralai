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
