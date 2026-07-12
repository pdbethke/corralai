// SPDX-License-Identifier: Elastic-2.0

// gate_tests.go: the human-gate transitions on top of the gate_tests store
// Task 1 built. A saved GateTest starts unvetted (SaveCandidate always forces
// vetted=false); Promote and Reject are the only two ways a candidate leaves
// that unvetted state — the control owner's approval or rejection, reusing corral's
// memory-vetting cycle (shared → SetShared) for this domain.
package controlspec

import (
	"fmt"
	"time"
)

// Promote marks an UNVETTED candidate vetted (the control owner's approval — only a
// vetted test may gate). Returns ok=false when there is no unvetted row to
// promote (already vetted, or absent). now is caller-stamped — the store
// never calls time.Now() itself, which keeps it deterministic under test.
func (s *Store) Promote(owner, goal, target string, now time.Time) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE gate_tests SET vetted = TRUE, vetted_ts = ? WHERE owner = ? AND goal = ? AND target = ? AND vetted = FALSE`,
		now, owner, goal, target)
	if err != nil {
		return false, fmt.Errorf("controlspec: promote: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("controlspec: promote: rows affected: %w", err)
	}
	return n > 0, nil
}

// Reject deletes a candidate (vetted or not). Returns ok=false when no such
// row exists.
func (s *Store) Reject(owner, goal, target string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM gate_tests WHERE owner = ? AND goal = ? AND target = ?`,
		owner, goal, target)
	if err != nil {
		return false, fmt.Errorf("controlspec: reject: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("controlspec: reject: rows affected: %w", err)
	}
	return n > 0, nil
}
