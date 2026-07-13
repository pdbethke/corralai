// SPDX-License-Identifier: Elastic-2.0

package sqlguard

import "testing"

func TestValidateReadOnly_Allow(t *testing.T) {
	allow := []string{
		// previously false-rejected: 'copyedit-task' contains "copy", "read_count" contains "read_"
		`SELECT subject FROM events WHERE subject = 'copyedit-task'`,
		`SELECT count(*) AS read_count FROM events`,
		// legit function calls with parens that are NOT banned
		`SELECT date_trunc('day', to_timestamp(ts)) d, count(*) FROM events GROUP BY d`,
		// CTE
		`WITH x AS (SELECT 1) SELECT * FROM x`,
		// lowercase, order by
		`select model, count(*) from events group by model order by 2 desc`,
		// single trailing semicolon OK
		`SELECT * FROM events; `,
	}
	for _, q := range allow {
		if err := ValidateReadOnly(q); err != nil {
			t.Errorf("ValidateReadOnly(%q) = %v; want nil (must-allow)", q, err)
		}
	}
}

func TestValidateReadOnly_Reject(t *testing.T) {
	reject := []string{
		// filesystem/metadata function calls
		`SELECT * FROM read_csv('/etc/passwd')`,
		`SELECT * FROM read_text('/etc/passwd')`,
		`SELECT getenv('HOME')`,
		`SELECT * FROM glob('/*')`,
		`SELECT * FROM parquet_scan('x')`,
		`SELECT * FROM arrow_scan('x')`,
		`SELECT * FROM sniff_csv('x')`,
		`SELECT * FROM duckdb_settings()`,
		// metadata table
		`SELECT * FROM sqlite_master`,
		// non-SELECT statement keywords
		`ATTACH 'x.db' AS y`,
		`COPY (SELECT 1) TO '/tmp/x'`,
		`PRAGMA database_list`,
		`SET memory_limit='1GB'`,
		`INSTALL httpfs`,
		`LOAD httpfs`,
		`CALL foo()`,
		`EXPORT DATABASE 'x'`,
		// DML
		`DELETE FROM events`,
		`UPDATE events SET x=1`,
		`INSERT INTO events VALUES(1)`,
		`DROP TABLE events`,
		// multi-statement
		`SELECT 1; SELECT 2`,
		// non-select
		`EXPLAIN SELECT 1`,
		`PRAGMA x`,
		// comment-hidden second statement
		`SELECT 1 /* */ ; DROP TABLE events`,
		"SELECT 1 -- \nDROP TABLE events",
	}
	for _, q := range reject {
		if err := ValidateReadOnly(q); err == nil {
			t.Errorf("ValidateReadOnly(%q) = nil; want error (must-reject)", q)
		}
	}
}
