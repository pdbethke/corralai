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

func cacheTestStore(t *testing.T) *scanstore.Store {
	t.Helper()
	s, err := scanstore.Open(filepath.Join(t.TempDir(), "scans.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestLedgerCacheHitCarriesVerdictAndComputedAt(t *testing.T) {
	s := cacheTestStore(t)
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Record(nil2ctx(), scanstore.Scan{Owner: "acme", Repo: "r"}, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{}`, ComputedAt: when,
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, ok := newLedgerCache(s).Get("acme", "K")
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
	s := cacheTestStore(t)
	if _, err := s.Record(nil2ctx(), scanstore.Scan{Owner: "acme", Repo: "r"}, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{not json`, ComputedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, ok := newLedgerCache(s).Get("acme", "K"); ok {
		t.Fatal("corrupt verdict JSON produced a HIT — the cache must fail closed")
	}
}

func TestLedgerCacheMissesOnEmptyOwnerAndNilStore(t *testing.T) {
	if _, ok := newLedgerCache(cacheTestStore(t)).Get("", "K"); ok {
		t.Fatal("empty owner produced a hit")
	}
	if _, ok := newLedgerCache(nil).Get("acme", "K"); ok {
		t.Fatal("nil store produced a hit — a cache must never be able to fail a scan")
	}
}

// A TimedOut verdict is a banked partial from a run that hit its wall clock:
// an artifact of load, not of content. Caching it would serve the partial
// forever, when a re-run may converge.
func TestLedgerCacheDoesNotServeATimedOutVerdict(t *testing.T) {
	s := cacheTestStore(t)
	js, err := marshalVerdict(advpool.Verdict{TimedOut: true})
	if err != nil {
		t.Fatalf("marshalVerdict: %v", err)
	}
	if _, err := s.Record(nil2ctx(), scanstore.Scan{Owner: "acme", Repo: "r"}, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: js, ComputedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, ok := newLedgerCache(s).Get("acme", "K"); ok {
		t.Fatal("a TimedOut verdict was served from cache")
	}
}

func TestLedgerCachePutDoesNotWrite(t *testing.T) {
	s := cacheTestStore(t)
	c := newLedgerCache(s)
	c.Put("acme", reposcan.FileResult{Gradable: true, Job: reposcan.Job{CacheKey: "K"}})
	if _, ok := c.Get("acme", "K"); ok {
		t.Fatal("Put wrote a row — verdicts must reach the ledger only through Record, or the ledger gets duplicate rows")
	}
}
