// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"strings"

	"github.com/pdbethke/corralai/internal/scanstore"
)

// selectionCacheStore is the persistence seam collectSelection looks up
// through — the small shape *scanstore.Store already satisfies (its
// SelectionCacheGet/Put methods match this exactly) and selectionLedgerCache
// below also satisfies, opening a fresh handle per operation. Defined here,
// not in internal/reposcan: the selection cache is a cmd/corral-level
// concern (collectSelection lives in certify_repo.go, not reposcan), so
// unlike GoalCacheStore this interface has no reason to cross a package
// boundary at all.
type selectionCacheStore interface {
	SelectionCacheGet(ctx context.Context, treeDigest, cmdDigest, plugin string) (raw []byte, scanID int64, ok bool, err error)
	SelectionCachePut(ctx context.Context, treeDigest, cmdDigest, plugin string, raw []byte, note string, scanID int64) error
}

// selectionLedgerCache is a selectionCacheStore backed by the same DuckDB
// ledger the verdict and goal caches use — opened and closed around each
// operation for the same single-writer-per-file reason goalLedgerCache is
// (see its own doc): a handle held for a whole scan would lock a concurrent
// `corral scans` (or another scan's own lookups) out of the same file.
//
// Only its Get half is ever actually reached through this type: a Put needs
// the scan's own ledger id, which does not exist until Record has run (see
// collectSelection's doc and this task's report), so the CLI's recording
// sequence calls st.SelectionCachePut directly on the *scanstore.Store it
// already has open there rather than through a second handle opened here.
// This type still implements the full interface — including Put — so a
// test can substitute a fake without caring which half of the store a given
// scan happened to reach.
//
// Fails closed like goalLedgerCache: an unopenable DSN is a miss on Get,
// never an error that could fail the scan. A selection cache miss costs one
// extra instrumented suite run — expensive, never wrong.
type selectionLedgerCache struct{ dsn string }

func newSelectionLedgerCache(dsn string) *selectionLedgerCache {
	return &selectionLedgerCache{dsn: strings.TrimSpace(dsn)}
}

func (c *selectionLedgerCache) SelectionCacheGet(ctx context.Context, treeDigest, cmdDigest, plugin string) (raw []byte, scanID int64, ok bool, err error) {
	if c == nil || c.dsn == "" {
		return nil, 0, false, nil
	}
	st, oerr := scanstore.Open(c.dsn)
	if oerr != nil {
		return nil, 0, false, nil
	}
	defer func() { _ = st.Close() }()
	return st.SelectionCacheGet(ctx, treeDigest, cmdDigest, plugin)
}

func (c *selectionLedgerCache) SelectionCachePut(ctx context.Context, treeDigest, cmdDigest, plugin string, raw []byte, note string, scanID int64) error {
	if c == nil || c.dsn == "" {
		return nil
	}
	st, oerr := scanstore.Open(c.dsn)
	if oerr != nil {
		return nil //nolint:nilerr -- fail closed: a write the ledger cannot accept must not fail the scan
	}
	defer func() { _ = st.Close() }()
	return st.SelectionCachePut(ctx, treeDigest, cmdDigest, plugin, raw, note, scanID)
}
