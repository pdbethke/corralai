// SPDX-License-Identifier: Elastic-2.0

// Package sqlguard is the shared read-only guard for corralai's ad-hoc-SQL
// surfaces (oracle / recordings / telemetry). It provides ValidateReadOnly (a
// normalize-then-reject validator that is defense-in-depth, never the sole wall)
// and ApplyLockdown (the DuckDB connection lockdown that IS the real wall).
package sqlguard

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

var (
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	wsRun        = regexp.MustCompile(`\s+`)

	// Statement keywords banned at WORD BOUNDARIES — so a keyword appearing inside
	// an identifier/alias/quoted literal (copyedit-task, read_count, a column named
	// "update_ts") is NOT a false positive, while a real `COPY ... TO` / `ATTACH` /
	// `PRAGMA` / `SET x=y` / DML statement is caught.
	bannedKeywords = regexp.MustCompile(`\b(attach|copy|install|load|pragma|set|call|export|delete|update|insert|create|alter|drop|truncate)\b`)

	// Dangerous filesystem/network/metadata FUNCTION calls — matched only as a call
	// `name(` so `AS read_count` / a column `getenv_x` is not a false positive, while
	// read_csv('/etc/passwd'), glob(...), getenv(...), parquet_scan(...),
	// arrow_scan(...), sniff_csv(...), duckdb_settings() are caught.
	bannedFuncCalls = regexp.MustCompile(`\b(read_[a-z0-9_]*|glob|parquet_scan|arrow_scan|sniff_csv|getenv|duckdb_[a-z0-9_]*)\s*\(`)

	// Metadata table/identifiers that legit analytics never reference and that the
	// engine lockdown does NOT block — reached WITHOUT a call, e.g. `FROM
	// sqlite_master` or `FROM duckdb_databases` (leaks attached-db names + on-disk
	// paths). Matched at a word boundary (no paren required); the call forms are
	// also covered by bannedFuncCalls.
	bannedMetaIdents = regexp.MustCompile(`\b(sqlite_[a-z0-9_]+|duckdb_[a-z0-9_]+)\b`)
)

// ValidateReadOnly normalizes userSQL (strips comments, collapses whitespace),
// requires a SINGLE SELECT/WITH statement, and rejects the banned constructs.
// It is defense-in-depth; ApplyLockdown is the real wall.
func ValidateReadOnly(userSQL string) error {
	s := blockComment.ReplaceAllString(userSQL, " ")
	s = lineComment.ReplaceAllString(s, " ")
	s = wsRun.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "select ") && !strings.HasPrefix(low, "with ") {
		return fmt.Errorf("only a single read-only SELECT/WITH query is allowed")
	}
	// reject any inner ';' (a single trailing one is fine)
	if i := strings.IndexByte(strings.TrimRight(s, "; "), ';'); i >= 0 {
		return fmt.Errorf("only a single read-only SELECT/WITH query is allowed")
	}
	if m := bannedKeywords.FindString(low); m != "" {
		return fmt.Errorf("query uses a disallowed construct (%q)", m)
	}
	if m := bannedFuncCalls.FindStringSubmatch(low); m != nil {
		return fmt.Errorf("query uses a disallowed construct (%q)", m[1])
	}
	if m := bannedMetaIdents.FindString(low); m != "" {
		return fmt.Errorf("query uses a disallowed construct (%q)", m)
	}
	return nil
}

// ApplyLockdown freezes a DuckDB *sql.Conn so a query on it can't reach the local
// filesystem, autoload extensions (httpfs → SSRF), or undo the lockdown. It MUST be
// applied to the SAME *sql.Conn the query then runs on (database/sql pools conns).
// This is the security wall; oracle additionally applies its own resource caps
// BEFORE calling this (lock_configuration must be last). `md:` (MotherDuck) survives.
//
// Idempotent by design: database/sql pools and REUSES *sql.Conns, so a conn that a
// prior ApplyLockdown already sealed (lock_configuration=true, which cannot be
// re-SET) may come back here. Re-running the SETs on it would error on the first
// statement even though the conn is already fully locked down. So we detect the
// already-locked state and return success — the conn is already the wall we want.
// (Callers like oracle that add their OWN pre-lockdown SETs use a dedicated pinned
// conn locked exactly once, so they never hit the reuse path.)
func ApplyLockdown(ctx context.Context, conn *sql.Conn) error {
	var locked string
	if err := conn.QueryRowContext(ctx, `SELECT CAST(current_setting('lock_configuration') AS VARCHAR)`).Scan(&locked); err == nil && locked == "true" {
		return nil
	}
	stmts := []string{
		`SET disabled_filesystems = 'LocalFileSystem'`,
		`SET autoinstall_known_extensions = false`,
		`SET autoload_known_extensions = false`,
		`SET allow_community_extensions = false`,
		`SET lock_configuration = true`, // MUST be last — freezes everything above
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sqlguard lockdown %q: %w", s, err)
		}
	}
	return nil
}
