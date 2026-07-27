package reposcan

import (
	"context"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
)

// Ungradable reasons.
const (
	ReasonBaselineFailed = "baseline-failed"
	ReasonFlakyBaseline  = "flaky-baseline"
	ReasonExecutorError  = "executor-error"
	ReasonCancelled      = "cancelled"
	// ReasonPrepFailed marks a job whose language-wide jail preparation failed
	// (e.g. `go mod vendor`). The failure is cached per language, so the WORK is
	// attempted once per language rather than once per file — but every file of
	// that language is still individually reported ungradable with this reason
	// and tallied by Aggregate.
	ReasonPrepFailed = "prep-failed"
)

// FileResult is one file's audit outcome plus the provenance the report needs.
// ComputedAt and CacheHit exist so reuse is always visible: a reader can see
// that a verdict was computed earlier and reused because its inputs were
// unchanged, rather than discovering later that a scan silently reported
// stale work as current.
type FileResult struct {
	Job        Job
	Verdict    advpool.Verdict
	Gradable   bool
	Reason     string
	ComputedAt time.Time
	CacheHit   bool
}

// Executor runs one job to a verdict. The CLI supplies an adapter over the
// existing in-process adversarial pool; the hosted service will supply a
// queue-backed one. The core does not care which.
type Executor interface {
	Execute(ctx context.Context, j Job) (FileResult, error)
}

// Cache stores verdicts by (owner, cacheKey). Owner-scoping is what keeps a
// later multi-tenant deployment from leaking verdicts across tenants.
type Cache interface {
	Get(owner, cacheKey string) (FileResult, bool)
	Put(owner string, r FileResult)
}

// Scan runs jobs through ex with at most `workers` concurrent executions,
// consulting c first. It never returns an error: a job that cannot be run
// becomes an ungradable result with a reason, because the repo report has to
// account for every job. Results are returned in `jobs` order.
//
// Scan serializes its own access to c with an internal mutex: the Cache
// interface does not promise concurrency-safe implementations (a plain
// map-backed cache is the common case), and Put is called from concurrent
// worker goroutines, so Scan — not its caller — owns making that safe.
func Scan(ctx context.Context, jobs []Job, ex Executor, c Cache, workers int) []FileResult {
	if workers < 1 {
		workers = 1
	}
	out := make([]FileResult, len(jobs))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var cacheMu sync.Mutex

	for i, j := range jobs {
		if c != nil {
			cacheMu.Lock()
			hit, ok := c.Get(j.Owner, j.CacheKey)
			cacheMu.Unlock()
			if ok {
				hit.Job = j
				hit.CacheHit = true
				out[i] = hit
				continue
			}
		}

		wg.Add(1)
		go func(i int, j Job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				out[i] = FileResult{Job: j, Gradable: false, Reason: ReasonCancelled, ComputedAt: time.Now()}
				return
			}

			res, err := ex.Execute(ctx, j)
			if err != nil {
				out[i] = FileResult{Job: j, Gradable: false, Reason: ReasonExecutorError, ComputedAt: time.Now()}
				return
			}
			res.Job = j
			// res.CacheHit is deliberately NOT reset here. Scan's own cache
			// miss says nothing about the executor's: a queue-backed executor
			// with a cache of its own is the one component that knows the
			// verdict was reused, and overwriting its answer would make the
			// scan report reused work as fresh — the exact opposite of the
			// "reuse is always disclosed" invariant above.
			if res.ComputedAt.IsZero() {
				res.ComputedAt = time.Now()
			}
			// Preserve the existing could-not-grade signal rather than
			// letting a zero kill rate masquerade as a real score.
			if res.Verdict.BaselineFailed {
				res.Gradable = false
				if res.Reason == "" {
					res.Reason = ReasonBaselineFailed
				}
			}
			out[i] = res
			if c != nil && res.Gradable {
				cacheMu.Lock()
				c.Put(j.Owner, res)
				cacheMu.Unlock()
			}
		}(i, j)
	}
	wg.Wait()
	return out
}
