// SPDX-License-Identifier: Elastic-2.0

package sqlguard

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// TestApplyLockdown_BlocksFilesystem proves ApplyLockdown is the real wall: after
// it runs, a read_csv against the local filesystem is refused by the ENGINE (not
// just the validator), and lock_configuration is frozen so it can't be undone.
func TestApplyLockdown_BlocksFilesystem(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := ApplyLockdown(ctx, conn); err != nil {
		t.Fatalf("ApplyLockdown: %v", err)
	}
	// read_csv must now be refused by the engine (bypassing the validator entirely).
	if _, err := conn.QueryContext(ctx, `SELECT * FROM read_csv('/etc/hostname')`); err == nil {
		t.Fatal("read_csv should be blocked by the lockdown, but it ran")
	}
	// The lockdown must be frozen — attempting to relax it fails.
	if _, err := conn.ExecContext(ctx, `SET disabled_filesystems = ''`); err == nil {
		t.Fatal("lock_configuration should prevent relaxing disabled_filesystems")
	}
}

// TestApplyLockdown_Idempotent proves re-applying on an already-sealed conn is a
// safe no-op — the case database/sql's connection pooling forces on the recordings
// and telemetry ad-hoc Query paths (a conn returned to the pool keeps
// lock_configuration=true; the next Query grabs it and calls ApplyLockdown again).
func TestApplyLockdown_Idempotent(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := ApplyLockdown(ctx, conn); err != nil {
		t.Fatalf("first ApplyLockdown: %v", err)
	}
	// Second application on the same (already-locked) conn must NOT error.
	if err := ApplyLockdown(ctx, conn); err != nil {
		t.Fatalf("second ApplyLockdown should be a no-op, got: %v", err)
	}
	// And the conn is still sealed after the no-op.
	if _, err := conn.QueryContext(ctx, `SELECT * FROM read_csv('/etc/hostname')`); err == nil {
		t.Fatal("read_csv should still be blocked after idempotent re-apply")
	}
	// A legit read-only query still works on the sealed conn.
	var one int
	if err := conn.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("plain SELECT on sealed conn: one=%d err=%v", one, err)
	}
}
