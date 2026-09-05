// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
)

// SelectionCacheGet returns the raw instrumented-coverage evidence a PRIOR
// scan recorded for this exact (treeDigest, cmdDigest, plugin, substrate)
// key. ok=false is the honest miss —
// no scan has ever run this exact instrumented command over this exact tree
// on this exact substrate — and carries no error: a cache miss is not a
// failure, it is the common case.
//
// substrate is part of the key, not merely disclosed: a jail run's
// instrumented evidence can be degraded in ways specific to that sandbox
// (see the jail Python recipe's preflight caveat), so evidence earned under
// substrateJail must never be served to a substrateWorkspace scan over the
// identical tree, or the reverse — a degraded-but-Ran=true row masquerading
// as a clean measurement on the other substrate. This is the #110 class of
// bug (a key blind to a dimension that changes what the row MEANS)
// recurring one cache later.
//
// raw comes back byte-for-byte identical to what was Put: a caller serving
// it as reposcan.SelectionEvidence.Raw must reproduce EXACTLY what the
// original instrumented run measured, not a re-encoding of it.
func (s *Store) SelectionCacheGet(ctx context.Context, treeDigest, cmdDigest, plugin, substrate string) (raw []byte, ok bool, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT raw FROM selection_cache
		WHERE tree_digest = ? AND cmd_digest = ? AND plugin = ? AND substrate = ?`,
		treeDigest, cmdDigest, plugin, substrate)
	if serr := row.Scan(&raw); serr != nil {
		if serr == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("scanstore: selection cache get: %w", serr)
	}
	// A row whose raw is empty/whitespace-only can only have been written
	// before CollectSelectionEvidence stopped Put-ing a failed instrumented
	// run's empty output as though it were real evidence (a Ran:true
	// document that measured nothing) — the fix closes the write side, but
	// a ledger already holding one of these rows must not keep serving it
	// forever. Treated as an honest miss: the caller re-runs the
	// instrumented pass, which either heals the row (a real Put on success)
	// or produces a fresh, disclosed failure — either way this ledger stops
	// being the reason a repo's selection stays dead.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false, nil
	}
	return raw, true, nil
}

// SelectionCachePut records the raw evidence one instrumented run produced
// for (treeDigest, cmdDigest, plugin, substrate). An upsert, the same reason GoalCachePut is one: a later Put for a
// key this store already holds (a re-run after a corrupted or evicted row)
// replaces it rather than erroring on the UNIQUE constraint, so this cache
// can never itself become the reason a scan fails. The Put happens at
// the end of the run, not at collection time: only a run that completed
// has proven its evidence was worth keeping (see cmd/corral's
// pendingSelectionPut).
func (s *Store) SelectionCachePut(ctx context.Context, treeDigest, cmdDigest, plugin, substrate string, raw []byte, note string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO selection_cache
		(tree_digest, cmd_digest, plugin, substrate, raw, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, now())
		ON CONFLICT (tree_digest, cmd_digest, plugin, substrate)
		DO UPDATE SET raw = excluded.raw, note = excluded.note, created_at = excluded.created_at`,
		treeDigest, cmdDigest, plugin, substrate, raw, note); err != nil {
		return fmt.Errorf("scanstore: selection cache put: %w", err)
	}
	return nil
}
