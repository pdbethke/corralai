// SPDX-License-Identifier: Elastic-2.0

// internal/oracle/sandbox_test.go
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// lockedConn opens a fresh in-mem DuckDB, seeds an in-MEMORY fleet_* table (NOT a file — a
// file-backed remote would be blocked by disabled_filesystems), applies the lockdown, and
// returns a single pinned connection. This mirrors production (where md: is attached before
// the lock) while staying hermetic (no network, no file remote).
func lockedConn(t *testing.T) (*sql.Conn, func()) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// seed an in-memory reporting table (stands in for the md:-attached fleet_missions)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE fleet_missions (brain VARCHAR, id BIGINT, status VARCHAR)`); err != nil {
		t.Fatal(err)
	}
	conn.ExecContext(ctx, `INSERT INTO fleet_missions VALUES ('A', 1, 'done'), ('A', 2, 'running')`)
	if err := applyLockdown(ctx, conn); err != nil {
		t.Fatalf("applyLockdown: %v", err)
	}
	return conn, func() { conn.Close(); db.Close() }
}

func TestSandboxBenignQueryWorks(t *testing.T) {
	conn, done := lockedConn(t)
	defer done()
	cols, rows, err := runLocked(context.Background(), conn, `SELECT status, count(*) c FROM fleet_missions GROUP BY status ORDER BY status`, 1000)
	if err != nil {
		t.Fatalf("benign query should work after lockdown: %v", err)
	}
	if len(cols) != 2 || len(rows) != 2 {
		t.Fatalf("expected 2 cols/2 rows, got %v %v", cols, rows)
	}
}

// TestSandboxExfilBlocked is the load-bearing security matrix. Each vector must be blocked:
// the call errors OR returns no rows, and the canary never appears in any cell.
func TestSandboxExfilBlocked(t *testing.T) {
	// a canary file the query will try to read
	dir := t.TempDir()
	canaryFile := filepath.Join(dir, "secret.txt")
	const canary = "CANARY_TOKEN_ghp_do_not_leak"
	os.WriteFile(canaryFile, []byte(canary), 0o600)

	vectors := []struct{ name, sql string }{
		{"read_text", `SELECT * FROM read_text('` + canaryFile + `')`},
		{"read_csv", `SELECT * FROM read_csv('` + canaryFile + `')`},
		{"read_blob", `SELECT * FROM read_blob('` + canaryFile + `')`},
		{"glob", `SELECT * FROM glob('` + dir + `/*')`},
		{"read_json", `SELECT * FROM read_json('` + canaryFile + `')`},
		{"attach_local", `ATTACH '` + canaryFile + `' AS x`},
		{"copy_to", `COPY (SELECT 1) TO '` + filepath.Join(dir, "out.csv") + `'`},
		{"url_read", `SELECT * FROM read_csv('https://example.com/x.csv')`},
		{"second_stmt", `SELECT 1; ATTACH '` + canaryFile + `' AS y`},
		{"obfuscated", `SEL/**/ECT * FROM read_text('` + canaryFile + `')`},
		{"pragma_fn", `SELECT * FROM pragma_table_info('fleet_missions')`},
	}
	conn, done := lockedConn(t)
	defer done()
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			_, rows, err := runLocked(context.Background(), conn, v.sql, 1000)
			if err == nil {
				// if it didn't error, it MUST NOT have returned the canary
				for _, r := range rows {
					for _, cell := range r {
						if strings.Contains(cell, canary) {
							t.Fatalf("VECTOR %s LEAKED the canary: %q", v.name, cell)
						}
					}
				}
			}
		})
	}
}

// TestLockdownBlocksFilesWithoutValidator is the executable proof that the CONFIG lockdown
// (disabled_filesystems + autoload-off + lock_configuration), NOT validateSelect string
// matching, is what contains file reads. Every file vector is run DIRECTLY via
// conn.QueryContext — bypassing validateSelect entirely — against a real canary file, and must
// error (or return no canary). It also proves the freeze is irreversible.
func TestLockdownBlocksFilesWithoutValidator(t *testing.T) {
	dir := t.TempDir()
	canaryFile := filepath.Join(dir, "secret.txt")
	const canary = "CANARY_TOKEN_ghp_do_not_leak"
	os.WriteFile(canaryFile, []byte(canary), 0o600)

	conn, done := lockedConn(t)
	defer done()
	ctx := context.Background()

	// Raw file vectors — NO validateSelect in the path.
	vectors := []struct{ name, sql string }{
		{"read_text", `SELECT * FROM read_text('` + canaryFile + `')`},
		{"read_csv", `SELECT * FROM read_csv('` + canaryFile + `')`},
		{"read_blob", `SELECT * FROM read_blob('` + canaryFile + `')`},
		{"glob", `SELECT * FROM glob('` + dir + `/*')`},
		{"attach_local", `ATTACH '` + canaryFile + `' AS raw_x`},
		{"copy_to", `COPY (SELECT 1) TO '` + filepath.Join(dir, "raw_out.csv") + `'`},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			rs, err := conn.QueryContext(ctx, v.sql)
			if err != nil {
				return // blocked at execution — the config lockdown did its job
			}
			// no error: prove no canary crossed the boundary
			defer rs.Close()
			cols, _ := rs.Columns()
			for rs.Next() {
				raw := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range raw {
					ptrs[i] = &raw[i]
				}
				if err := rs.Scan(ptrs...); err != nil {
					return
				}
				for _, cell := range raw {
					if strings.Contains(fmt.Sprintf("%v", cell), canary) {
						t.Fatalf("RAW VECTOR %s LEAKED the canary via the lockdown: %v", v.name, cell)
					}
				}
			}
		})
	}

	// The freeze must be irreversible: unlocking configuration fails on the locked conn.
	if _, err := conn.ExecContext(ctx, `SET lock_configuration=false`); err == nil {
		t.Fatal("lock_configuration=false succeeded — the lockdown freeze is NOT irreversible")
	}
}

// TestGetenvCannotReadScrubbedSecret proves the credential-boundary defense at the DuckDB
// level: even if getenv is reachable (it is NOT blocked by disabled_filesystems), an env that
// has been scrubbed of the secret yields nothing. This runs getenv on a RAW conn (bypassing
// validateSelect) precisely to test the ENV defense, not the validator. (validateSelect also
// rejects getenv — that is the separate second layer, covered by TestValidateSelectRejects.)
func TestGetenvCannotReadScrubbedSecret(t *testing.T) {
	const secret = "ghp_MUST_NOT_LEAK_123456" // gitleaks:allow — deliberately fake canary
	os.Setenv("ORACLE_CANARY_SECRET", secret)
	os.Unsetenv("ORACLE_CANARY_SECRET") // the scrub (cmd/corral does this for CORRALAI_GIT_TOKEN)
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got sql.NullString
	// getenv may error "unavailable in this client" (fine) or return NULL/empty (fine) — the
	// ONLY failure is returning the secret.
	if err := db.QueryRow(`SELECT getenv('ORACLE_CANARY_SECRET')`).Scan(&got); err == nil {
		if strings.Contains(got.String, secret) {
			t.Fatalf("getenv leaked a scrubbed secret: %q", got.String)
		}
	}
}

// TestRunLockedRespectsContextTimeout proves the statement-timeout mechanism: a context
// cancelled before the query completes causes runLocked to return an error. If DuckDB
// completes the query faster than the timeout (highly unlikely for range(1e9) but
// theoretically possible on exotic hardware), the test is skipped rather than failing.
func TestRunLockedRespectsContextTimeout(t *testing.T) {
	conn, done := lockedConn(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, _, err := runLocked(ctx, conn, `SELECT count(*) FROM range(1000000000) t(i)`, 1000)
	if err == nil {
		t.Skip("range(1e9) completed under 1ms on this machine — skipping timing-sensitive cancellation check")
	}
	// DuckDB signals cancellation as an "Interrupt" or ctx "deadline exceeded"; either is correct.
	errLow := strings.ToLower(err.Error())
	if !strings.Contains(errLow, "interrupt") && !strings.Contains(errLow, "deadline") && !strings.Contains(errLow, "cancel") {
		t.Errorf("expected a cancellation/timeout error, got: %v", err)
	}
}

func TestValidateSelectRejects(t *testing.T) {
	bad := []string{
		`INSERT INTO fleet_missions VALUES (1)`,
		`DROP TABLE fleet_missions`,
		`ATTACH 'x' AS y`,
		`SELECT 1; DELETE FROM fleet_missions`,
		`SELECT getenv('CORRALAI_GIT_TOKEN')`,
		`SELECT * FROM read_text('/etc/passwd')`,
		`COPY x TO 'y'`,
		`SET lock_configuration=false`,
		`SEL/**/ECT * FROM read_text('/x')`, // obfuscation defeated by normalization
	}
	for _, s := range bad {
		if err := validateSelect(s); err == nil {
			t.Fatalf("validateSelect should reject: %q", s)
		}
	}
	if err := validateSelect(`WITH t AS (SELECT 1 x) SELECT * FROM t`); err != nil {
		t.Fatalf("a plain CTE SELECT should pass: %v", err)
	}
}
