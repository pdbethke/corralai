// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/scanstore"
)

// goalLedgerCache is reposcan.GoalCacheStore backed by the same DuckDB
// ledger the verdict cache (ledgerCache, verdict_cache.go) uses — a goal
// derived for identical bytes, by the same model under the same prompt
// revision, is a fact worth keeping rather than re-purchasing.
//
// It holds a DSN, not a handle, and opens/closes around each operation for
// the same reason ledgerCache does: DuckDB is single-writer per file, and a
// handle held for a whole scan would lock a concurrent `corral scans`
// (or another scan's own goal lookups) out of the same file for the
// duration.
//
// Fails closed like ledgerCache: an unopenable DSN is a miss on Get and a
// silently-dropped Put, never an error that could fail the scan. A goal
// cache miss costs one extra model call — annoying, never wrong.
type goalLedgerCache struct{ dsn string }

func newGoalLedgerCache(dsn string) *goalLedgerCache {
	return &goalLedgerCache{dsn: strings.TrimSpace(dsn)}
}

func (c *goalLedgerCache) GoalCacheGet(ctx context.Context, path, sourceDigest, model, promptRev string) (goal, provenance string, ungoaled, ok bool, err error) {
	if c == nil || c.dsn == "" {
		return "", "", false, false, nil
	}
	st, oerr := scanstore.Open(c.dsn)
	if oerr != nil {
		return "", "", false, false, nil
	}
	defer func() { _ = st.Close() }()
	return st.GoalCacheGet(ctx, path, sourceDigest, model, promptRev)
}

func (c *goalLedgerCache) GoalCachePut(ctx context.Context, path, sourceDigest, model, promptRev, goal, provenance string, ungoaled bool) error {
	if c == nil || c.dsn == "" {
		return nil
	}
	st, oerr := scanstore.Open(c.dsn)
	if oerr != nil {
		return nil //nolint:nilerr -- fail closed: a write the ledger cannot accept must not fail the scan
	}
	defer func() { _ = st.Close() }()
	if err := st.GoalCachePut(ctx, path, sourceDigest, model, promptRev, goal, provenance, ungoaled); err != nil {
		return fmt.Errorf("goal cache: %w", err)
	}
	return nil
}
