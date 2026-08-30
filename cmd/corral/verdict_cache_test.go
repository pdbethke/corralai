// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

func nil2ctx() context.Context { return context.Background() }

// cacheTestStore opens a ledger and returns it WITH its path: the cache is
// addressed by DSN, not by an open handle, because a handle held for the
// whole scan holds DuckDB's single-writer lock for the whole scan (see
// ledgerCache's own doc).
func cacheTestStore(t *testing.T) (*scanstore.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scans.duckdb")
	s, err := scanstore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// closedCacheTestStore is cacheTestStore for a test that wants the ledger
// written and then RELEASED — the state every ledgerCache lookup sees in
// production, now that the scan does not hold the handle.
func closedCacheTestStore(t *testing.T, files []scanstore.File) string {
	t.Helper()
	s, path := cacheTestStore(t)
	if _, err := s.Record(nil2ctx(), scanstore.Scan{Owner: "acme", Repo: "r"}, files); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func TestLedgerCacheHitCarriesVerdictAndComputedAt(t *testing.T) {
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	path := closedCacheTestStore(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{}`, ComputedAt: when,
	}})
	got, ok := newLedgerCache(path).Get("acme", "K")
	if !ok {
		t.Fatal("expected a hit")
	}
	if !got.Gradable {
		t.Fatal("a cached verdict must come back gradable")
	}
	if !got.ComputedAt.Equal(when) {
		t.Fatalf("ComputedAt = %v, want %v (age must survive reuse or it cannot be disclosed)", got.ComputedAt, when)
	}
}

// Fail closed: unparseable JSON is a MISS, never an error and never a partial
// verdict. Re-auditing costs money; serving a half-decoded verdict signs a
// claim nothing measured.
func TestLedgerCacheMissesOnUnparseableVerdict(t *testing.T) {
	path := closedCacheTestStore(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{not json`, ComputedAt: time.Now().UTC(),
	}})
	if _, ok := newLedgerCache(path).Get("acme", "K"); ok {
		t.Fatal("corrupt verdict JSON produced a HIT — the cache must fail closed")
	}
}

func TestLedgerCacheMissesOnEmptyOwnerAndNilStore(t *testing.T) {
	_, path := cacheTestStore(t)
	if _, ok := newLedgerCache(path).Get("", "K"); ok {
		t.Fatal("empty owner produced a hit")
	}
	if _, ok := newLedgerCache("").Get("acme", "K"); ok {
		t.Fatal("an unset DSN produced a hit — a cache must never be able to fail a scan")
	}
	if _, ok := newLedgerCache(filepath.Join(t.TempDir(), "nope.duckdb")).Get("acme", "K"); ok {
		t.Fatal("a DSN that cannot be opened produced a hit — the cache fails CLOSED")
	}
}

// A TimedOut verdict is a banked partial from a run that hit its wall clock:
// an artifact of load, not of content. Caching it would serve the partial
// forever, when a re-run may converge.
func TestLedgerCacheDoesNotServeATimedOutVerdict(t *testing.T) {
	js, err := marshalVerdict(advpool.Verdict{TimedOut: true})
	if err != nil {
		t.Fatalf("marshalVerdict: %v", err)
	}
	path := closedCacheTestStore(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: js, ComputedAt: time.Now().UTC(),
	}})
	if _, ok := newLedgerCache(path).Get("acme", "K"); ok {
		t.Fatal("a TimedOut verdict was served from cache")
	}
}

func TestLedgerCachePutDoesNotWrite(t *testing.T) {
	path := closedCacheTestStore(t, nil)
	c := newLedgerCache(path)
	c.Put("acme", reposcan.FileResult{Gradable: true, Job: reposcan.Job{CacheKey: "K"}})
	if _, ok := c.Get("acme", "K"); ok {
		t.Fatal("Put wrote a row — verdicts must reach the ledger only through Record, or the ledger gets duplicate rows")
	}
}

// TestLedgerCacheHoldsNoLockBetweenLookups is the concurrency rule the scan
// used to break: `certify --repo --record` opened the ledger BEFORE the scan
// and held it until the final write, and DuckDB is single-writer per file —
// so for the entire hours-long run, `corral scans` against the same (default)
// DSN failed outright with "Conflicting lock is held". The record of the
// audits you have already paid for should not go dark for the duration of the
// next one.
//
// The cache therefore holds a DSN, not a handle: it opens for a lookup and
// closes immediately. Between lookups anyone can open the ledger.
func TestLedgerCacheHoldsNoLockBetweenLookups(t *testing.T) {
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	path := closedCacheTestStore(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{}`, ComputedAt: when,
	}})
	c := newLedgerCache(path)

	for i := 0; i < 3; i++ {
		if _, ok := c.Get("acme", "K"); !ok {
			t.Fatalf("lookup %d missed a verdict that is in the ledger", i)
		}
		// A SECOND reader — `corral scans`, in production — must be able to
		// open the same file right now.
		reader, err := scanstore.Open(path)
		if err != nil {
			t.Fatalf("after lookup %d the ledger is still locked: %v", i, err)
		}
		if _, err := reader.Scans(nil2ctx(), 10); err != nil {
			t.Fatalf("after lookup %d: reading scans: %v", i, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("closing the second reader: %v", err)
		}
	}

	// And the cache field itself: nothing is held between operations.
	if c.store != nil {
		t.Error("the cache is holding an open store between lookups — that IS the lock")
	}
}
