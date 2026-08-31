// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"database/sql"
	"fmt"
)

// SelectionCacheGet returns the raw instrumented-coverage evidence a PRIOR
// scan recorded for this exact (treeDigest, cmdDigest, plugin) key, and the
// id of the scan that earned it. ok=false is the honest miss — no scan has
// ever run this exact instrumented command over this exact tree — and
// carries no error: a cache miss is not a failure, it is the common case.
//
// raw comes back byte-for-byte identical to what was Put: a caller serving
// it as reposcan.SelectionEvidence.Raw must reproduce EXACTLY what the
// original instrumented run measured, not a re-encoding of it.
func (s *Store) SelectionCacheGet(ctx context.Context, treeDigest, cmdDigest, plugin string) (raw []byte, scanID int64, ok bool, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT raw, scan_id FROM selection_cache
		WHERE tree_digest = ? AND cmd_digest = ? AND plugin = ?`,
		treeDigest, cmdDigest, plugin)
	var id sql.NullInt64
	if serr := row.Scan(&raw, &id); serr != nil {
		if serr == sql.ErrNoRows {
			return nil, 0, false, nil
		}
		return nil, 0, false, fmt.Errorf("scanstore: selection cache get: %w", serr)
	}
	return raw, id.Int64, true, nil
}

// SelectionCachePut records the raw evidence one instrumented run produced
// for (treeDigest, cmdDigest, plugin), and the scan that paid for it. An
// upsert, the same reason GoalCachePut is one: a later Put for a key this
// store already holds (a re-run after a corrupted or evicted row) replaces
// it rather than erroring on the UNIQUE constraint, so this cache can never
// itself become the reason a scan fails.
//
// scanID is the id Record just assigned THIS scan — see
// cmd/corral/certify_repo.go's recording sequence for why the Put happens
// there, after the scan row exists, rather than at collection time (before
// it does).
func (s *Store) SelectionCachePut(ctx context.Context, treeDigest, cmdDigest, plugin string, raw []byte, note string, scanID int64) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO selection_cache
		(tree_digest, cmd_digest, plugin, raw, note, created_at, scan_id)
		VALUES (?, ?, ?, ?, ?, now(), ?)
		ON CONFLICT (tree_digest, cmd_digest, plugin)
		DO UPDATE SET raw = excluded.raw, note = excluded.note, created_at = excluded.created_at, scan_id = excluded.scan_id`,
		treeDigest, cmdDigest, plugin, raw, note, scanID); err != nil {
		return fmt.Errorf("scanstore: selection cache put: %w", err)
	}
	return nil
}
