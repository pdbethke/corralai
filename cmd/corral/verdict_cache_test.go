// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// cacheTestLedger writes one ledger entry holding files, the way the run
// writes its own (buildBundle → the directory writer, source carried), and
// returns the directory — the state every ledgerCache read sees in
// production.
func cacheTestLedger(t *testing.T, files []scanstore.File) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	writeCacheTestEntry(t, dir, files)
	return dir
}

func writeCacheTestEntry(t *testing.T, dir string, files []scanstore.File) {
	t.Helper()
	b := buildBundle(scanstore.Scan{Owner: "acme", Repo: "r"}, 0, files, nil, nil, nil,
		auditpush.Link{}, true, "acme/r", "abc", "", bundleMeta{})
	b.SourcePushed, b.Scan.SourcePushed = true, true
	if _, err := pushBundle(dir+"/", b); err != nil {
		t.Fatalf("writing the ledger entry: %v", err)
	}
}

func TestLedgerCacheHitCarriesVerdictAndComputedAt(t *testing.T) {
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	dir := cacheTestLedger(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{}`, ComputedAt: when,
	}})
	got, ok := newLedgerCache(dir, io.Discard).Get("acme", "K")
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

// The newest entry wins for a key two entries share: a file re-audited
// under the same key (--no-verdict-cache) must serve its LATEST verdict.
func TestLedgerCacheServesTheNewestEntryForAKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	older, _ := marshalVerdict(advpool.Verdict{Survivors: 9})
	newer, _ := marshalVerdict(advpool.Verdict{Survivors: 1})
	writeCacheTestEntry(t, dir, []scanstore.File{{Path: "a.go", Disposition: "audited", Gradable: true, CacheKey: "K", VerdictJSON: older, ComputedAt: time.Now().UTC()}})
	writeCacheTestEntry(t, dir, []scanstore.File{{Path: "a.go", Disposition: "audited", Gradable: true, CacheKey: "K", VerdictJSON: newer, ComputedAt: time.Now().UTC()}})
	got, ok := newLedgerCache(dir, io.Discard).Get("acme", "K")
	if !ok || got.Verdict.Survivors != 1 {
		t.Fatalf("got %+v ok=%v, want the newer entry's verdict (1 survivor)", got.Verdict, ok)
	}
}

// Fail closed: unparseable JSON is a MISS, never an error and never a partial
// verdict. Re-auditing costs money; serving a half-decoded verdict signs a
// claim nothing measured.
func TestLedgerCacheMissesOnUnparseableVerdict(t *testing.T) {
	dir := cacheTestLedger(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{not json`, ComputedAt: time.Now().UTC(),
	}})
	if _, ok := newLedgerCache(dir, io.Discard).Get("acme", "K"); ok {
		t.Fatal("corrupt verdict JSON produced a HIT — the cache must fail closed")
	}
}

func TestLedgerCacheMissesOnEmptyKeyNoDirAndAnUnreadableDir(t *testing.T) {
	dir := cacheTestLedger(t, nil)
	if _, ok := newLedgerCache(dir, io.Discard).Get("acme", ""); ok {
		t.Fatal("an empty key produced a hit")
	}
	if _, ok := newLedgerCache("", io.Discard).Get("acme", "K"); ok {
		t.Fatal("no directory produced a hit — a cache must never be able to fail a scan")
	}
	// A directory that does not exist is an empty ledger, not an error:
	// the first scan of every repo starts there. Nothing on stderr.
	var quiet bytes.Buffer
	if _, ok := newLedgerCache(filepath.Join(t.TempDir(), "nope"), &quiet).Get("acme", "K"); ok || quiet.Len() != 0 {
		t.Fatalf("a not-yet-existing ledger: hit=%v stderr=%q, want a silent miss", ok, quiet.String())
	}
	// An entry that cannot be read is said ONCE, and misses every key.
	broken := filepath.Join(t.TempDir(), "ledger")
	if err := os.MkdirAll(filepath.Join(broken, auditpush.ScansSubdir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, auditpush.ScansSubdir, "20260101T000000Z-abc-def.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var loud bytes.Buffer
	if _, ok := newLedgerCache(broken, &loud).Get("acme", "K"); ok {
		t.Fatal("an unreadable ledger produced a hit — the cache fails CLOSED")
	}
	if !strings.Contains(loud.String(), "no verdict will be reused") {
		t.Fatalf("an unreadable ledger must be said on stderr, got %q", loud.String())
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
	dir := cacheTestLedger(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: js, ComputedAt: time.Now().UTC(),
	}})
	if _, ok := newLedgerCache(dir, io.Discard).Get("acme", "K"); ok {
		t.Fatal("a TimedOut verdict was served from cache")
	}
}

func TestLedgerCachePutDoesNotWrite(t *testing.T) {
	dir := cacheTestLedger(t, nil)
	c := newLedgerCache(dir, io.Discard)
	c.Put("acme", reposcan.FileResult{Gradable: true, Job: reposcan.Job{CacheKey: "K"}})
	if _, ok := c.Get("acme", "K"); ok {
		t.Fatal("Put served a verdict — verdicts reach the ledger only through the run's own entry, or the record gets a second writer")
	}
	if entries, _ := auditpush.ReadLedgerDir(dir); len(entries) != 1 {
		t.Fatalf("Put wrote to the ledger: %d entries, want the fixture's 1", len(entries))
	}
}

// TestLedgerCacheDoesNotServeAVerdictWhoseWriterHalfNeverGraded: only
// TimedOut used to be refused. A writer-failed, unsound, or all-seats-
// ungraded verdict is an artifact of its run (a 429, a model that produced
// nothing compiling), and serving it pinned "proven_missed=0 [WRITER
// FAILED]" — and a --max-proven-missed failure — on the file for every
// later scan of the same content.
func TestLedgerCacheDoesNotServeAVerdictWhoseWriterHalfNeverGraded(t *testing.T) {
	for name, v := range map[string]advpool.Verdict{
		"writer failed":        {TestWriterFailed: true, Survivors: 9},
		"pool test unsound":    {PoolTestUnsound: true, Survivors: 9},
		"every seat ungraded":  {WriterMode: advpool.WriterModePerSurvivor, Survivors: 9, WriterSeatsUngraded: 9},
		"provider never spoke": {TestWriterFailed: true, WriterProviderFailed: true, Survivors: 2},
	} {
		js, err := marshalVerdict(v)
		if err != nil {
			t.Fatalf("marshalVerdict: %v", err)
		}
		dir := cacheTestLedger(t, []scanstore.File{{
			Path: "a.go", Disposition: "audited", Gradable: true,
			CacheKey: "K", VerdictJSON: js, ComputedAt: time.Now().UTC(),
		}})
		if _, ok := newLedgerCache(dir, io.Discard).Get("acme", "K"); ok {
			t.Errorf("%s: served from cache", name)
		}
	}
	// A verdict that graded — even one that proved nothing — is served.
	js, _ := marshalVerdict(advpool.Verdict{WriterMode: advpool.WriterModePerSurvivor, Survivors: 9, WriterSeatsUngraded: 3, ProvenMissed: 0})
	dir := cacheTestLedger(t, []scanstore.File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: js, ComputedAt: time.Now().UTC(),
	}})
	if _, ok := newLedgerCache(dir, io.Discard).Get("acme", "K"); !ok {
		t.Error("a verdict with six of nine seats graded was refused — that is a measurement")
	}
}

// roundTripFilesThroughTheLedger writes files as one entry the way the
// run does and reads them back through the reader `corral scans` uses —
// the record's round trip, for a test that pins one column of it.
func roundTripFilesThroughTheLedger(t *testing.T, files []scanstore.File) []scanstore.File {
	t.Helper()
	dir := cacheTestLedger(t, files)
	st, err := openLedgerScans(dir)
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	defer st.Close()
	back, err := st.FilesForScan(context.Background(), 1)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	return back
}
