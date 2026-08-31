// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"database/sql"
	"fmt"
)

// GoalCacheGet returns a previously derived (or previously-ungoaled) goal
// for this exact (path, sourceDigest, model, promptRev) key. ok=false means
// no row exists for this key — a miss, the honest default. ok=true with
// ungoaled=true means a prior scan asked the same question and the deriver
// said NONE; goal and provenance are then both "" — nothing to reuse but
// the FACT of "no goal", which is itself worth not re-asking a model for.
//
// This implements reposcan.GoalCacheStore, satisfied at compile time by
// cmd/corral's wiring rather than here — this package deliberately does not
// import reposcan (see reposcan.GoalCacheStore's own doc for the direction
// this dependency runs).
func (s *Store) GoalCacheGet(ctx context.Context, path, sourceDigest, model, promptRev string) (goal, provenance string, ungoaled, ok bool, err error) {
	var g, p sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT goal, provenance FROM goal_cache
		WHERE path = ? AND source_digest = ? AND model = ? AND engine_prompt_rev = ?`,
		path, sourceDigest, model, promptRev)
	if serr := row.Scan(&g, &p); serr != nil {
		if serr == sql.ErrNoRows {
			return "", "", false, false, nil
		}
		return "", "", false, false, fmt.Errorf("scanstore: goal cache get %q: %w", path, serr)
	}
	// A stored NULL goal is the ungoaled shape (see GoalCachePut): the row
	// exists — this exact question was asked before — but the deriver's
	// answer was "no goal", not an empty string worth reusing as one.
	if !g.Valid {
		return "", "", true, true, nil
	}
	return g.String, p.String, false, true, nil
}

// GoalCachePut records the answer to (path, sourceDigest, model, promptRev)
// — either a real goal, or the ungoaled fact that the deriver had nothing
// to say. An upsert: a later Put for the same key (e.g. a re-derivation
// after a corrupted or evicted row) replaces the row rather than erroring
// on the UNIQUE constraint, so this cache can never itself become the
// reason a scan fails.
func (s *Store) GoalCachePut(ctx context.Context, path, sourceDigest, model, promptRev, goal, provenance string, ungoaled bool) error {
	var g, p sql.NullString
	if !ungoaled {
		g = sql.NullString{String: goal, Valid: true}
		p = sql.NullString{String: provenance, Valid: true}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO goal_cache
		(path, source_digest, model, engine_prompt_rev, goal, provenance, created_at)
		VALUES (?, ?, ?, ?, ?, ?, now())
		ON CONFLICT (path, source_digest, model, engine_prompt_rev)
		DO UPDATE SET goal = excluded.goal, provenance = excluded.provenance, created_at = excluded.created_at`,
		path, sourceDigest, model, promptRev, g, p); err != nil {
		return fmt.Errorf("scanstore: goal cache put %q: %w", path, err)
	}
	return nil
}
