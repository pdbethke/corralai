// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"fmt"
)

// MutantAttempt is one seat's outcome for one mutant: which model, in which
// role, killed or did not kill it. Two rows sharing (scan_id, path, mutant_id)
// and differing in model are the paired observation internal/modelcorr needs.
type MutantAttempt struct {
	ScanID   int64
	Path     string
	MutantID string
	Model    string
	Role     string // "test-writer" | "test-writer-shadow"
	// Shadow is derivable from Role but kept for query convenience and for
	// symmetry with bugcatch_observations.
	Shadow  bool
	Outcome string // "killed" | "survived"
}

// RecordMutantAttempts writes a batch of seat outcomes in one transaction.
//
// Callers must write BOTH seats of a pair or neither — this function does not
// enforce that, because it cannot see the run; advpool's shadow writer pass
// owns the pair rule.
func (s *Store) RecordMutantAttempts(ctx context.Context, as []MutantAttempt) error {
	if len(as) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scanstore: RecordMutantAttempts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range as {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mutant_attempts (scan_id, path, mutant_id, model, role, shadow, outcome) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			a.ScanID, a.Path, a.MutantID, a.Model, a.Role, a.Shadow, a.Outcome,
		); err != nil {
			return fmt.Errorf("scanstore: RecordMutantAttempts: insert %s/%s/%s: %w", a.Path, a.MutantID, a.Model, err)
		}
	}
	return tx.Commit()
}

// AttemptsForScan returns every seat outcome for a scan, for round-trip tests
// and for the correlation reader.
func (s *Store) AttemptsForScan(ctx context.Context, scanID int64) ([]MutantAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scan_id, path, mutant_id, model, role, shadow, outcome FROM mutant_attempts WHERE scan_id = ? ORDER BY path, mutant_id, model`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: AttemptsForScan: %w", err)
	}
	defer rows.Close()
	var out []MutantAttempt
	for rows.Next() {
		var a MutantAttempt
		if err := rows.Scan(&a.ScanID, &a.Path, &a.MutantID, &a.Model, &a.Role, &a.Shadow, &a.Outcome); err != nil {
			return nil, fmt.Errorf("scanstore: AttemptsForScan: scan row: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: AttemptsForScan: rows: %w", err)
	}
	return out, nil
}
