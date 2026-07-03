// SPDX-License-Identifier: Elastic-2.0

package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// applyLockdown freezes a connection that ALREADY has the reporting schema present so it can
// read only that schema — no local files, no new attach, no URL, no extension autoload. MUST
// be applied to the SAME *sql.Conn the query later runs on (database/sql pools connections).
func applyLockdown(ctx context.Context, conn *sql.Conn) error {
	stmts := []string{
		`SET disabled_filesystems = 'LocalFileSystem'`, // kills read_csv/read_text/read_blob/glob/local ATTACH+COPY; md: survives
		`SET autoinstall_known_extensions = false`,
		`SET autoload_known_extensions = false`, // kills httpfs autoload → no URL/SSRF
		`SET allow_community_extensions = false`,
		`SET memory_limit = '512MB'`,
		`SET threads = 2`,
		`SET max_expression_depth = 100`,
		`SET lock_configuration = true`, // freeze — nothing above can be undone
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("lockdown %q: %w", s, err)
		}
	}
	return nil
}

var (
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	wsRun        = regexp.MustCompile(`\s+`)
	// banned normalized substrings (defense-in-depth; the lockdown is the real wall)
	bannedSubstr = []string{
		"attach", "copy", "install ", "load ", "pragma", "set ", "call ", "export",
		"read_", "glob(", "sqlite_", "getenv", "parquet_scan", "duckdb_",
		"sniff_csv", "sniff", "arrow_scan",
	}
)

// validateSelect is defense-in-depth: normalize (strip comments, collapse whitespace) then
// require a single SELECT/WITH statement and reject the banned substrings. NEVER the sole wall.
func validateSelect(userSQL string) error {
	s := blockComment.ReplaceAllString(userSQL, " ")
	s = lineComment.ReplaceAllString(s, " ")
	s = wsRun.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "select ") && !strings.HasPrefix(low, "with ") {
		return fmt.Errorf("only SELECT/WITH queries are allowed")
	}
	// reject any inner ';' (a trailing one is fine)
	if i := strings.IndexByte(strings.TrimRight(s, "; "), ';'); i >= 0 {
		return fmt.Errorf("multiple statements are not allowed")
	}
	for _, b := range bannedSubstr {
		if strings.Contains(low, b) {
			return fmt.Errorf("query uses a disallowed construct (%q)", strings.TrimSpace(b))
		}
	}
	return nil
}

// runLocked validates then executes userSQL on the already-locked conn, scanning at most
// rowCap rows (the read cap is bulletproof regardless of what the query produces).
func runLocked(ctx context.Context, conn *sql.Conn, userSQL string, rowCap int) ([]string, [][]string, error) {
	if err := validateSelect(userSQL); err != nil {
		return nil, nil, err
	}
	rs, err := conn.QueryContext(ctx, userSQL)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	var rows [][]string
	for rs.Next() && len(rows) < rowCap {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(cols))
		for i, v := range raw {
			row[i] = fmt.Sprintf("%v", v)
		}
		rows = append(rows, row)
	}
	return cols, rows, rs.Err()
}
