// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// ledgerCache is reposcan.Cache backed by the ledger DIRECTORY: a file
// whose every verdict-affecting input is unchanged (the cache key —
// reposcan.KeyInputs — covers the bytes, the tests, the models, the engine,
// the substrate and the prior) is not re-audited; the verdict an earlier
// entry recorded under the same key is served, marked as reused, with the
// time it was earned.
//
// The directory is read ONCE, when the cache is built, into a map keyed by
// cache key: a scan of N files against a ledger of M entries must not pay
// M decompressions per file. Later entries win, which is the order
// ReadLedgerDir returns them in (oldest push first).
//
// It FAILS CLOSED without exception. No directory, an unreadable one, an
// entry whose verdict JSON will not unmarshal, a verdict not worth serving
// (timed out, writer failed, unsound pool test) — every one of these is a
// MISS, never a served verdict. An unreadable directory is said once on
// stderr: a cache that quietly misses every key would make the next run
// look like a full re-audit for no visible reason.
//
// Put is a no-op: the scan's own ledger entry, written at the end of the
// run, is what a later scan reads. There is one write path to the record.
type ledgerCache struct {
	byKey map[string]cachedVerdict
}

type cachedVerdict struct {
	verdict    advpool.Verdict
	computedAt time.Time
	// toolAccount is the file's account of the --mutants-from report, read
	// from the SAME entry the verdict is served from (its ScanRow.
	// ToolMutants, by path); nil when that entry recorded none.
	toolAccount *ledger.ToolMutantCounts
}

func newLedgerCache(dir string, stderr io.Writer) *ledgerCache {
	c := &ledgerCache{byKey: map[string]cachedVerdict{}}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return c
	}
	all, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: verdict cache: %s is unreadable, so no verdict will be reused: %v\n", dir, err)
		return c
	}
	// A retracted entry's verdicts are never served (auditpush.ScanEntries).
	for _, e := range auditpush.ScanEntries(all) {
		accounts := toolAccountsByPath(e.Bundle.Scan.ToolMutants)
		for _, r := range e.Bundle.Files {
			if r.CacheKey == "" || r.VerdictJSON == "" {
				continue
			}
			var v advpool.Verdict
			if err := json.Unmarshal([]byte(r.VerdictJSON), &v); err != nil {
				continue
			}
			if !verdictWorthServing(v) {
				continue
			}
			at := e.Pushed
			if r.ComputedAt != nil && !r.ComputedAt.IsZero() {
				at = *r.ComputedAt
			}
			c.byKey[r.CacheKey] = cachedVerdict{verdict: v, computedAt: at, toolAccount: accounts[r.Path]}
		}
	}
	return c
}

// Get serves a verdict recorded under exactly this cache key. owner is part
// of reposcan.Cache's contract and is not a key input here: the key already
// binds the file's bytes, and the directory is the audited repo's own.
func (c *ledgerCache) Get(owner, cacheKey string) (reposcan.FileResult, bool) {
	if c == nil || strings.TrimSpace(cacheKey) == "" {
		return reposcan.FileResult{}, false
	}
	hit, ok := c.byKey[cacheKey]
	if !ok {
		return reposcan.FileResult{}, false
	}
	return reposcan.FileResult{
		Verdict:    hit.verdict,
		Gradable:   true, // only gradable results are ever recorded with a key
		ComputedAt: hit.computedAt,
	}, true
}

// verdictWorthServing is the one rule for which recorded verdicts a cache
// may serve. A verdict that timed out, whose writer failed, or whose pool
// test was unsound was never a complete measurement; a per-survivor verdict
// whose writer seats all went ungraded proved nothing about its survivors.
// Serving any of them would carry the failure forward as a "reused"
// result, indistinguishable on the report from a verdict that earned it.
func verdictWorthServing(v advpool.Verdict) bool {
	if v.TimedOut || v.TestWriterFailed || v.PoolTestUnsound {
		return false
	}
	if v.WriterMode == advpool.WriterModePerSurvivor && v.Survivors > 0 && v.WriterSeatsUngraded >= v.Survivors {
		return false
	}
	return true
}

// oldestReuse is the age of the oldest reused verdict in a scan's results,
// for the report line beside the reuse count: "3 verdict(s) reused, the
// oldest from <date>" says how stale the cheapest part of the scan is.
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

func (c *ledgerCache) Put(string, reposcan.FileResult) {}

// toolAccount is the tool account recorded beside the verdict served under
// cacheKey. A served file ran no audit, so nothing handed its account to the
// scan's sink, yet its tool-sourced mutant rows are written from the served
// verdict; the account comes from the entry that earned that verdict, so the
// rows and the account on one entry always describe the same exam. It is
// carried, not recomputed: recomputing would re-derive the file's budget
// outside the audit, a second derivation of a recorded number that could
// drift from the first.
func (c *ledgerCache) toolAccount(cacheKey string) (ledger.ToolMutantCounts, bool) {
	if c == nil {
		return ledger.ToolMutantCounts{}, false
	}
	hit, ok := c.byKey[cacheKey]
	if !ok || hit.toolAccount == nil {
		return ledger.ToolMutantCounts{}, false
	}
	return *hit.toolAccount, true
}

// toolAccountsByPath reads a scan row's ToolMutants text by path. A text
// that does not parse yields none: the cache fails closed on what it cannot
// read, and the served file's account is then absent, never guessed.
func toolAccountsByPath(text string) map[string]*ledger.ToolMutantCounts {
	if text == "" {
		return nil
	}
	var counts []ledger.ToolMutantCounts
	if err := json.Unmarshal([]byte(text), &counts); err != nil {
		return nil
	}
	out := make(map[string]*ledger.ToolMutantCounts, len(counts))
	for i := range counts {
		out[counts[i].Path] = &counts[i]
	}
	return out
}

// marshalVerdict is the one encoding of a verdict the record carries and
// the cache reads back; the two must agree, so there is one function.
func marshalVerdict(v advpool.Verdict) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
