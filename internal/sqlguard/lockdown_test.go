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

// TestApplyLockdown_FreshConnNotSkipped pins the invariant behind the idempotent
// skip in ApplyLockdown: it only fires when lock_configuration is ALREADY true,
// which (per the comment on ApplyLockdown) is set exclusively by ApplyLockdown
// itself. A brand-new conn must start unlocked and must have the actual SET
// statements run (not silently skipped) — proved here by checking the settings
// directly, not just the behavioral (read_csv-blocked) side effect.
func TestApplyLockdown_FreshConnNotSkipped(t *testing.T) {
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

	var locked string
	if err := conn.QueryRowContext(ctx, `SELECT CAST(current_setting('lock_configuration') AS VARCHAR)`).Scan(&locked); err != nil {
		t.Fatalf("query lock_configuration before ApplyLockdown: %v", err)
	}
	if locked == "true" {
		t.Fatal("a fresh conn should not already be locked — invariant violated before the test even runs ApplyLockdown")
	}

	if err := ApplyLockdown(ctx, conn); err != nil {
		t.Fatalf("ApplyLockdown: %v", err)
	}

	// The SETs must have actually run (not been skipped): lock_configuration
	// itself flips to true (it is the LAST of the SET statements, so seeing it
	// true proves every prior SET in the list executed on this conn too — a
	// silent skip would have left it at the pre-check value), and the fs
	// lockdown it froze is now provably in effect (read_csv refused by the
	// engine, and the setting can no longer be relaxed).
	if err := conn.QueryRowContext(ctx, `SELECT CAST(current_setting('lock_configuration') AS VARCHAR)`).Scan(&locked); err != nil {
		t.Fatalf("query lock_configuration after ApplyLockdown: %v", err)
	}
	if locked != "true" {
		t.Fatalf("lock_configuration = %q, want true after a real (non-skipped) ApplyLockdown", locked)
	}
	if _, err := conn.QueryContext(ctx, `SELECT * FROM read_csv('/etc/hostname')`); err == nil {
		t.Fatal("read_csv should be blocked immediately after a real (non-skipped) ApplyLockdown")
	}
	if _, err := conn.ExecContext(ctx, `SET disabled_filesystems = ''`); err == nil {
		t.Fatal("disabled_filesystems should be frozen after a real (non-skipped) ApplyLockdown")
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
