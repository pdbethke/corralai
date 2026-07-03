// SPDX-License-Identifier: Elastic-2.0

package fleet

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

func TestReportingSchemaListsTables(t *testing.T) {
	s := ReportingSchema()
	// Base tables must be qualified with the remote catalog prefix; fleet_missions_current is unqualified.
	for _, want := range []string{"remote.fleet_actions", "remote.fleet_missions", "remote.fleet_tasks", "remote.fleet_telemetry", "fleet_missions_current"} {
		if !strings.Contains(s, want) {
			t.Fatalf("ReportingSchema missing %q", want)
		}
	}
	// Bare (unqualified) base table names must NOT appear as TABLE declarations — that was the bug.
	for _, bare := range []string{"TABLE fleet_actions", "TABLE fleet_missions", "TABLE fleet_tasks", "TABLE fleet_telemetry"} {
		if strings.Contains(s, bare) {
			t.Fatalf("ReportingSchema still advertises unqualified table name %q; should use remote.<name>", bare)
		}
	}
	// it must NOT leak a content/store name — classification stays out of the prompt too.
	// "detail" is intentionally absent from this list: fleet_telemetry.detail is a
	// curated analytics column (structured JSON finding metadata — type/severity/outcome)
	// that feeds the model-comparison report. Free-form instruction/result payloads
	// remain banned via the denylist in TestClassificationNoContentStore.
	for _, bad := range []string{"memory", "reference", "repoindex"} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Fatalf("ReportingSchema references banned token %q", bad)
		}
	}
}

func TestCurrentStateViewDedup(t *testing.T) {
	views := CurrentStateViews()
	joined := strings.Join(views, "\n")
	if !strings.Contains(joined, "fleet_missions_current") || !strings.Contains(strings.ToLower(joined), "row_number") {
		t.Fatalf("expected a windowed fleet_missions_current view, got: %s", joined)
	}
}

// TestCurrentStateViewDedupFunctional proves the N+1/correctness fix: two
// temporal versions of the same mission in fleet_missions → the view returns
// only the latest row (highest updated_ts).
func TestCurrentStateViewDedupFunctional(t *testing.T) {
	dir := t.TempDir()
	remoteFile := filepath.Join(dir, "remote.duckdb")

	// Seed a remote duckdb with two versions of mission id=1 (updated_ts 1.0 then 2.0).
	mustDuckDB(t, remoteFile, `
		CREATE TABLE fleet_missions (
			brain VARCHAR, id BIGINT, directive VARCHAR, status VARCHAR,
			repo VARCHAR, branch VARCHAR, pr_url VARCHAR, review_rounds BIGINT,
			created_ts DOUBLE, updated_ts DOUBLE
		);
		INSERT INTO fleet_missions VALUES ('b', 1, 'd', 'old', '', '', '', 0, 1.0, 1.0);
		INSERT INTO fleet_missions VALUES ('b', 1, 'd', 'new', '', '', '', 0, 1.0, 2.0)`)

	// Open an in-memory DuckDB, attach the remote file, and create the view.
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("ATTACH '" + remoteFile + "' AS remote (READ_ONLY)"); err != nil {
		t.Fatalf("attach remote: %v", err)
	}
	for _, ddl := range CurrentStateViews() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create view: %v", err)
		}
	}

	// The view must return exactly one row — the latest (status='new').
	var count int
	if err := db.QueryRow("SELECT count(*) FROM fleet_missions_current WHERE brain='b' AND id=1").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 deduped row, got %d", count)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM fleet_missions_current WHERE brain='b' AND id=1").Scan(&status); err != nil {
		t.Fatalf("status query: %v", err)
	}
	if status != "new" {
		t.Fatalf("expected status='new' (latest updated_ts=2.0), got %q", status)
	}
}
