// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// ledgerCache is reposcan.Cache backed by the DuckDB scan ledger: a file whose
// every verdict-affecting input is unchanged is not re-audited.
//
// It FAILS CLOSED without exception. Every error path — no DSN, a ledger that
// will not open, empty owner, query failure, verdict JSON that will not
// unmarshal — returns a MISS. A miss costs money. A wrong hit signs a claim
// about content that was never measured, into a tamper-evident record where
// it is undetectable afterwards. That asymmetry decides every judgement call
// in this file.
//
// IT HOLDS A DSN, NOT A HANDLE, and that is the point. DuckDB is
// single-writer per file, so the handle this cache used to be given was
// opened before the scan and held until the final write — for the whole
// hours-long run. Any concurrent `corral scans` against the same (default)
// ledger failed outright with "Conflicting lock is held": the record of the
// audits already paid for went dark for the duration of the next one. Open
// per operation, close immediately; between operations the file is free.
//
// store is non-nil ONLY inside a lookup. Tests assert that (see
// TestLedgerCacheHoldsNoLockBetweenLookups) — a handle left behind is the
// lock, whatever the intent was.
type ledgerCache struct {
	dsn   string
	store *scanstore.Store
}

func newLedgerCache(dsn string) *ledgerCache { return &ledgerCache{dsn: strings.TrimSpace(dsn)} }

// withStore opens the ledger for ONE operation and closes it before
// returning. A DSN that will not open is a miss, never an error: the cache
// must never be able to fail a scan.
func (c *ledgerCache) withStore(fn func(*scanstore.Store) bool) bool {
	if c == nil || c.dsn == "" {
		return false
	}
	st, err := scanstore.Open(c.dsn)
	if err != nil {
		return false
	}
	c.store = st
	defer func() {
		c.store = nil
		_ = st.Close()
	}()
	return fn(st)
}

// marshalVerdict is the one spelling of how a verdict is serialized for the
// ledger, shared by the writer and the tests so the two can never disagree
// about the format a later Get has to parse.
func marshalVerdict(v advpool.Verdict) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Get returns a previously earned verdict for this exact content address.
func (c *ledgerCache) Get(owner, cacheKey string) (reposcan.FileResult, bool) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(cacheKey) == "" {
		return reposcan.FileResult{}, false
	}
	var out reposcan.FileResult
	hit := c.withStore(func(st *scanstore.Store) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		js, computedAt, ok, err := st.VerdictByCacheKey(ctx, owner, cacheKey)
		if err != nil || !ok {
			return false
		}
		var v advpool.Verdict
		if err := json.Unmarshal([]byte(js), &v); err != nil {
			// Corrupt or written by an incompatible engine generation.
			// Re-audit.
			return false
		}
		if v.TimedOut {
			// A banked partial from a run that hit its wall clock. That is an
			// artifact of LOAD, not of content: the inputs have not changed,
			// but the reason this verdict is incomplete might have. Serving
			// it would freeze the partial forever; a re-run may converge.
			return false
		}
		out = reposcan.FileResult{
			Verdict:    v,
			Gradable:   true, // only gradable results are ever recorded with a key
			ComputedAt: computedAt,
		}
		return true
	})
	return out, hit
}

// oldestReuse returns the earliest ComputedAt among results that were REUSED,
// and whether any were.
//
// A CacheHits count alone is not disclosure. Continuous re-evaluation's whole
// claim is freshness, so a report where most files were reused from weeks ago,
// presented as a current audit, is precisely the self-flattering record this
// tool exists to prevent. The OLDEST contributing verdict is the honest
// summary number: it is the strongest thing that can be said about how current
// the report is as a whole.
func oldestReuse(results []reposcan.FileResult) (time.Time, bool) {
	var oldest time.Time
	found := false
	for _, r := range results {
		if !r.CacheHit || r.ComputedAt.IsZero() {
			continue
		}
		if !found || r.ComputedAt.Before(oldest) {
			oldest, found = r.ComputedAt, true
		}
	}
	return oldest, found
}

// Put is deliberately a no-op.
//
// Verdicts reach the ledger through the single Record call at the end of a
// scan, which already writes one row per file with its cache key. Writing here
// as well would produce duplicate rows for the same audit and two sources of
// truth that can disagree. The interface requires the method; the ledger does
// not need it.
func (c *ledgerCache) Put(string, reposcan.FileResult) {}
