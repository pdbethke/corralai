// SPDX-License-Identifier: Elastic-2.0

package criticscore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrationRunsAgainstAnOlderStore exercises the path the rest of this
// suite structurally cannot: every other test creates a FRESH table, whose
// CREATE TABLE already carries the full column set, so migrateCriticFindings
// runs zero ALTERs and its DDL is never executed.
//
// A malformed migration therefore passes the entire suite and fails only on a
// real store the first time someone upgrades — which is exactly what happened:
// the ddl duplicated the "ALTER TABLE ... ADD COLUMN" prefix the helper already
// builds, and every test stayed green.
func TestMigrationRunsAgainstAnOlderStore(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "old.duckdb")

	// Build a store at the PRE-migration schema by hand.
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE critic_findings (
		id VARCHAR PRIMARY KEY, ts DOUBLE, record_id BIGINT, record_head VARCHAR,
		repo VARCHAR, commit VARCHAR, mission_id BIGINT, model VARCHAR,
		target_test VARCHAR, test_file VARCHAR, test_selector VARCHAR, scope VARCHAR,
		evidence VARCHAR, severity VARCHAR,
		adjudication VARCHAR NOT NULL DEFAULT 'unadjudicated', source VARCHAR NOT NULL DEFAULT 'auto',
		adjudicated_by VARCHAR, adjudicated_ts DOUBLE
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO critic_findings (id, ts, record_id, record_head, repo, commit, mission_id, model,
		target_test, test_file, test_selector, scope, evidence, severity, adjudication, source, adjudicated_by, adjudicated_ts)
		VALUES ('9:1', 1, 9, 'h', 'r', 'c', 1, 'm', 't', 'f', 's', 'dead-check', 'e', 'high', 'unadjudicated', 'auto', NULL, NULL)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Opening must bring it forward, preserving the existing row.
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("opening an older store must migrate it, got: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if ok, err := s.Adjudicate(ctx, "9:1", "refuted", "alice", "checked it; the assertion does fail when the code breaks"); err != nil || !ok {
		t.Fatalf("adjudicate after migration: ok=%v err=%v", ok, err)
	}
	f, ok, err := s.Get(ctx, "9:1")
	if err != nil || !ok {
		t.Fatalf("get after migration: ok=%v err=%v", ok, err)
	}
	if f.Rationale == "" {
		t.Fatal("the migrated column must round-trip a rationale")
	}
	// Opening twice must be a no-op, not a duplicate-column error.
	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("migration must be idempotent: %v", err)
	}
	_ = s2.Close()
}
