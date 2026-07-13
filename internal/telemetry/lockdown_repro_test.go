// SPDX-License-Identifier: Elastic-2.0

package telemetry

import (
	"testing"
)

// TestAdHocQueryDoesNotBreakCheckpoint is the regression for the audited HIGH:
// an ad-hoc Query must NOT poison the store's shared read/write pool. Before the
// fix, Query applied the DuckDB filesystem lockdown (disabled_filesystems +
// lock_configuration, both database-wide) on a pooled conn, so the next
// CHECKPOINT on a writer fataled ("File system LocalFileSystem has been
// disabled") — crash + silent audit-log loss. Sequence: write, Query, write,
// explicit CHECKPOINT, Close, reopen, count. All must succeed with no data loss.
func TestAdHocQueryDoesNotBreakCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tel.duckdb"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Record(Event{MissionID: 1, Kind: "created", Actor: "engine"}); err != nil {
			t.Fatalf("pre-query write %d: %v", i, err)
		}
	}
	if _, err := s.Query("SELECT count(*) FROM events"); err != nil {
		t.Fatalf("ad-hoc query: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Record(Event{MissionID: 2, Kind: "task_claimed", Actor: "bee", Subject: "t"}); err != nil {
			t.Fatalf("post-query write %d: %v (lockdown poisoned the write pool)", i, err)
		}
	}
	// The exact operation that fataled before the fix.
	if _, err := s.db.Exec("CHECKPOINT"); err != nil {
		t.Fatalf("CHECKPOINT after ad-hoc query failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close (checkpoints on close): %v", err)
	}
	// Reopen and confirm all 10 events persisted (no silent audit-log loss).
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	var n int
	if err := s2.db.QueryRow("SELECT count(*) FROM events").Scan(&n); err != nil {
		t.Fatalf("count after reopen: %v", err)
	}
	if n != 10 {
		t.Fatalf("persisted %d events, want 10 — audit-log data loss", n)
	}
}
